package agents

import (
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// groupLog records the groups of the agents like proc.SessionState does.
type groupLog struct {
	mu      sync.Mutex
	added   []int
	removed []int
}

func (g *groupLog) Add(pgid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.added = append(g.added, pgid)
}

func (g *groupLog) Remove(pgid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removed = append(g.removed, pgid)
}

func (g *groupLog) snapshot() (added, removed []int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.added...), append([]int(nil), g.removed...)
}

// seamFleet is a fleet whose claude is the seam `agent` (a command line), with the groups and ended ids it saw.
func seamFleet(t *testing.T, agent string) (*Fleet, *recordingSink, *groupLog, *[]string) {
	t.Helper()
	env := home.Env{"PATH": systemPath(), SeamVariable("claude"): agent}
	sink := &recordingSink{}
	groups := &groupLog{}
	var mu sync.Mutex
	var ended []string
	f := NewFleet(FleetOptions{
		Cwd:     t.TempDir(),
		BinDir:  t.TempDir(),
		Env:     env,
		Adapter: noAdapter,
		State:   groups,
		Sink:    func() FrameSink { return sink },
		Ended: func(id string) {
			mu.Lock()
			defer mu.Unlock()
			ended = append(ended, id)
		},
		Log: proc.NewLogger(io.Discard, io.Discard),
	})
	t.Cleanup(func() { f.Shutdown(5 * time.Second) })
	return f, sink, groups, &ended
}

func TestFleetOpen(t *testing.T) {
	t.Run("runs the agent in the cwd with the wrapper's directory first on its PATH, and answers with its pid", func(t *testing.T) {
		f, sink, groups, _ := seamFleet(t, `sh -c 'pwd; echo "$PATH"; cat'`)
		opened, err := f.Open("s1", "claude")
		if err != nil || opened.PID <= 0 || opened.Cwd != f.o.Cwd {
			t.Fatalf("got %+v, %v", opened, err)
		}
		waitFor(t, "two lines", 5*time.Second, func() bool { return len(sink.data(FrameOut)) >= 2 })
		out := sink.data(FrameOut)
		if !strings.HasSuffix(out[0], f.o.Cwd) && !strings.HasSuffix(f.o.Cwd, out[0]) { // /var vs /private/var
			t.Fatalf("cwd: %q, want %q", out[0], f.o.Cwd)
		}
		if !strings.HasPrefix(out[1], f.o.BinDir+":") {
			t.Fatalf("PATH: %q", out[1])
		}
		if added, _ := groups.snapshot(); !reflect.DeepEqual(added, []int{opened.PID}) || f.Size() != 1 {
			t.Fatalf("added %v, size %d", added, f.Size())
		}
	})

	t.Run("a written line reaches the agent; closing it ends it, tells the state and the owner, and frees the id", func(t *testing.T) {
		f, sink, groups, ended := seamFleet(t, "cat")
		opened, err := f.Open("s1", "claude")
		if err != nil {
			t.Fatal(err)
		}
		if !f.Write("s1", "ping") || f.Write("nobody", "x") {
			t.Fatal("Write")
		}
		waitFor(t, "the echo", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 1 })
		if got := sink.data(FrameOut)[0]; got != "ping" {
			t.Fatalf("got %q", got)
		}
		f.Close("s1")
		waitFor(t, "the exit frame", 5*time.Second, func() bool { _, ok := sink.exit(); return ok })
		waitFor(t, "the session to leave the fleet", 5*time.Second, func() bool { return f.Size() == 0 })
		if _, removed := groups.snapshot(); !reflect.DeepEqual(removed, []int{opened.PID}) {
			t.Fatalf("removed %v", removed)
		}
		if !reflect.DeepEqual(*ended, []string{"s1"}) {
			t.Fatalf("ended %v", *ended)
		}
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatalf("the id opens again: %v", err)
		}
	})

	t.Run("refuses what cannot open", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "cat")
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ id, agent, want string }{
			{"", "claude", "empty session id"},
			{"s1", "claude", "session s1 exists"},
			{"s2", "rm", `unknown agent "rm": this daemon runs claude, codex, grok`},
			{"s2", "codex", "the codex adapter is still installing on this machine"},
		} {
			if _, err := f.Open(c.id, c.agent); err == nil || err.Error() != c.want {
				t.Errorf("open %q %q: got %v, want %q", c.id, c.agent, err, c.want)
			}
		}
	})

	t.Run("a command that is not on PATH is named", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "/nonexistent/agentd-test-agent --x")
		if _, err := f.Open("s1", "claude"); err == nil || err.Error() != `agent claude: command "/nonexistent/agentd-test-agent" not found in PATH` {
			t.Fatalf("got %v", err)
		}
	})
}

func TestFleetShutdown(t *testing.T) {
	f, sink, _, _ := seamFleet(t, "cat")
	for _, id := range []string{"a", "b"} {
		if _, err := f.Open(id, "claude"); err != nil {
			t.Fatal(err)
		}
	}
	f.Shutdown(5 * time.Second)
	if f.Size() != 0 {
		t.Fatalf("size %d", f.Size())
	}
	exits := 0
	for _, fr := range sink.all() {
		if fr.Kind == FrameExit {
			exits++
		}
	}
	if exits != 2 {
		t.Fatalf("exit frames: %d", exits)
	}
	if _, err := f.Open("c", "claude"); err == nil || err.Error() != "this daemon is stopping" {
		t.Fatalf("got %v", err)
	}
}

func TestFleetEndAll(t *testing.T) {
	f, sink, _, _ := seamFleet(t, "cat")
	if _, err := f.Open("a", "claude"); err != nil {
		t.Fatal(err)
	}
	f.EndAll()
	waitFor(t, "the exit frame", 5*time.Second, func() bool { _, ok := sink.exit(); return ok })
	if _, err := f.Open("b", "claude"); err != nil {
		t.Fatalf("a socket that returns may open again: %v", err)
	}
}

func TestFleetFolders(t *testing.T) {
	allowed := t.TempDir()
	elsewhere := t.TempDir()
	newLine := func(id int, cwd string) string {
		return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"session/new","params":{"cwd":"` + cwd + `"}}`
	}
	limited := func(t *testing.T) (*Fleet, *recordingSink) {
		f, sink, _, _ := seamFleet(t, "cat")
		f.o.Folders = func() ([]string, error) { return []string{filepath.Join(allowed, "missing"), allowed}, nil }
		return f, sink
	}

	t.Run("the agent starts in the first folder that exists", func(t *testing.T) {
		f, _ := limited(t)
		opened, err := f.Open("s1", "claude")
		if err != nil || opened.Cwd != allowed {
			t.Fatalf("got %+v, %v; want it in %s", opened, err, allowed)
		}
	})

	t.Run("a session inside the folders reaches the agent, one outside it is answered with an error", func(t *testing.T) {
		f, sink := limited(t)
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		inside := newLine(1, filepath.Join(allowed, "project"))
		if !f.Write("s1", inside) || !f.Write("s1", newLine(2, elsewhere)) {
			t.Fatal("Write")
		}
		waitFor(t, "the echo of the first line and the error for the second", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 2 })
		// Replies are matched by id, not by order: the refusal does not wait for the agent's echo.
		var echoed, refused string
		for _, line := range sink.data(FrameOut) {
			if strings.Contains(line, `"method"`) {
				echoed = line
			} else {
				refused = line
			}
		}
		if !strings.Contains(echoed, `"id":1`) || !strings.Contains(echoed, `"method":"session/new"`) {
			t.Errorf("the agent should have echoed the first request, got %q", echoed)
		}
		if !strings.Contains(refused, `"id":2`) || !strings.Contains(refused, `"error"`) {
			t.Errorf("the client should get an error for request 2, got %q", refused)
		}
	})

	t.Run("no folders is no limit", func(t *testing.T) {
		f, sink, _, _ := seamFleet(t, "cat")
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		f.Write("s1", newLine(1, elsewhere))
		waitFor(t, "the echo", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 1 })
		if strings.Contains(sink.data(FrameOut)[0], `"error"`) {
			t.Errorf("got %q", sink.data(FrameOut)[0])
		}
	})

	t.Run("an unreadable list refuses the open", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "cat")
		f.o.Folders = func() ([]string, error) { return nil, errors.New("daemon.json is unreadable") }
		if _, err := f.Open("s1", "claude"); err == nil || !strings.Contains(err.Error(), "daemon.json is unreadable") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("folders that do not exist refuse the open", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "cat")
		f.o.Folders = func() ([]string, error) { return []string{filepath.Join(allowed, "gone")}, nil }
		if _, err := f.Open("s1", "claude"); err == nil || !strings.Contains(err.Error(), "none of the folders you allowed") {
			t.Fatalf("got %v", err)
		}
	})
}
