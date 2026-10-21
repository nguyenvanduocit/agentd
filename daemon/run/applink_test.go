package run

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// The daemon as agentd.app runs it: inside the app's process, with a UI that shows its state and does what the app
// chooses. The tests run it inside the test process, with a fake UI in the app's place.

const pageOrigin = "http://localhost:5173"

// fakeUI is the app: it keeps the states the daemon sends and can choose.
type fakeUI struct {
	mu       sync.Mutex
	states   []ui.State
	onAction func(ui.Action)
}

// open is run.Deps.UI.
func (f *fakeUI) open(onAction func(ui.Action)) ui.Link {
	f.mu.Lock()
	f.onAction = onAction
	f.mu.Unlock()
	return f
}

func (f *fakeUI) Start() {}
func (f *fakeUI) Stop()  {}
func (f *fakeUI) Update(s ui.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, s)
}

// choose is the user choosing in the app's menu.
func (f *fakeUI) choose(t *testing.T, a ui.Action) {
	t.Helper()
	f.mu.Lock()
	act := f.onAction
	f.mu.Unlock()
	if act == nil {
		t.Fatal("the daemon never opened the link")
	}
	act(a)
}

// state is the first state that pred accepts.
func (f *fakeUI) state(t *testing.T, what string, pred func(ui.State) bool) ui.State {
	t.Helper()
	var found ui.State
	waitFor(t, what, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, s := range f.states {
			if pred(s) {
				found = s
				return true
			}
		}
		return false
	})
	return found
}

// latest is the newest state; false before there is one.
func (f *fakeUI) latest() (ui.State, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 {
		return ui.State{}, false
	}
	return f.states[len(f.states)-1], true
}

func anyState(ui.State) bool { return true }

// startWithApp runs the daemon inside this process with a fake app as its UI, as agentd.app does.
func startWithApp(t *testing.T, box *sandbox, env map[string]string) (*daemonProc, *fakeUI) {
	t.Helper()
	app := &fakeUI{}
	environ := home.Env{}
	for _, kv := range box.environ(env) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			environ[k] = v
		}
	}
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemonProc{out: out, exited: make(chan struct{}), end: cancel}
	deps := Deps{
		Log:        proc.NewLogger(out, out),
		Host:       home.Host{Platform: runtime.GOOS, Env: environ, Home: box.root},
		Executable: binaries.agentd,
		UI:         app.open,
	}
	go func() {
		d.code = Daemon(ctx, cli.Run{Dir: &box.work}, deps)
		close(d.exited)
	}()
	t.Cleanup(d.stop)
	return d, app
}

func startAppMode(t *testing.T) (*sandbox, *daemonProc, *fakeUI) {
	t.Helper()
	box := newSandbox(t)
	box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: registry.GrantStamp(time.Now())}})
	d, app := startWithApp(t, box, nil)
	return box, d, app
}

func TestAppModeSendsItsStateToTheApp(t *testing.T) {
	_, _, app := startAppMode(t)
	state := app.state(t, "a state", anyState)
	if len(state.Pages) != 1 || state.Pages[0] != pageOrigin {
		t.Fatalf("pages = %v", state.Pages)
	}
	if state.Version != testVersion && state.Version != home.Version {
		t.Fatalf("version = %v", state.Version)
	}
}

func TestAppModeDoesWhatTheAppChooses(t *testing.T) {
	box, _, app := startAppMode(t)
	app.state(t, "a state", anyState)
	app.choose(t, ui.Action{Kind: ui.ActionDisconnect, Origin: pageOrigin})
	reg := func() registry.Registry {
		r, err := registry.Load(box.configFile())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	waitFor(t, "the page disconnected", func() bool { return len(reg().Browsers) == 0 })
	app.state(t, "a state without the page", func(s ui.State) bool { return len(s.Pages) == 0 })
}

func TestAppModeEndsWhenTheAppEndsIt(t *testing.T) {
	_, d, app := startAppMode(t)
	app.state(t, "a state", anyState)
	d.signal(0)
	if code := d.exitCode(t, 15*time.Second); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
}
