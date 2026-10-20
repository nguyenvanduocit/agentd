//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// fakeEngine is the compiled testdata/fakeengine: the app runs it as it runs agentd (`<engine> run`), and the
// environment tells it where to write its choices and whether to ignore its channel.
var fakeEngine string

func TestMain(m *testing.M) {
	os.Exit(buildFakeEngine(m))
}

func buildFakeEngine(m *testing.M) int {
	dir, err := os.MkdirTemp("", "fakeengine")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fakeEngine = filepath.Join(dir, "agentd")
	if out, err := exec.Command("go", "build", "-o", fakeEngine, "./testdata/fakeengine").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build the fake engine: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// start runs the fake engine as the app runs agentd.
func start(t *testing.T, stubborn bool) (*Engine, chan State, string) {
	t.Helper()
	dir := t.TempDir()
	got := filepath.Join(dir, "got")
	t.Setenv("FAKE_ENGINE_GOT", got)
	if stubborn {
		t.Setenv("FAKE_ENGINE_STUBBORN", "1")
	}
	f := EngineFiles{Engine: fakeEngine, Home: dir, Log: filepath.Join(dir, "logs", "daemon.log")}
	states := make(chan State, 8)
	e, err := StartEngine(f, dir, os.Getenv("PATH"), func(line []byte) {
		if s, ok := ParseState(line); ok {
			states <- s
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop(time.Second) })
	return e, states, got
}

func waitState(t *testing.T, states chan State) State {
	t.Helper()
	select {
	case s := <-states:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no state from the engine")
		return State{}
	}
}

func waitDone(t *testing.T, e *Engine, within time.Duration) {
	t.Helper()
	select {
	case <-e.Done():
	case <-time.After(within):
		t.Fatalf("the engine did not exit within %v", within)
	}
}

func TestTheAppReadsTheEnginesStatesAndHandsItTheUsersChoices(t *testing.T) {
	e, states, got := start(t, false)
	if s := waitState(t, states); s.Version != "9.9.9" || len(s.Apps) != 1 || s.Sessions != 1 {
		t.Fatalf("state %+v", s)
	}
	e.Send("disconnect:https://a")
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(got)
		if strings.Contains(string(b), `{"t":"action","id":"disconnect:https://a"}`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine got %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStopClosesTheChannelAndTheEngineEndsByItself(t *testing.T) {
	e, states, _ := start(t, false)
	waitState(t, states)
	e.Stop(10 * time.Second)
	waitDone(t, e, time.Second)
	if e.ExitCode() != 0 {
		t.Fatalf("exit %d, want 0: it ended by itself", e.ExitCode())
	}
}

func TestStopEndsAnEngineThatIgnoresItsChannel(t *testing.T) {
	e, states, _ := start(t, true)
	waitState(t, states)
	e.Stop(300 * time.Millisecond)
	waitDone(t, e, time.Second)
	if e.ExitCode() != -1 {
		t.Fatalf("exit %d, want -1: ended by a signal", e.ExitCode())
	}
}

func TestAnExitOfTheEngineIsItsCode(t *testing.T) {
	e, states, _ := start(t, false)
	waitState(t, states)
	e.Send("exit75")
	waitDone(t, e, 10*time.Second)
	if e.ExitCode() != 75 {
		t.Fatalf("exit %d, want 75", e.ExitCode())
	}
}

func TestFindEngineFindsTheEngineAndTheNodeTheAppCarries(t *testing.T) {
	res := t.TempDir()
	env := func(string) string { return "" }
	put := func(parts ...string) string {
		p := filepath.Join(append([]string{res}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	platform := goruntime.GOOS + "-" + goruntime.GOARCH
	if _, err := FindEngine(res, "/Users/ann", env); err == nil {
		t.Fatal("an app without an engine has one")
	}
	// go run: the engine in the platform's own directory of the resources; no node, and the engine still runs
	platformEngine := put(platform, "engine", "agentd")
	f, err := FindEngine(res, "/Users/ann", env)
	if err != nil || f.Engine != platformEngine || f.Node != "" || f.NodeBin() != "" ||
		f.Home != "/Users/ann/.agentd" || f.Log != "/Users/ann/.agentd/logs/daemon.log" {
		t.Fatalf("got %+v, %v", f, err)
	}
	platformNode := put(platform, "node", "bin", "node")
	if f, _ := FindEngine(res, "/Users/ann", env); f.Node != platformNode || f.NodeBin() != filepath.Dir(platformNode) {
		t.Fatalf("node %q, want %q", f.Node, platformNode)
	}
	// a built app: the platform's resources are merged in
	mergedEngine, mergedNode := put("engine", "agentd"), put("node", "bin", "node")
	if f, _ := FindEngine(res, "/Users/ann", env); f.Engine != mergedEngine || f.Node != mergedNode {
		t.Fatalf("got %+v, want %q and %q", f, mergedEngine, mergedNode)
	}
}

func TestFindEngineTakesTheEngineAndNodeNamedByTheEnvironment(t *testing.T) {
	env := map[string]string{"AGENTD_APP_ENGINE": fakeEngine, "AGENTD_APP_NODE": "/n/bin/node", "AGENTD_HOME": "/h"}
	f, err := FindEngine(t.TempDir(), "/Users/ann", func(k string) string { return env[k] })
	if err != nil || f.Engine != fakeEngine || f.Node != "/n/bin/node" || f.Home != "/h" || f.Log != "/h/logs/daemon.log" {
		t.Fatalf("got %+v, %v", f, err)
	}
	env["AGENTD_APP_ENGINE"] = "/no/such/agentd"
	if _, err := FindEngine(t.TempDir(), "/Users/ann", func(k string) string { return env[k] }); err == nil {
		t.Fatal("an engine that is not there is found")
	}
}

// What the person reads when agentd does not start: one plain sentence, whatever went wrong. The error as it came goes
// to the log.
func TestWhyAgentdDidNotStartIsOnePlainSentence(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, noEngine := FindEngine(t.TempDir(), "/Users/ann", func(string) string { return "" })
	_, noLog := StartEngine(EngineFiles{Engine: fakeEngine, Log: filepath.Join(notADir, "logs", "daemon.log")}, t.TempDir(), os.Getenv("PATH"), func([]byte) {})
	_, noStart := StartEngine(EngineFiles{Engine: filepath.Join(t.TempDir(), "no-such-engine"), Log: filepath.Join(t.TempDir(), "daemon.log")}, t.TempDir(), os.Getenv("PATH"), func([]byte) {})

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"the app carries no engine", noEngine, "This copy of agentd is incomplete. Download agentd.app again."},
		{"the log cannot be written", noLog, "agentd cannot write its log in ~/.agentd. Check that folder, then choose Start."},
		{"the program does not start", noStart, "agentd could not start. Open the log for details."},
		{"anything else", fmt.Errorf("something nobody foresaw"), "agentd could not start. Open the log for details."},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s: the setup produced no error", c.name)
			continue
		}
		got := PlainReason(c.err)
		if got != c.want {
			t.Errorf("%s: got %q, want %q (raw error %q)", c.name, got, c.want, c.err)
		}
		for _, word := range []string{"engine", "daemon", "adapter", "origin", "fork/exec", "looked for", "no such file"} {
			if strings.Contains(strings.ToLower(got), word) {
				t.Errorf("%s: the sentence says %q", c.name, word)
			}
		}
	}
}

func TestTheRawErrorGoesToTheLogTheOpenLogEntryShows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "logs", "daemon.log")
	NoteInLog(file, "agentd.app could not start agentd: boom")
	NoteInLog(file, "second")
	NoteInLog("", "nowhere to write") // no log path known yet: nothing to do, and no panic
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " agentd.app could not start agentd: boom") || !strings.HasSuffix(lines[1], " second") {
		t.Fatalf("log %q", b)
	}
	if ts := strings.SplitN(lines[0], " ", 2)[0]; len(ts) != len("2006-01-02T15:04:05.000Z") || !strings.HasSuffix(ts, "Z") {
		t.Errorf("the timestamp %q is not the daemon's", ts)
	}
}
