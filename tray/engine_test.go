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

// fakeEngine speaks the engine's side of the channel on fd 3: it sends a state, writes each choice it gets to `got`,
// exits 75 when told "exit75", and exits 0 when the channel closes, unless `stubborn` (then only a signal ends it).
const fakeEngine = `
const net = require("node:net"), fs = require("node:fs")
const [got, stubborn] = process.argv.slice(2)
const s = new net.Socket({ fd: 3, readable: true, writable: true })
s.write(JSON.stringify({ t: "state", version: "9.9.9", apps: ["https://a"], pages: [], sessions: 1 }) + "\n")
let rest = ""
s.on("data", (d) => {
  const lines = (rest + d).split("\n"); rest = lines.pop()
  for (const l of lines) { fs.appendFileSync(got, l + "\n"); if (l.includes("exit75")) process.exit(75) }
})
s.on("close", () => { if (stubborn !== "stubborn") process.exit(0) })
setInterval(() => {}, 1000)
`

// start runs the fake engine as the app runs daemon.js (`node <script> run`): a wrapper hands the fake the file to
// write its choices to and whether to ignore its channel.
func start(t *testing.T, stubborn bool) (*Engine, chan State, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	fake, wrapper, got := filepath.Join(dir, "engine.js"), filepath.Join(dir, "daemon.js"), filepath.Join(dir, "got")
	mode := ""
	if stubborn {
		mode = "stubborn"
	}
	body := fmt.Sprintf("process.argv = [process.argv[0], process.argv[1], %q, %q]\nrequire(%q)\n", got, mode, fake)
	if err := os.WriteFile(fake, []byte(fakeEngine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := EngineFiles{Node: node, Script: wrapper, Home: dir, Log: filepath.Join(dir, "logs", "daemon.log")}
	states := make(chan State, 8)
	e, err := StartEngine(f, dir, os.Getenv("PATH"), func(s State) { states <- s })
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

func TestFindEngineFindsTheNodeTheAppCarries(t *testing.T) {
	res := t.TempDir()
	env := func(string) string { return "" }
	if _, err := FindEngine(res, "/Users/ann", env); err == nil {
		t.Fatal("an app without an engine has one")
	}
	if err := os.MkdirAll(filepath.Join(res, "engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res, "engine", "daemon.js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// go run: node in the platform's own directory of the resources
	platformNode := filepath.Join(res, goruntime.GOOS+"-"+goruntime.GOARCH, "node", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(platformNode), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(platformNode, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := FindEngine(res, "/Users/ann", env)
	if err != nil || f.Node != platformNode || f.Home != "/Users/ann/.agentd" || f.Log != "/Users/ann/.agentd/logs/daemon.log" {
		t.Fatalf("got %+v, %v", f, err)
	}
	// a built app: the platform's resources are merged in
	merged := filepath.Join(res, "node", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(merged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(merged, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if f, _ := FindEngine(res, "/Users/ann", env); f.Node != merged {
		t.Fatalf("node %q, want %q", f.Node, merged)
	}
}
