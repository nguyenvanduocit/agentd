//go:build !windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
)

// How long Quit waits for the engine to end its agents before it ends them itself.
const quitGrace = 5 * time.Second

// desktop is agentd.app: the menu and the window of the tray, and the engine it runs.
type desktop struct {
	app
	files   EngineFiles
	home    string
	path    string
	engine  *Engine
	crashes int
	// quitting: Quit closed the engine's channel and waits for it to exit before the app does.
	quitting bool
	// launch runs the engine: StartEngine, or a stand-in in a test.
	launch func(f EngineFiles, home, path string, onLine func(line []byte)) (*Engine, error)
	// restart is the wait before the start that follows a crash; there is at most one.
	restart *time.Timer
}

// startApp makes this process agentd.app, run once; false when one runs already (it gets to show its window).
func startApp() bool {
	if !mygo.App.RequestSingleInstanceLock() {
		return false
	}
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	// The app updates itself, engine included: a check in the background once a day, the update window in native UI.
	mygo.Use(native.Plugin)
	d := &desktop{launch: StartEngine}
	d.report = func(id string) {
		if d.engine != nil {
			d.engine.Send(id)
		}
	}
	d.reply = func(line []byte) {
		if d.engine != nil {
			d.engine.Write(line)
		}
	}
	d.own = d.handle
	d.settings = Settings{State: State{}, Host: Host{App: true, Running: true}, send: d.choose}
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnSecondInstance(func([]string, string) { d.showSettings() })
	mygo.App.OnActivate(func(visible bool) {
		if !visible {
			d.showSettings()
		}
	})
	mygo.App.OnBeforeQuit(d.beforeQuit)
	mygo.App.WhenReady(d.ready)
	return true
}

func (d *desktop) ready() {
	d.makeTray()
	d.home, _ = os.UserHomeDir()
	// A moved home (AGENTD_HOME) is a test or a trial copy, not the user's install: it takes over no service and makes
	// no login item.
	if os.Getenv("AGENTD_HOME") == "" {
		if took, err := TakeOverCLIService(d.home); took {
			log.Printf("agentd.app took over the service of a terminal install (%v)", err)
		}
		d.loginByDefault()
	}
	d.settings.Host.OpenAtLogin = mygo.App.OpenAtLogin()
	if !mygo.App.WasOpenedAtLogin() {
		d.showSettings()
	}
	resources, _ := mygo.App.Path(mygo.PathResources)
	files, err := FindEngine(resources, d.home, os.Getenv)
	d.files = files // its log path is known even when the engine is not found
	if err != nil {
		d.failed(err)
		return
	}
	// The login shell may take a moment: the main thread does not wait for it.
	go func() {
		path := EnginePath(files.NodeBin(), LoginPath())
		mygo.RunOnMain(func() {
			d.path = path
			d.start()
		})
	}()
}

// loginByDefault makes agentd a login item the first time the app runs: apps find it after a restart of the Mac. The
// window's switch turns it off, and the app never turns it on again by itself.
func (d *desktop) loginByDefault() {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return
	}
	mark := filepath.Join(dir, "login-item-offered")
	if exists(mark) {
		return
	}
	if err := mygo.App.SetOpenAtLogin(true); err != nil {
		log.Println("open at login:", err)
	}
	_ = os.WriteFile(mark, nil, 0o644)
}

// start runs the engine; it reports its exit to ended. It does nothing while one runs: the wait after a crash and the
// Start the person chooses both come here, and a second engine on the same home would have nobody to end it. A start
// ends the wait that was left, which has nothing more to do.
func (d *desktop) start() {
	d.cancelRestart()
	if d.engine != nil {
		return
	}
	e, err := d.launch(d.files, d.home, d.path, d.line)
	if err != nil {
		d.failed(err)
		return
	}
	d.engine = e
	d.settings.Host.Running, d.settings.Host.Note = true, ""
	d.refresh()
	go func() {
		<-e.Done()
		mygo.RunOnMain(func() { d.ended(e) })
	}()
}

// failed shows why the engine did not start in a sentence a person can use, and keeps the error as it came in the log.
func (d *desktop) failed(err error) {
	log.Printf("agentd could not start: %v", err)
	NoteInLog(d.files.Log, "agentd.app could not start agentd: "+err.Error())
	d.stopped(PlainReason(err))
}

func (d *desktop) stopped(note string) {
	d.settings.Host.Running, d.settings.Host.Note = false, note
	d.settings.State = State{}
	d.refresh()
}

func (d *desktop) ended(e *Engine) {
	if d.engine != e {
		return
	}
	d.engine = nil
	if d.quitting {
		mygo.App.Quit()
		return
	}
	next, crashes := AfterExit(e.ExitCode(), e.RanFor(), d.crashes)
	d.crashes = crashes
	if !next.Restart {
		d.stopped(next.Note)
		return
	}
	d.stopped("agentd is starting again")
	d.cancelRestart()
	d.restart = time.AfterFunc(next.Delay, func() { mygo.RunOnMain(d.start) })
}

// cancelRestart ends the wait before a start that follows a crash, if there is one.
func (d *desktop) cancelRestart() {
	if d.restart != nil {
		d.restart.Stop()
		d.restart = nil
	}
}

// beforeQuit holds the quit until the engine has ended its agents: it closes its channel, and ended quits again.
func (d *desktop) beforeQuit(q *mygo.QuitEvent) {
	if d.engine == nil {
		return
	}
	q.PreventDefault()
	if d.quitting {
		return
	}
	d.quitting = true
	go d.engine.Stop(quitGrace)
}

// handle does what the app keeps for itself: Start, Quit, the log and the login item.
func (d *desktop) handle(id string) bool {
	switch id {
	case StartID:
		if d.engine == nil && d.files.Engine != "" {
			d.crashes = 0
			d.start()
		}
	case QuitID:
		mygo.App.Quit()
	case CheckUpdatesID:
		updater.CheckForUpdates()
	case "logs":
		_ = exec.Command("open", d.files.Log).Start()
	case LoginOnID, LoginOffID:
		if err := mygo.App.SetOpenAtLogin(id == LoginOnID); err != nil {
			log.Println("open at login:", err)
		}
		d.settings.Host.OpenAtLogin = mygo.App.OpenAtLogin()
		d.refresh()
	default:
		return false
	}
	return true
}
