//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
)

// fakeApp is the app's side of the link: it keeps the states the engine shows.
type fakeApp struct {
	states chan agentui.State
}

func (f *fakeApp) open(func(agentui.Action)) agentui.Link { return f }
func (f *fakeApp) Start()                                 {}
func (f *fakeApp) Stop()                                  {}
func (f *fakeApp) Update(s agentui.State) {
	select {
	case f.states <- s:
	default:
	}
}

// start runs the engine in this process as the app runs it, in a home of its own.
func start(t *testing.T) (*Engine, *fakeApp) {
	t.Helper()
	dir := t.TempDir()
	for k, v := range map[string]string{
		"HOME": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"AGENTD_LOCAL_PORT": "0", "AGENTD_ADAPTERS": "off", "AGENTD_AGENT_CLAUDE": "true", "AGENTD_AGENT_CODEX": "true",
	} {
		t.Setenv(k, v)
	}
	f := EngineFiles{Home: filepath.Join(dir, "agentd"), Log: filepath.Join(dir, "logs", "daemon.log")}
	app := &fakeApp{states: make(chan agentui.State, 64)}
	e, err := StartEngine(f, dir, os.Getenv("PATH"), app.open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop(5 * time.Second) })
	return e, app
}

func waitState(t *testing.T, app *fakeApp) agentui.State {
	t.Helper()
	select {
	case s := <-app.states:
		return s
	case <-time.After(20 * time.Second):
		t.Fatal("no state from the engine")
		return agentui.State{}
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

func TestTheAppReadsTheEnginesStates(t *testing.T) {
	_, app := start(t)
	s := waitState(t, app)
	if s.Version == "" {
		t.Fatalf("state %+v: want the version", s)
	}
}

func TestStopEndsTheEngineByItself(t *testing.T) {
	e, app := start(t)
	waitState(t, app)
	e.Stop(10 * time.Second)
	waitDone(t, e, time.Second)
	if e.ExitCode() != 0 {
		t.Fatalf("exit %d, want 0: it ended by itself", e.ExitCode())
	}
}

// A Restart of the window is a choice the engine answers by exiting 75, the code that has the app start it again.
func TestARestartTheUserChoosesExitsWithTheCodeThatStartsItAgain(t *testing.T) {
	e, app := start(t)
	waitState(t, app)
	e.Send("restart")
	waitDone(t, e, 10*time.Second)
	if e.ExitCode() != 75 {
		t.Fatalf("exit %d, want 75", e.ExitCode())
	}
}

func TestAChoiceThatIsNotTheEnginesDoesNothing(t *testing.T) {
	e, app := start(t)
	waitState(t, app)
	for _, id := range []string{"settings", "quit", "format-disk", ""} {
		e.Send(id)
	}
	select {
	case <-e.Done():
		t.Fatalf("the engine ended with %d", e.ExitCode())
	case <-time.After(300 * time.Millisecond):
	}
}

func TestFindFilesFindsTheNodeTheAppCarries(t *testing.T) {
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
	// no node: the app runs, and its adapters are not ready
	f := FindFiles(res, "/Users/ann", env)
	if f.Node != "" || f.NodeBin() != "" || f.Home != "/Users/ann/.agentd" || f.Log != "/Users/ann/.agentd/logs/daemon.log" {
		t.Fatalf("got %+v", f)
	}
	// go run: node in the platform's own directory of the resources
	platformNode := put(platform, "node", "bin", "node")
	if f := FindFiles(res, "/Users/ann", env); f.Node != platformNode || f.NodeBin() != filepath.Dir(platformNode) {
		t.Fatalf("node %q, want %q", f.Node, platformNode)
	}
	// a built app: the platform's resources are merged in
	mergedNode := put("node", "bin", "node")
	if f := FindFiles(res, "/Users/ann", env); f.Node != mergedNode {
		t.Fatalf("node %q, want %q", f.Node, mergedNode)
	}
}

func TestFindFilesTakesTheNodeAndHomeNamedByTheEnvironment(t *testing.T) {
	env := map[string]string{"AGENTD_APP_NODE": "/n/bin/node", "AGENTD_HOME": "/h"}
	f := FindFiles(t.TempDir(), "/Users/ann", func(k string) string { return env[k] })
	if f.Node != "/n/bin/node" || f.Home != "/h" || f.Log != "/h/logs/daemon.log" {
		t.Fatalf("got %+v", f)
	}
}

func TestTheEngineSeesThePathAndHomeTheAppGivesIt(t *testing.T) {
	host, err := engineHost("/Users/ann", "/App/node/bin:/usr/bin", "/Users/ann/.agentd")
	if err != nil {
		t.Fatal(err)
	}
	if host.Env["PATH"] != "/App/node/bin:/usr/bin" || host.Env["AGENTD_MANAGED"] != "1" || host.Env["AGENTD_HOME"] != "/Users/ann/.agentd" || host.Home != "/Users/ann" {
		t.Fatalf("host %+v", host)
	}
}

// What the person reads when agentd does not start: one plain sentence, whatever went wrong. The error as it came goes
// to the log.
func TestWhyAgentdDidNotStartIsOnePlainSentence(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, noLog := StartEngine(EngineFiles{Log: filepath.Join(notADir, "logs", "daemon.log")}, t.TempDir(), os.Getenv("PATH"), (&fakeApp{}).open)

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"the log cannot be written", noLog, "agentd cannot write its log in ~/.agentd. Check that folder, then choose Start."},
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
