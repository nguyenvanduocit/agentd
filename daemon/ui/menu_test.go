package ui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// fakeLink records what the menu does with its link.
type fakeLink struct {
	started, stopped atomic.Bool
	states           lineLog
	onAction         func(Action)
}

func (l *fakeLink) Start() { l.started.Store(true) }
func (l *fakeLink) Stop()  { l.stopped.Store(true) }
func (l *fakeLink) Update(s State) {
	l.states.add(s.line())
}

func (l *fakeLink) last(t *testing.T) State {
	t.Helper()
	all := l.states.all()
	if len(all) == 0 {
		t.Fatal("no state")
	}
	s, _ := parseState(all[len(all)-1])
	return s
}

type menuFixture struct {
	deps    MenuDeps
	link    *fakeLink
	log     *safeBuf
	file    string
	stopped chan int
	mu      sync.Mutex
	updates bool
	linked  map[string]bool
}

func newMenu(t *testing.T) *menuFixture {
	t.Helper()
	log, out := testLog()
	f := &menuFixture{link: &fakeLink{}, log: out, file: filepath.Join(t.TempDir(), "daemon.json"), stopped: make(chan int, 2), updates: true, linked: map[string]bool{}}
	f.deps = MenuDeps{
		Open:         func(onAction func(Action)) Link { f.link.onAction = onAction; return f.link },
		RegistryFile: f.file,
		Hello: func() Hello {
			return Hello{Version: "0.0.0-test", Managed: true, Agents: []pagetools.AgentInfo{claude}}
		},
		Linked:      func(s string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.linked[s] },
		Sessions:    func() int { return 2 },
		GatewayPort: 47615,
		Stop:        func(code int) { f.stopped <- code },
		Restart:     func() {},
		Disconnect:  func(string) error { return nil },
		Updates:     func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.updates },
		Updatable:   true,
		SetUpdates:  func(on bool) error { f.mu.Lock(); defer f.mu.Unlock(); f.updates = on; return nil },
		Log:         log,
		Refresh:     20 * time.Millisecond,
	}
	return f
}

func (f *menuFixture) start(t *testing.T) {
	t.Helper()
	end := StartMenu(f.deps)
	t.Cleanup(end)
}

func TestMenuShowsWhatTheDaemonServes(t *testing.T) {
	f := newMenu(t)
	app := "https://a.example"
	writeRegistry(t, f.file, []string{app, "https://b.example"}, []string{page})
	f.linked[app] = true // b is paired but not linked now
	f.start(t)
	if !f.link.started.Load() {
		t.Fatal("the link was not started")
	}
	want := State{T: "state", Version: "0.0.0-test", Apps: []string{app}, Pages: []string{page}, Sessions: 2, Managed: true, Agents: []pagetools.AgentInfo{claude}, Updates: true, Updatable: true}
	if got := f.link.last(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A page whose grant lapsed is not connected any more: it must not be listed with a Disconnect it does not need.
func TestMenuListsOnlyThePagesWhoseGrantHasNotLapsed(t *testing.T) {
	f := newMenu(t)
	old := time.Now().Add(-registry.BrowserGrantTTL - time.Hour)
	r := registry.Empty().AddBrowser("https://old.example", old, nil).AddBrowser(page, time.Now(), nil)
	if err := registry.Save(f.file, r); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	if got := f.link.last(t).Pages; !reflect.DeepEqual(got, []string{page}) {
		t.Fatalf("pages = %v, want only %s", got, page)
	}
}

func TestMenuShowsNothingWhileTheRegistryCannotBeRead(t *testing.T) {
	f := newMenu(t)
	if err := writeFile(f.file, "{not json"); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	time.Sleep(80 * time.Millisecond)
	if n := len(f.link.states.all()); n != 0 {
		t.Fatalf("got %d states for an unreadable registry", n)
	}
}

func TestMenuRefreshesWhenSomethingChanges(t *testing.T) {
	f := newMenu(t)
	f.start(t)
	waitFor(t, "the first state", 2*time.Second, func() bool { return len(f.link.states.all()) >= 1 })
	writeRegistry(t, f.file, nil, []string{page})
	waitFor(t, "a state with the page", 2*time.Second, func() bool { return f.link.states.hasState(func(s State) bool { return len(s.Pages) == 1 }) })
}

func TestMenuEndStopsTheLinkAndTheRefresh(t *testing.T) {
	f := newMenu(t)
	end := StartMenu(f.deps)
	end()
	if !f.link.stopped.Load() {
		t.Fatal("the link was not stopped")
	}
	n := len(f.link.states.all())
	writeRegistry(t, f.file, nil, []string{page})
	time.Sleep(80 * time.Millisecond)
	if len(f.link.states.all()) != n {
		t.Fatal("the menu went on refreshing after it ended")
	}
}

func TestMenuStopStopsTheDaemonWith0(t *testing.T) {
	f := newMenu(t)
	f.start(t)
	f.link.onAction(Action{Kind: ActionStop})
	select {
	case code := <-f.stopped:
		if code != 0 {
			t.Fatalf("stopped with %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("not stopped")
	}
	if !contains(f.log.String(), "stop requested from the tray menu\n") {
		t.Fatalf("log: %q", f.log.String())
	}
}

func TestMenuRestartRestarts(t *testing.T) {
	f := newMenu(t)
	restarted := make(chan struct{}, 1)
	f.deps.Restart = func() { restarted <- struct{}{} }
	f.start(t)
	f.link.onAction(Action{Kind: ActionRestart})
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("no restart")
	}
}

func TestMenuDisconnectNamesTheOriginAndKeepsTheDaemonUp(t *testing.T) {
	f := newMenu(t)
	writeRegistry(t, f.file, nil, []string{page})
	got := make(chan string, 1)
	f.deps.Disconnect = func(origin string) error {
		_, err := registry.Update(f.file, func(r registry.Registry) registry.Registry { r.Browsers = []registry.BrowserGrant{}; return r })
		got <- origin
		return err
	}
	f.start(t)
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	select {
	case origin := <-got:
		if origin != page {
			t.Fatalf("disconnected %q", origin)
		}
	case <-time.After(time.Second):
		t.Fatal("no disconnect")
	}
	waitFor(t, "a state without the page", 2*time.Second, func() bool { return f.link.states.hasState(func(s State) bool { return len(s.Pages) == 0 }) })
	if len(f.stopped) != 0 {
		t.Fatal("the daemon was stopped")
	}
	if !contains(f.log.String(), "disconnect "+page+" requested from the tray menu\n") {
		t.Fatalf("log: %q", f.log.String())
	}
}

func TestMenuDisconnectThatFailsIsLogged(t *testing.T) {
	f := newMenu(t)
	f.deps.Disconnect = func(string) error { return errors.New("no such app") }
	f.start(t)
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "the warning", time.Second, func() bool {
		return contains(f.log.String(), "tray: cannot disconnect "+page+": no such app\n")
	})
}

// The refresh runs every two seconds; a person who chose Disconnect should not see the row for that long.
func TestMenuDisconnectShowsTheResultAtOnce(t *testing.T) {
	f := newMenu(t)
	f.deps.Refresh = time.Hour // only the choice itself refreshes
	writeRegistry(t, f.file, nil, []string{page})
	f.deps.Disconnect = func(string) error {
		_, err := registry.Update(f.file, func(r registry.Registry) registry.Registry { r.Browsers = []registry.BrowserGrant{}; return r })
		return err
	}
	f.start(t)
	if !f.link.states.hasState(func(s State) bool { return len(s.Pages) == 1 }) {
		t.Fatal("the first state does not list the page")
	}
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "a state without the page", time.Second, func() bool { return f.link.states.hasState(func(s State) bool { return len(s.Pages) == 0 }) })
}

func TestMenuDisconnectThatFailsSendsTheStateAgainAsItIs(t *testing.T) {
	f := newMenu(t)
	f.deps.Refresh = time.Hour
	writeRegistry(t, f.file, nil, []string{page})
	f.deps.Disconnect = func(string) error { return errors.New("no such app") }
	f.start(t)
	first := len(f.link.states.all())
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "the state asked for again", time.Second, func() bool { return len(f.link.states.all()) > first })
	if s := f.link.last(t); len(s.Pages) != 1 {
		t.Fatalf("pages %v: a disconnect that failed must still list the page", s.Pages)
	}
}

func TestMenuUpdatesSwitchSavesTheSettingAndShowsIt(t *testing.T) {
	f := newMenu(t)
	f.deps.Refresh = time.Hour // only the choice itself refreshes
	f.start(t)
	f.link.onAction(Action{Kind: ActionUpdates, On: false})
	waitFor(t, "a state with updates off", 2*time.Second, func() bool { return f.link.states.hasState(func(s State) bool { return !s.Updates }) })
}

func TestMenuUpdatesThatCannotBeSavedAreSentBackAsTheyWere(t *testing.T) {
	f := newMenu(t)
	f.deps.Refresh = time.Hour
	f.deps.SetUpdates = func(bool) error { return errors.New("read-only") }
	f.start(t)
	first := len(f.link.states.all())
	f.link.onAction(Action{Kind: ActionUpdates, On: false})
	waitFor(t, "the warning", time.Second, func() bool {
		return contains(f.log.String(), "tray: cannot save the updates setting: read-only\n")
	})
	// The state is the same as before, so the link got it again only because the link dedups: the menu asked.
	waitFor(t, "the state asked for again", time.Second, func() bool { return len(f.link.states.all()) > first })
	if s := f.link.last(t); !s.Updates {
		t.Fatalf("updates is %v, want the setting as it was (true)", s.Updates)
	}
}

func TestMenuLogsOpensTheLogFileWithThePlatformsOpener(t *testing.T) {
	f := newMenu(t)
	f.deps.LogFile = "/l/daemon.log"
	f.deps.GOOS = "darwin"
	opened := make(chan Command, 1)
	f.deps.Opener = func(c Command) error { opened <- c; return nil }
	f.start(t)
	f.link.onAction(Action{Kind: ActionLogs})
	if got := <-opened; !reflect.DeepEqual(got, Command{File: "open", Args: []string{"/l/daemon.log"}}) {
		t.Fatalf("opened %+v", got)
	}
	if s := f.link.last(t); !s.Log {
		t.Fatal("the state says there is no log to open")
	}
}

func TestMenuLogsWithoutAFileSaysTheDaemonWritesToItsTerminal(t *testing.T) {
	f := newMenu(t)
	f.deps.Opener = func(Command) error { t.Error("opened something"); return nil }
	f.start(t)
	f.link.onAction(Action{Kind: ActionLogs})
	if !contains(f.log.String(), "tray: no log file: this daemon writes to its terminal\n") {
		t.Fatalf("log: %q", f.log.String())
	}
}

func TestMenuLogsThatCannotOpenIsLogged(t *testing.T) {
	f := newMenu(t)
	f.deps.LogFile = "/l/daemon.log"
	f.deps.Opener = func(Command) error { return errors.New("no opener") }
	f.start(t)
	f.link.onAction(Action{Kind: ActionLogs})
	if !contains(f.log.String(), "tray: cannot open the log: no opener\n") {
		t.Fatalf("log: %q", f.log.String())
	}
}

func writeFile(file, text string) error { return os.WriteFile(file, []byte(text), 0o600) }

func contains(text, part string) bool { return strings.Contains(text, part) }
