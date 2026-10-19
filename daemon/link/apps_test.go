package link

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// syncBuffer is a log both the hub's goroutines and the test use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// groups records the process groups of the agents like proc.SessionState does.
type groups struct{}

func (groups) Add(int)    {}
func (groups) Remove(int) {}

func noAdapter(agents.AdapterName) (agents.AdapterState, bool) { return agents.AdapterState{}, false }

// rig is an AppHub of a test: its fleets run `agent` as claude, its log is kept, and what it reports is recorded.
type rig struct {
	t    *testing.T
	hub  *AppHub
	log  *syncBuffer
	root string

	mu       sync.Mutex
	empties  []int
	restarts []string
	hellos   int
}

func newRig(t *testing.T, agent string) *rig {
	t.Helper()
	r := &rig{t: t, log: &syncBuffer{}, root: t.TempDir()}
	r.hub = NewAppHub(AppHubOptions{
		Fleet: agents.FleetOptions{
			Cwd:     t.TempDir(),
			Env:     home.Env{"PATH": os.Getenv("PATH"), agents.SeamVariable("claude"): agent},
			Adapter: noAdapter,
			State:   groups{},
		},
		WrapperRoot: r.root,
		ConfigFile:  "/etc/agentd/daemon.json",
		Executable:  "/usr/local/bin/agentd",
		Backoff:     fastBackoffOptions(),
		Hello: func() contract.Hello {
			r.mu.Lock()
			r.hellos++
			r.mu.Unlock()
			return helloOf(false)
		},
		OnEmpty:   func(code int) { r.mu.Lock(); r.empties = append(r.empties, code); r.mu.Unlock() },
		OnRestart: func(server string) { r.mu.Lock(); r.restarts = append(r.restarts, server); r.mu.Unlock() },
		Log:       proc.NewLogger(r.log, r.log),
	})
	t.Cleanup(func() { r.hub.Shutdown(time.Second) })
	return r
}

func (r *rig) add(server, token string) {
	r.t.Helper()
	if err := r.hub.Add(registry.App{Server: server, Token: token}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) emptied() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.empties...)
}

func (r *rig) restarted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.restarts...)
}

func (r *rig) logged(text string) bool { return strings.Contains(r.log.String(), text) }

func (r *rig) waitLog(text string) {
	r.t.Helper()
	waitFor(r.t, "the log to say "+text, func() bool { return r.logged(text) })
}

const echoAgent = "sh -c 'echo up; cat'"

// openedPid is the pid in an open answer.
func openedPid(t *testing.T, answer []json.RawMessage) int {
	t.Helper()
	var opened contract.Opened
	if len(answer) != 1 || json.Unmarshal(answer[0], &opened) != nil || opened.Pid <= 0 {
		t.Fatalf("open answered %s", answer)
	}
	return int(opened.Pid)
}

func groupAlive(pid int) bool { return syscall.Kill(-pid, 0) == nil }

func waitGroupGone(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, "the agent's process group to end", func() bool { return !groupAlive(pid) })
}

func frameData(e event) (sid, kind, data string, code int) {
	var c float64
	for i, p := range []any{&sid, &kind, &data, &c} {
		_ = json.Unmarshal(e.args[i], p)
	}
	return sid, kind, data, int(c)
}

// outLines are the stdout lines of session sid that the server received.
func outLines(c *fakeConn, sid string) []string {
	var lines []string
	for _, e := range c.sent("frame") {
		if id, kind, data, _ := frameData(e); id == sid && kind == "out" {
			lines = append(lines, data)
		}
	}
	return lines
}

func exitFrames(c *fakeConn, sid string) int {
	n := 0
	for _, e := range c.sent("frame") {
		if id, kind, _, _ := frameData(e); id == sid && kind == "exit" {
			n++
		}
	}
	return n
}

func TestAppHubSaysHelloOnEveryConnect(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	hello := c.waitEvents("hello", 1)[0]
	var info contract.Hello
	if err := json.Unmarshal(hello.args[0], &info); err != nil || info.Version != "1.2.3" || len(info.Agents) != 2 {
		t.Fatalf("hello = %s (%v)", hello.args[0], err)
	}
	r.waitLog("connected to " + s.srv.URL + " (protocol 1); agents: claude ready, codex not ready (Node is not installed)")

	c.kill()
	s.last(2).waitEvents("hello", 1)
	r.mu.Lock()
	hellos := r.hellos
	r.mu.Unlock()
	if hellos != 2 {
		t.Fatalf("the hello was built %d times, want once per connect", hellos)
	}
}

func TestAppHubWrittenWrapperPerApp(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(a.srv.URL, "ta")
	r.add(a.srv.URL, "ta") // the app has a link already
	r.add(b.srv.URL, "tb")
	if got := r.hub.Servers(); len(got) != 2 || got[0] != a.srv.URL || got[1] != b.srv.URL {
		t.Fatalf("servers = %q", got)
	}
	for n, server := range []string{a.srv.URL, b.srv.URL} {
		file := filepath.Join(r.root, string(rune('1'+n)), home.WrapperFileName(runtime.GOOS))
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable: %v", file, info.Mode())
		}
		text, _ := os.ReadFile(file)
		want := "'/usr/local/bin/agentd' 'mcp' '--config' '/etc/agentd/daemon.json' '--app' '" + server + "'"
		if runtime.GOOS != "windows" && !strings.Contains(string(text), want) {
			t.Fatalf("%s = %q, want it to run %s", file, text, want)
		}
	}
	if entries, _ := os.ReadDir(r.root); len(entries) != 2 {
		t.Fatalf("%d wrapper directories, want one per app", len(entries))
	}
}

func TestAppHubAgentPathStartsWithItsOwnWrapperDirectory(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	r := newRig(t, `sh -c 'echo "$PATH"; cat'`)
	r.add(a.srv.URL, "ta")
	r.add(b.srv.URL, "tb")
	ca, cb := a.last(1), b.last(1)
	ca.call("open", "s", "claude")
	cb.call("open", "s", "claude")
	waitFor(t, "both PATH lines", func() bool { return len(outLines(ca, "s")) == 1 && len(outLines(cb, "s")) == 1 })
	if got := outLines(ca, "s")[0]; !strings.HasPrefix(got, filepath.Join(r.root, "1")+string(os.PathListSeparator)) {
		t.Fatalf("PATH of app a = %q", got)
	}
	if got := outLines(cb, "s")[0]; !strings.HasPrefix(got, filepath.Join(r.root, "2")+string(os.PathListSeparator)) {
		t.Fatalf("PATH of app b = %q", got)
	}
}

func TestAppHubEverySessionIdMeansOneAgentPerApp(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(a.srv.URL, "ta")
	r.add(b.srv.URL, "tb")
	ca, cb := a.last(1), b.last(1)
	pa := openedPid(t, ca.call("open", "s1", "claude"))
	pb := openedPid(t, cb.call("open", "s1", "claude"))
	if pa == pb || !groupAlive(pa) || !groupAlive(pb) {
		t.Fatalf("pids %d and %d: want two agents, both alive", pa, pb)
	}
	if got := ca.call("write", "s1", `{"a":1}`); string(got[0]) != "true" {
		t.Fatalf("write answered %s", got)
	}
	waitFor(t, "the echo", func() bool { return len(outLines(ca, "s1")) == 2 })
	time.Sleep(100 * time.Millisecond)
	if got := outLines(ca, "s1"); got[1] != `{"a":1}` {
		t.Fatalf("a's agent said %q", got)
	}
	if got := outLines(cb, "s1"); len(got) != 1 || got[0] != "up" {
		t.Fatalf("b's agent said %q, want only its own start line", got)
	}
	if r.hub.Size() != 2 || r.hub.Linked() != 2 {
		t.Fatalf("size %d linked %d", r.hub.Size(), r.hub.Linked())
	}
	// a session of one app can be closed by that app only
	if got := string(ca.call("open", "s1", "claude")[0]); !strings.Contains(got, "session s1 exists") {
		t.Fatalf("a second open of s1 answered %s", got)
	}
	ca.emit("close", "s1")
	waitGroupGone(t, pa)
	if !groupAlive(pb) {
		t.Fatal("closing a's s1 ended b's")
	}
}

func TestAppHubOpenFailuresAreTheDaemonsOwnWords(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	got := string(c.call("open", "s1", "nope")[0])
	if got != `{"__rpcError":true,"code":"INTERNAL_ERROR","message":"unknown agent \"nope\": this daemon runs claude, codex, grok","method":"open"}` {
		t.Fatalf("open answered %s", got)
	}
}

func TestAppHubStopFromOneAppDetachesItAndTheOtherKeepsServing(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(a.srv.URL, "ta")
	r.add(b.srv.URL, "tb")
	ca, cb := a.last(1), b.last(1)
	pa := openedPid(t, ca.call("open", "s1", "claude"))
	ca.emit("stop")
	waitFor(t, "a to be disconnected", ca.gone)
	waitGroupGone(t, pa)
	r.waitLog("stop requested by the server " + a.srv.URL + ": disconnecting it")
	if r.hub.Has(a.srv.URL) || !r.hub.Has(b.srv.URL) || r.hub.Linked() != 1 {
		t.Fatalf("servers = %q", r.hub.Servers())
	}
	if got := r.emptied(); len(got) != 0 {
		t.Fatalf("OnEmpty ran %v while an app is left", got)
	}
	pb := openedPid(t, cb.call("open", "s2", "claude"))
	if !groupAlive(pb) {
		t.Fatal("b does not serve")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(a.handshakes()); n != 1 {
		t.Fatalf("a was dialled %d times: a stopped link must not reconnect", n)
	}

	cb.emit("stop") // the last app
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	if got := r.emptied(); got[0] != home.ExitStopped {
		t.Fatalf("OnEmpty(%d), want %d", got[0], home.ExitStopped)
	}
}

func TestAppHubStopInTheMiddleOfATurnEndsTheTreeAndTheExitFrameStillArrives(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, `sh -c 'sleep 600 & echo up; wait'`)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	pid := openedPid(t, c.call("open", "busy", "claude"))
	waitFor(t, "the agent's first line", func() bool { return len(outLines(c, "busy")) == 1 })
	c.emit("stop")
	waitGroupGone(t, pid)
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	if exitFrames(c, "busy") != 1 {
		t.Fatalf("the server received %d exit frames of the session, want 1: the link must outlive the agents", exitFrames(c, "busy"))
	}
}

func TestAppHubRestartIsLeftToTheCaller(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.emit("restart")
	waitFor(t, "OnRestart", func() bool { return len(r.restarted()) == 1 })
	if got := r.restarted(); got[0] != s.srv.URL {
		t.Fatalf("OnRestart(%q)", got[0])
	}
	if !r.hub.Has(s.srv.URL) {
		t.Fatal("restart detached the app: the daemon exits, the hub does not decide")
	}
}

func TestAppHubARefusedTokenLeavesTheOtherAppServing(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	a.set(func() { a.refuse = Refused })
	r := newRig(t, echoAgent)
	r.add(b.srv.URL, "tb") // b first: a is refused as soon as it is added, and must not be the last app
	r.add(a.srv.URL, "wrong")
	r.waitLog(a.srv.URL + " refused this daemon (unauthorized): the token is revoked or wrong, so this app stays stopped. To use it again: agentd pair --app " + a.srv.URL + " --code <code>, then agentd service start")
	waitFor(t, "a to be detached", func() bool { return !r.hub.Has(a.srv.URL) })
	b.last(1).waitEvents("hello", 1)
	time.Sleep(150 * time.Millisecond)
	if n := len(a.handshakes()); n != 1 {
		t.Fatalf("%d handshakes of the refused token, want 1", n)
	}
	if got := r.emptied(); len(got) != 0 {
		t.Fatalf("OnEmpty ran %v while b is left", got)
	}
	if !r.hub.Has(b.srv.URL) {
		t.Fatal("b was detached")
	}
}

func TestAppHubTheOnlyAppRefusedMeansExit0(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.refuse = Refused })
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "revoked")
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	if got := r.emptied(); got[0] != home.ExitStopped {
		t.Fatalf("OnEmpty(%d)", got[0])
	}
	if !r.logged("refused this daemon (unauthorized)") || !r.logged("stays stopped") {
		t.Fatalf("log = %q", r.log.String())
	}
}

func TestAppHubAnUnavailableServerIsRetriedAndTheAppStaysLinked(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.refuse = "unavailable" })
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	waitFor(t, "two handshakes", func() bool { return len(s.handshakes()) >= 2 })
	if !r.logged("cannot reach "+s.srv.URL+" (unavailable); retrying") || r.logged("stays stopped") {
		t.Fatalf("log = %q", r.log.String())
	}
	if !r.hub.Has(s.srv.URL) || len(r.emptied()) != 0 {
		t.Fatal("an unavailable server unlinked the app")
	}
}

func TestAppHubAServerThatCannotBeReachedIsRetried(t *testing.T) {
	r := newRig(t, echoAgent)
	server := deadAddress(t)
	r.add(server, "tok")
	r.waitLog("cannot reach " + server)
	if !r.logged("); retrying") || !r.hub.Has(server) {
		t.Fatalf("log = %q", r.log.String())
	}
}

func TestAppHubSocketDropEndsEveryAgentAndSendsNothingStaleAfterTheReconnect(t *testing.T) {
	for name, drop := range map[string]func(*fakeConn){
		"the server ends the socket": func(c *fakeConn) { c.disconnect() },
		"the connection breaks":      func(c *fakeConn) { c.kill() },
	} {
		t.Run(name, func(t *testing.T) {
			s := newFakeServer(t)
			r := newRig(t, `sh -c 'sleep 300 & sleep 300 & echo up; wait'`)
			r.add(s.srv.URL, "tok")
			first := s.last(1)
			pid := openedPid(t, first.call("open", "s1", "claude"))
			waitFor(t, "the agent's first line", func() bool { return len(outLines(first, "s1")) == 1 })

			drop(first)
			waitGroupGone(t, pid)
			second := s.last(2)
			second.waitEvents("hello", 1)
			waitFor(t, "the link to be connected again", func() bool { return r.hub.snapshot()[0].link.Connected() })
			time.Sleep(300 * time.Millisecond)
			if n := exitFrames(second, "s1") + len(second.sent("frame")); n != 0 {
				t.Fatalf("the new connection received %d frames of the old session: the exit frame was stale", n)
			}
			if !r.logged("disconnected from "+s.srv.URL+" (") || !r.logged("ending 1 agent(s), reconnecting") {
				t.Fatalf("log = %q", r.log.String())
			}
			// the daemon serves new sessions after the reconnect
			if again := openedPid(t, second.call("open", "s2", "claude")); !groupAlive(again) {
				t.Fatal("the new session's agent is not running")
			}
			if len(r.emptied()) != 0 {
				t.Fatal("a drop unlinked the app")
			}
		})
	}
}

func TestAppHubAnAppOfAnotherProtocolIsLeft(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.protocol = 2 })
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	r.waitLog(s.srv.URL + " speaks protocol 2, this daemon speaks 1: disconnecting it. Update the daemon or the app.")
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	if r.hub.Has(s.srv.URL) {
		t.Fatal("the app is still linked")
	}
}

func TestAppHubAHelloTheServerRefusesIsLoggedAndTheLinkStays(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.noHello = true })
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.waitEvents("hello", 1)
	c.write(`43/agentd,0[{"__rpcError":true,"code":"INTERNAL_ERROR","message":"not now","method":"hello"}]`)
	r.waitLog(s.srv.URL + " did not accept hello: not now")
	if !r.hub.Has(s.srv.URL) || !r.hub.snapshot()[0].link.Connected() {
		t.Fatal("the link did not stay")
	}
}

func TestAppHubTellsEveryConnectedAppWhichAgentsAreReady(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	dead := deadAddress(t)
	r := newRig(t, echoAgent)
	r.add(a.srv.URL, "ta")
	r.add(b.srv.URL, "tb")
	r.add(dead, "td") // not connected: it is skipped
	ca, cb := a.last(1), b.last(1)
	ca.waitEvents("hello", 1)
	cb.waitEvents("hello", 1)
	list := []contract.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Reason: "installing"}}
	r.hub.AgentsChanged(list)
	for _, c := range []*fakeConn{ca, cb} {
		e := c.waitEvents("agents", 1)[0]
		want := `42/agentd,["agents",[{"name":"claude","title":"Claude Code","ready":true,"reason":""},{"name":"codex","title":"Codex","ready":false,"reason":"installing"}]]`
		if e.raw != want {
			t.Fatalf("agents went out as\n%s\nwant\n%s", e.raw, want)
		}
	}
}

func TestAppHubMcpOutIsRefused(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.emit("mcpOut", "{}")
	failure := c.waitEvents("__rpc:error__", 1)[0]
	if !strings.Contains(string(failure.args[0]), `"message":"this socket is not an MCP bridge"`) {
		t.Fatalf("out-of-band error = %s", failure.args[0])
	}
}

func TestAppHubDetachQuietlyAndShutdown(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	r := newRig(t, `sh -c 'sleep 300 & echo up; wait'`)
	r.add(a.srv.URL, "ta")
	r.add(b.srv.URL, "tb")
	ca, cb := a.last(1), b.last(1)
	pa := openedPid(t, ca.call("open", "s1", "claude"))
	pb := openedPid(t, cb.call("open", "s1", "claude"))

	r.hub.Detach(a.srv.URL, true)
	waitGroupGone(t, pa)
	waitFor(t, "a's connection to close", ca.gone)
	r.hub.Detach("https://nobody.example", false) // an app without a link: nothing
	if len(r.emptied()) != 0 {
		t.Fatalf("OnEmpty ran %v", r.emptied())
	}

	r.hub.Shutdown(2 * time.Second)
	waitGroupGone(t, pb)
	if !cb.gone() || r.hub.Linked() != 0 || r.hub.Size() != 0 {
		t.Fatalf("after Shutdown: b gone %v, linked %d, size %d", cb.gone(), r.hub.Linked(), r.hub.Size())
	}
	if exitFrames(cb, "s1") != 1 {
		t.Fatalf("b received %d exit frames before its link closed, want 1", exitFrames(cb, "s1"))
	}
	if len(r.emptied()) != 0 {
		t.Fatalf("Shutdown ran OnEmpty: %v", r.emptied())
	}
}

func TestAppHubWithTheRealHub(t *testing.T) {
	e := newHubEnv(t, false)
	rec := e.pair(t, "tok-real")
	r := newRig(t, echoAgent)
	r.add(e.srv.URL, "tok-real")
	e.waitState(t, rec.ID, "online")
	r.waitLog("connected to " + e.srv.URL + " (protocol 1)")

	c := e.open(t, rec.ID)
	if got := readLine(t, c); got != "up" {
		t.Fatalf("first line = %q", got)
	}
	if _, err := c.Write([]byte("hello agent\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLine(t, c); got != "hello agent" {
		t.Fatalf("echo = %q", got)
	}
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(waitLimit):
		t.Fatal("closing the session did not end it")
	}

	list := []contract.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: false, Reason: "installing"}}
	r.hub.AgentsChanged(list)
	waitFor(t, "the hub to learn the agents", func() bool {
		l, _ := e.hub.List(user)
		return len(l.Daemons) == 1 && len(l.Daemons[0].Agents) == 1 && l.Daemons[0].Agents[0].Reason == "installing"
	})

	if _, err := e.hub.Stop(user, rec.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	e.waitState(t, rec.ID, "offline")
}

func TestAppHubARevokedTokenWithTheRealHub(t *testing.T) {
	e := newHubEnv(t, false)
	rec := e.pair(t, "tok-revoked")
	r := newRig(t, echoAgent)
	r.add(e.srv.URL, "tok-revoked")
	e.waitState(t, rec.ID, "online")
	if _, err := e.hub.Revoke(user, rec.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnEmpty", func() bool { return len(r.emptied()) == 1 })
	if !r.logged("refused this daemon (unauthorized)") {
		t.Fatalf("log = %q", r.log.String())
	}
	n := e.store.handshakesOf("tok-revoked")
	time.Sleep(200 * time.Millisecond)
	if e.store.handshakesOf("tok-revoked") != n {
		t.Fatal("the refused token was retried")
	}
}
