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
	mu               sync.Mutex
	states           []State
	onAction         func(Action)
}

func (l *fakeLink) Start() { l.started.Store(true) }
func (l *fakeLink) Stop()  { l.stopped.Store(true) }
func (l *fakeLink) Update(s State) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states = append(l.states, s)
}

func (l *fakeLink) last(t *testing.T) State {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.states) == 0 {
		t.Fatal("no state")
	}
	return l.states[len(l.states)-1]
}

// count is how many states the link got.
func (l *fakeLink) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.states)
}

// hasState is whether any state the link got is one match accepts.
func (l *fakeLink) hasState(match func(State) bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.states {
		if match(s) {
			return true
		}
	}
	return false
}

type menuFixture struct {
	deps    MenuDeps
	link    *fakeLink
	log     *safeBuf
	file    string
	stopped chan int
	mu      sync.Mutex
	linked  map[string]bool
}

func newMenu(t *testing.T) *menuFixture {
	t.Helper()
	log, out := testLog()
	f := &menuFixture{link: &fakeLink{}, log: out, file: filepath.Join(t.TempDir(), "daemon.json"), stopped: make(chan int, 2), linked: map[string]bool{}}
	f.deps = MenuDeps{
		Open:         func(onAction func(Action)) Link { f.link.onAction = onAction; return f.link },
		RegistryFile: f.file,
		Hello: func() Hello {
			return Hello{Version: "0.0.0-test", Agents: []pagetools.AgentInfo{claude}}
		},
		Linked:      func(s string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.linked[s] },
		Sessions:    func() int { return 2 },
		GatewayPort: 47615,
		Restart:     func() {},
		Disconnect:  func(string) error { return nil },
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
	want := State{Version: "0.0.0-test", Apps: []string{app}, AppList: []AppRow{{Server: app, Status: AppConnected}, {Server: "https://b.example", Status: AppConnecting}}, Pages: []string{page}, PageList: []PageRow{{Origin: page}}, Sessions: 2, Agents: []pagetools.AgentInfo{claude}}
	got := f.link.last(t)
	// A page grant that names no end ends BrowserGrantTTL after it was made.
	if len(got.PageList) == 1 && time.Until(got.PageList[0].Ends).Round(time.Hour) == registry.BrowserGrantTTL {
		want.PageList[0].Ends = got.PageList[0].Ends
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A page whose grant lapsed is not connected any more: it must not be listed with a Disconnect it does not need.
func TestMenuListsOnlyThePagesWhoseGrantHasNotLapsed(t *testing.T) {
	f := newMenu(t)
	old := time.Now().Add(-registry.BrowserGrantTTL - time.Hour)
	r := registry.Empty().AddBrowser("https://old.example", old, registry.Access{}).AddBrowser(page, time.Now(), registry.Access{})
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
	if n := f.link.count(); n != 0 {
		t.Fatalf("got %d states for an unreadable registry", n)
	}
}

func TestMenuRefreshesWhenSomethingChanges(t *testing.T) {
	f := newMenu(t)
	f.start(t)
	waitFor(t, "the first state", 2*time.Second, func() bool { return f.link.count() >= 1 })
	writeRegistry(t, f.file, nil, []string{page})
	waitFor(t, "a state with the page", 2*time.Second, func() bool { return f.link.hasState(func(s State) bool { return len(s.Pages) == 1 }) })
}

func TestMenuEndStopsTheLinkAndTheRefresh(t *testing.T) {
	f := newMenu(t)
	end := StartMenu(f.deps)
	end()
	if !f.link.stopped.Load() {
		t.Fatal("the link was not stopped")
	}
	n := f.link.count()
	writeRegistry(t, f.file, nil, []string{page})
	time.Sleep(80 * time.Millisecond)
	if f.link.count() != n {
		t.Fatal("the menu went on refreshing after it ended")
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
	waitFor(t, "a state without the page", 2*time.Second, func() bool { return f.link.hasState(func(s State) bool { return len(s.Pages) == 0 }) })
	if len(f.stopped) != 0 {
		t.Fatal("the daemon was stopped")
	}
	if !contains(f.log.String(), "disconnect "+page+" requested from the app\n") {
		t.Fatalf("log: %q", f.log.String())
	}
}

func TestMenuDisconnectThatFailsIsLogged(t *testing.T) {
	f := newMenu(t)
	f.deps.Disconnect = func(string) error { return errors.New("no such app") }
	f.start(t)
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "the warning", time.Second, func() bool {
		return contains(f.log.String(), "app: cannot disconnect "+page+": no such app\n")
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
	if !f.link.hasState(func(s State) bool { return len(s.Pages) == 1 }) {
		t.Fatal("the first state does not list the page")
	}
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "a state without the page", time.Second, func() bool { return f.link.hasState(func(s State) bool { return len(s.Pages) == 0 }) })
}

func TestMenuDisconnectThatFailsSendsTheStateAgainAsItIs(t *testing.T) {
	f := newMenu(t)
	f.deps.Refresh = time.Hour
	writeRegistry(t, f.file, nil, []string{page})
	f.deps.Disconnect = func(string) error { return errors.New("no such app") }
	f.start(t)
	first := f.link.count()
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: page})
	waitFor(t, "the state asked for again", time.Second, func() bool { return f.link.count() > first })
	if s := f.link.last(t); len(s.Pages) != 1 {
		t.Fatalf("pages %v: a disconnect that failed must still list the page", s.Pages)
	}
}

func writeFile(file, text string) error { return os.WriteFile(file, []byte(text), 0o600) }

func contains(text, part string) bool { return strings.Contains(text, part) }
