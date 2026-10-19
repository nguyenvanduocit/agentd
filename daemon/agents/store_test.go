// The adapter store, driven as the daemon drives it: a fake `npm` (helpers_test.go) installs a fake adapter into a
// real adapters directory in the background, and a fleet runs the installed adapter.
package agents

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// newStore makes a store of a fresh home, stopped when the test ends.
func newStore(t *testing.T, env home.Env) (*Store, home.Layout) {
	t.Helper()
	l := home.LayoutOf(t.TempDir())
	s := NewStore(StoreOptions{Layout: l, Env: env})
	t.Cleanup(s.Stop)
	return s, l
}

func claudeStatus(s *Store, env home.Env) AgentStatus {
	return Status("claude", env, noPrograms, s.State)
}

// fleetOf is a fleet over the store that runs adapters with `sh`, whose frames go to sink.
func fleetOf(t *testing.T, s *Store, env home.Env, sink *recordingSink) *Fleet {
	t.Helper()
	f := NewFleet(FleetOptions{
		Cwd:     t.TempDir(),
		BinDir:  t.TempDir(),
		Env:     env,
		Adapter: s.State,
		Node:    "sh",
		State:   noGroups{},
		Sink:    func() FrameSink { return sink },
		Log:     proc.NewLogger(io.Discard, io.Discard),
	})
	t.Cleanup(func() { f.Shutdown(5 * time.Second) })
	return f
}

type noGroups struct{}

func (noGroups) Add(int)    {}
func (noGroups) Remove(int) {}

func TestADaemonWithItsAdapterInstalledInTheBackground(t *testing.T) {
	t.Run("claude is installing, two opens meanwhile are refused without spawning, then ready, and open runs the installed adapter", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		env := npmEnv(t, specFile(t), home.Env{"FAKE_NPM_PIDFILE": pidFile, "FAKE_NPM_SLEEP": "1"})
		s, l := newStore(t, env)
		sink := &recordingSink{}
		f := fleetOf(t, s, env, sink)
		s.Start()

		want := AgentStatus{Name: "claude", Title: "Claude Code", Reason: "installing the Claude adapter (first run only, this can take several minutes)"}
		if got := claudeStatus(s, env); got != want {
			t.Fatalf("got %+v", got)
		}
		for _, id := range []string{"one", "two"} {
			if _, err := f.Open(id, "claude"); err == nil || !strings.Contains(err.Error(), "the claude adapter is still installing on this machine") {
				t.Fatalf("open %s: %v", id, err)
			}
		}
		if got := sink.all(); len(got) != 0 {
			t.Fatalf("frames: %v", got)
		}

		waitFor(t, "claude ready", 30*time.Second, func() bool { return claudeStatus(s, env).Ready })
		opened, err := f.Open("three", "claude")
		if err != nil || opened.PID <= 0 {
			t.Fatalf("open: %+v, %v", opened, err)
		}
		waitFor(t, "the adapter's first line", 5*time.Second, func() bool { return len(sink.data(FrameOut)) > 0 })
		if got := sink.data(FrameOut)[0]; got != "fake adapter up" {
			t.Fatalf("got %q", got)
		}

		// on disk: one complete install, a marker, nothing partial
		dir := AdapterDir(l, Claude, AdapterKey(Adapters[Claude], env))
		if _, err := os.Stat(filepath.Join(dir, Marker)); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(AdapterAgentDir(l, Claude))
		if err != nil || len(entries) != 1 || entries[0].Name() != AdapterKey(Adapters[Claude], env) {
			t.Fatalf("entries: %v, %v", entries, err)
		}
	})

	t.Run("an install that was cut short leaves nothing that looks installed; the next start installs clean", func(t *testing.T) {
		env := npmEnv(t, specFile(t), nil)
		l := home.LayoutOf(t.TempDir())
		key := AdapterKey(Adapters[Claude], env)
		agentDir := AdapterAgentDir(l, Claude)
		// what a SIGKILLed daemon leaves: a partial directory of its npm run, and no final directory
		partial := PartialDir(l, Claude, key, 4242)
		mustMkdir(t, filepath.Join(partial, "node_modules", "half"))

		s := NewStore(StoreOptions{Layout: l, Env: env})
		t.Cleanup(s.Stop)
		s.Start()
		waitFor(t, "claude ready", 30*time.Second, func() bool { return claudeStatus(s, env).Ready })
		if _, err := os.Stat(partial); !os.IsNotExist(err) {
			t.Fatalf("the partial directory is still there: %v", err)
		}
		if _, err := os.Stat(filepath.Join(agentDir, key, Marker)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a directory with the final name and no marker (the old npx failure) is removed and installed again", func(t *testing.T) {
		env := npmEnv(t, specFile(t), nil)
		l := home.LayoutOf(t.TempDir())
		half := AdapterDir(l, Claude, AdapterKey(Adapters[Claude], env))
		mustMkdir(t, filepath.Join(half, "node_modules", "junk"))

		s := NewStore(StoreOptions{Layout: l, Env: env})
		t.Cleanup(s.Stop)
		s.Start()
		waitFor(t, "claude ready", 30*time.Second, func() bool { return claudeStatus(s, env).Ready })
		if _, err := os.Stat(filepath.Join(half, "node_modules", "junk")); !os.IsNotExist(err) {
			t.Fatalf("the junk is still there: %v", err)
		}
		if _, err := os.Stat(filepath.Join(half, Marker)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an install that fails says why in its reason, the store stays up, open says why", func(t *testing.T) {
		env := npmEnv(t, "file:/definitely/not/here.tgz", nil)
		s, _ := newStore(t, env)
		sink := &recordingSink{}
		f := fleetOf(t, s, env, sink)
		s.Start()
		waitFor(t, "a reason", 30*time.Second, func() bool {
			return strings.HasPrefix(claudeStatus(s, env).Reason, "cannot install the Claude adapter:")
		})
		got := claudeStatus(s, env)
		if got.Ready || !regexp.MustCompile(`retrying in \d+ s$`).MatchString(got.Reason) || !strings.Contains(got.Reason, "npm error") {
			t.Fatalf("got %+v", got)
		}
		_, err := f.Open("x", "claude")
		if err == nil || !strings.Contains(err.Error(), "agent claude is not ready: cannot install the Claude adapter:") {
			t.Fatalf("open: %v", err)
		}
		if got := sink.all(); len(got) != 0 {
			t.Fatalf("frames: %v", got)
		}
	})

	t.Run("a seam command makes the agent ready at once and installs nothing", func(t *testing.T) {
		env := home.Env{"PATH": systemPath(), SeamVariable("claude"): "sh -c 'sleep 60'", SeamVariable("codex"): "true"}
		s, l := newStore(t, env)
		s.Start()
		if got := claudeStatus(s, env); got != (AgentStatus{Name: "claude", Title: "Claude Code", Ready: true}) {
			t.Fatalf("got %+v", got)
		}
		time.Sleep(300 * time.Millisecond)
		if _, err := os.Stat(l.AdaptersDir); !os.IsNotExist(err) {
			t.Fatalf("adapters directory: %v", err)
		}
	})
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAnInstallGoesDownWithTheStore(t *testing.T) {
	// the adapter's postinstall (a child of npm) runs for 60 s and says its pid
	pidFile := filepath.Join(t.TempDir(), "pid")
	env := npmEnv(t, specFile(t), home.Env{"FAKE_NPM_PIDFILE": pidFile, "FAKE_NPM_SLEEP": "60"})
	s, l := newStore(t, env)
	s.Start()
	var pid int
	waitFor(t, "the install script to run", 10*time.Second, func() bool {
		text, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(text)))
		return err == nil
	})
	if !alive(pid) {
		t.Fatal("the install script is not running")
	}

	s.Stop()
	waitFor(t, "npm's child to be gone", 3*time.Second, func() bool { return !alive(pid) })
	agentDir := AdapterAgentDir(l, Claude)
	if _, err := os.Stat(filepath.Join(agentDir, AdapterKey(Adapters[Claude], env))); !os.IsNotExist(err) {
		t.Fatalf("an install exists: %v", err)
	}
	entries, _ := os.ReadDir(agentDir)
	for _, e := range entries {
		if IsPartial(e.Name()) {
			t.Fatalf("partial directory left: %s", e.Name())
		}
	}
}

func TestAdaptersOff(t *testing.T) {
	t.Run("an agent without a seam says the install is disabled; nothing is installed, npm does not run", func(t *testing.T) {
		npmLog := filepath.Join(t.TempDir(), "npm.log")
		env := npmEnv(t, specFile(t), home.Env{"AGENTD_ADAPTERS": "off", "FAKE_NPM_LOG": npmLog})
		delete(env, SeamVariable("codex"))
		s, l := newStore(t, env)
		sink := &recordingSink{}
		f := fleetOf(t, s, env, sink)
		s.Start()
		if got := claudeStatus(s, env); got != (AgentStatus{Name: "claude", Title: "Claude Code", Reason: "adapter install is disabled"}) {
			t.Fatalf("got %+v", got)
		}
		if _, err := f.Open("x", "claude"); err == nil || !strings.Contains(err.Error(), "agent claude is not ready: adapter install is disabled") {
			t.Fatalf("open: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		for _, path := range []string{l.AdaptersDir, npmLog} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("%s exists: %v", path, err)
			}
		}
	})

	t.Run("a seam still wins", func(t *testing.T) {
		env := home.Env{"AGENTD_ADAPTERS": "off", SeamVariable("claude"): "sh -c 'sleep 60'", "PATH": systemPath()}
		s, _ := newStore(t, env)
		s.Start()
		if !claudeStatus(s, env).Ready {
			t.Fatal("a seam makes claude ready")
		}
	})
}

func TestStoreRetries(t *testing.T) {
	t.Run("a failed install is tried again with backoff and succeeds when the package appears", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "late.tgz")
		env := npmEnv(t, "file:"+missing, nil)
		l := home.LayoutOf(t.TempDir())
		var mu sync.Mutex
		var changes []string
		s := NewStore(StoreOptions{Layout: l, Env: env, Retry: home.BackoffOptions{Base: 400 * time.Millisecond, Max: 400 * time.Millisecond}})
		s.OnChange(func() {
			state, _ := s.State(Claude)
			mu.Lock()
			defer mu.Unlock()
			changes = append(changes, string(state.Kind))
		})
		s.Start()
		defer s.Stop()
		if state, ok := s.State(Claude); !ok || state.Kind != AdapterInstalling {
			t.Fatalf("state: %+v, %v", state, ok)
		}
		if _, ok := s.State(Codex); ok {
			t.Fatal("codex has a seam: the store has no state for it")
		}
		waitFor(t, "a failure", 30*time.Second, func() bool { state, _ := s.State(Claude); return state.Kind == AdapterFailed })
		if err := os.WriteFile(missing, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the retry to succeed", 30*time.Second, func() bool { state, _ := s.State(Claude); return state.Kind == AdapterReady })
		mu.Lock()
		defer mu.Unlock()
		if changes[0] != "failed" || changes[len(changes)-1] != "ready" {
			t.Fatalf("changes: %v", changes)
		}
	})

	t.Run("a complete install on disk is ready at start, without npm", func(t *testing.T) {
		l := home.LayoutOf(t.TempDir())
		env := home.Env{"PATH": "/nonexistent", SeamVariable("codex"): "true"}
		dir := AdapterDir(l, Claude, AdapterKey(Adapters[Claude], env))
		mustMkdir(t, filepath.Join(dir, "node_modules", "x"))
		if err := os.WriteFile(filepath.Join(dir, "node_modules", "x", "run.js"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, Marker), []byte(`{"spec":"s","bin":"node_modules/x/run.js"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		s := NewStore(StoreOptions{Layout: l, Env: env})
		defer s.Stop()
		s.Start()
		got, ok := s.State(Claude)
		want := AdapterState{Kind: AdapterReady, Bin: filepath.Join(dir, "node_modules", "x", "run.js")}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, %v", got, ok)
		}
	})
}

func TestReadInstalled(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadInstalled(dir); ok {
		t.Fatal("a directory without a marker is not installed")
	}
	if err := os.WriteFile(filepath.Join(dir, Marker), []byte(MarkerText("s", "bin.js")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadInstalled(dir); ok {
		t.Fatal("a marker whose file is gone is not installed")
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := ReadInstalled(dir); !ok || got != filepath.Join(dir, "bin.js") {
		t.Fatalf("got %q, %v", got, ok)
	}
}

func TestOldInstallsAreRemovedAfterAnInstall(t *testing.T) {
	env := npmEnv(t, specFile(t), nil)
	l := home.LayoutOf(t.TempDir())
	agentDir := AdapterAgentDir(l, Claude)
	for i, name := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		dir := filepath.Join(agentDir, name)
		mustMkdir(t, dir)
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(StoreOptions{Layout: l, Env: env})
	t.Cleanup(s.Stop)
	s.Start()
	waitFor(t, "claude ready", 30*time.Second, func() bool { return claudeStatus(s, env).Ready })
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"0.3.0", AdapterKey(Adapters[Claude], env)}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
}
