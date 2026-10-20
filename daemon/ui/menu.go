package ui

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const refreshEvery = 2 * time.Second

// NoticeFor is how long a sentence about a choice that failed stays on the state, long enough to read.
const NoticeFor = 30 * time.Second

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
	Agents  []pagetools.AgentInfo
}

// MenuDeps is what the menu shows and what its choices do.
type MenuDeps struct {
	// Open makes the link, which calls onAction with each choice of the user: NewTray or NewAppLink.
	Open         func(onAction func(Action)) Link
	RegistryFile string
	Hello        func() Hello
	// Linked: whether the daemon is linked to the app at server now.
	Linked func(server string) bool
	// AppStatus is how a saved app stands, including why the daemon dropped it. Nil: a linked app is connected and any
	// other saved app is on its way to being linked.
	AppStatus func(server string) AppState
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
	// Acp is the address of the standard ACP endpoint with its token, asked for at each refresh; false while there is
	// no endpoint or no token. Nil: the daemon has no such endpoint.
	Acp func() (AcpState, bool)
	Log *proc.Logger

	// Optional: GOOS defaults to runtime.GOOS, Opener to a detached start of the command, Refresh to two seconds, Now
	// (the clock that tells which page grants have lapsed) to time.Now.
	GOOS    string
	Opener  func(Command) error
	Refresh time.Duration
	Now     func() time.Time
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

// notice is one sentence about a choice that failed, shown until it is read or a later choice works.
type notice struct {
	mu    sync.Mutex
	text  string
	until time.Time
}

func (n *notice) set(text string, now time.Time) {
	n.mu.Lock()
	n.text, n.until = text, now.Add(NoticeFor)
	n.mu.Unlock()
}

func (n *notice) clear() {
	n.mu.Lock()
	n.text = ""
	n.mu.Unlock()
}

func (n *notice) current(now time.Time) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.text == "" || !now.Before(n.until) {
		return ""
	}
	return n.text
}

// oneLineOf is the first line of an error, trimmed, without a trailing stop, and short enough for one menu line.
// An empty error says that no reason was given.
func oneLineOf(err error) string {
	s := ""
	if err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".")
	s = strings.TrimSpace(s)
	if s == "" {
		return "no reason was given"
	}
	if utf8.RuneCountInString(s) > 159 {
		return string([]rune(s)[:159]) + "…"
	}
	return s
}

// act does what the user chose; refresh sends the state again at once, for a choice that changes it. A choice that
// fails leaves one sentence for NoticeFor; one that works clears it.
func (d MenuDeps) act(action Action, refresh func(), note *notice, now func() time.Time) {
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
		// Sent again whether it worked or not: the row goes at once, or stays because the registry still lists it.
		go func() {
			if err := d.Disconnect(action.Origin); err != nil {
				d.Log.Warn("tray: cannot disconnect " + action.Origin + ": " + err.Error())
				note.set("Could not disconnect "+action.Origin+": "+oneLineOf(err)+".", now())
			} else {
				note.clear()
			}
			refresh()
		}()
	case ActionUpdates:
		// A setting that could not be saved is sent back as it was, so the window's switch goes back too.
		go func() {
			if err := d.SetUpdates(action.On); err != nil {
				d.Log.Warn("tray: cannot save the updates setting: " + err.Error())
				note.set("Could not save the updates setting: "+oneLineOf(err)+".", now())
			} else {
				note.clear()
			}
			refresh()
		}()
	}
}

// StartMenu starts the link (the tray program, or the app that started the daemon) and keeps it showing what the
// daemon serves. It returns what ends it.
func StartMenu(d MenuDeps) (end func()) {
	var link Link
	var note notice
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	status := d.AppStatus
	if status == nil {
		status = func(server string) AppState {
			if d.Linked(server) {
				return AppState{Status: AppConnected}
			}
			return AppState{Status: AppConnecting}
		}
	}
	push := func() {
		r, err := registry.Load(d.RegistryFile)
		if err != nil {
			return
		}
		hello := d.Hello()
		var servers, apps, pages []string
		for _, a := range r.Apps {
			servers = append(servers, a.Server)
			if d.Linked(a.Server) {
				apps = append(apps, a.Server)
			}
		}
		for _, b := range r.Browsers {
			if b.Live(now()) {
				pages = append(pages, b.Origin)
			}
		}
		var acp *AcpState
		if d.Acp != nil {
			if state, ok := d.Acp(); ok {
				acp = &state
			}
		}
		link.Update(NewState(Facts{
			Version:     hello.Version,
			Apps:        apps,
			AppList:     AppRows(servers, status),
			Notice:      note.current(now()),
			Browsers:    pages,
			Sessions:    d.Sessions(),
			Managed:     hello.Managed,
			GatewayPort: d.GatewayPort,
			LogFile:     d.LogFile,
			Agents:      hello.Agents,
			Updates:     d.Updates(),
			Updatable:   d.Updatable,
			Acp:         acp,
		}))
	}
	link = d.Open(func(action Action) { d.act(action, push, &note, now) })
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
