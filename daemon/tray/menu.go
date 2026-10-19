package tray

import (
	"os/exec"
	"runtime"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagemcp"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const refreshEvery = 2 * time.Second

// File is the tray program to start: AGENTD_TRAY (a path; "off" for none) or the one packaged beside the engine, in
// dir; "" for none. exists is the file probe.
func File(env home.Env, dir, goos, goarch string, exists func(string) bool) string {
	seam := env["AGENTD_TRAY"]
	if seam == "off" {
		return ""
	}
	if seam != "" {
		return seam
	}
	packaged := Binary(dir, goos, goarch)
	if packaged != "" && exists(packaged) {
		return packaged
	}
	return ""
}

// Hello is what the daemon tells its apps of itself (the contract's Hello): the part the tray shows.
type Hello struct {
	Version string
	// Managed: a service restarts the daemon when it exits 75.
	Managed bool
	Agents  []pagemcp.AgentInfo
}

// MenuDeps is what the menu shows and what its choices do.
type MenuDeps struct {
	// Open makes the link, which calls onAction with each choice of the user: NewTray or NewAppLink.
	Open         func(onAction func(Action)) Link
	RegistryFile string
	Hello        func() Hello
	// Linked: whether the daemon is linked to the app at server now.
	Linked func(server string) bool
	// Sessions is the agents running for apps and for web pages.
	Sessions func() int
	// GatewayPort is the port of the local gateway, 0 when none was free.
	GatewayPort int
	// LogFile is the daemon's log file, when it runs as an installed service; "" otherwise.
	LogFile    string
	Stop       func(code int)
	Restart    func()
	Disconnect func(origin string) error
	// Updates is the setting "install updates automatically".
	Updates func() bool
	// Updatable: whether this daemon can update itself at all; the setting changes nothing otherwise.
	Updatable bool
	// SetUpdates saves the updates setting and turns the checks on or off.
	SetUpdates func(on bool) error
	Log        *proc.Logger

	// Optional: GOOS defaults to runtime.GOOS, Opener to a detached start of the command, Refresh to two seconds.
	GOOS    string
	Opener  func(Command) error
	Refresh time.Duration
}

// openDetached starts the command and leaves it running on its own; it is reaped when it ends.
func openDetached(c Command) error {
	cmd := exec.Command(c.File, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (d MenuDeps) openLog() {
	if d.LogFile == "" {
		d.Log.Say("tray: no log file: this daemon writes to its terminal")
		return
	}
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	open := d.Opener
	if open == nil {
		open = openDetached
	}
	if err := open(OpenCommand(goos, d.LogFile)); err != nil {
		d.Log.Warn("tray: cannot open the log: " + err.Error())
	}
}

// act does what the user chose; refresh sends the state again at once, for a choice that changes it.
func (d MenuDeps) act(action Action, refresh func()) {
	switch action.Kind {
	case ActionLogs:
		d.openLog()
	case ActionRestart:
		d.Restart()
	case ActionStop:
		d.Log.Say("stop requested from the tray menu")
		d.Stop(home.ExitStopped)
	case ActionDisconnect:
		d.Log.Say("disconnect " + action.Origin + " requested from the tray menu")
		go func() {
			if err := d.Disconnect(action.Origin); err != nil {
				d.Log.Warn("tray: cannot disconnect " + action.Origin + ": " + err.Error())
			}
		}()
	case ActionUpdates:
		// A setting that could not be saved is sent back as it was, so the window's switch goes back too.
		go func() {
			if err := d.SetUpdates(action.On); err != nil {
				d.Log.Warn("tray: cannot save the updates setting: " + err.Error())
			}
			refresh()
		}()
	}
}

// StartMenu starts the link (the tray program, or the app that started the daemon) and keeps it showing what the
// daemon serves. It returns what ends it.
func StartMenu(d MenuDeps) (end func()) {
	var link Link
	push := func() {
		r, err := registry.Load(d.RegistryFile)
		if err != nil {
			return
		}
		hello := d.Hello()
		var apps, pages []string
		for _, a := range r.Apps {
			if d.Linked(a.Server) {
				apps = append(apps, a.Server)
			}
		}
		for _, b := range r.Browsers {
			pages = append(pages, b.Origin)
		}
		link.Update(NewState(Facts{
			Version:     hello.Version,
			Apps:        apps,
			Browsers:    pages,
			Sessions:    d.Sessions(),
			Managed:     hello.Managed,
			GatewayPort: d.GatewayPort,
			LogFile:     d.LogFile,
			Agents:      hello.Agents,
			Updates:     d.Updates(),
			Updatable:   d.Updatable,
		}))
	}
	link = d.Open(func(action Action) { d.act(action, push) })
	link.Start()
	push()
	every := d.Refresh
	if every == 0 {
		every = refreshEvery
	}
	ticker := time.NewTicker(every)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-ticker.C:
				push()
			case <-done:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
		<-finished
		link.Stop()
	}
}
