package link

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// recorder is a ClientHandler that keeps what the server asked and answers by its hooks.
type recorder struct {
	mu       sync.Mutex
	opens    []string // "<sid>:<agent>"
	writes   map[string][]string
	closes   []string
	restarts int
	stops    int
	mcpOuts  []string

	open    func(sid, agent string) (contract.Opened, error)
	write   func(sid, line string) (bool, error)
	restart func() error
}

func (r *recorder) HandleOpen(_ context.Context, sid, agent string) (contract.Opened, error) {
	r.mu.Lock()
	r.opens = append(r.opens, sid+":"+agent)
	fn := r.open
	r.mu.Unlock()
	if fn != nil {
		return fn(sid, agent)
	}
	return contract.Opened{Pid: 42, Cwd: "/work"}, nil
}

func (r *recorder) HandleWrite(_ context.Context, sid, line string) (bool, error) {
	r.mu.Lock()
	if r.writes == nil {
		r.writes = map[string][]string{}
	}
	r.writes[sid] = append(r.writes[sid], line)
	fn := r.write
	r.mu.Unlock()
	if fn != nil {
		return fn(sid, line)
	}
	return true, nil
}

func (r *recorder) HandleClose(_ context.Context, sid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes = append(r.closes, sid)
	return nil
}

func (r *recorder) HandleRestart(context.Context) error {
	r.mu.Lock()
	r.restarts++
	fn := r.restart
	r.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (r *recorder) HandleStop(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
	return nil
}

func (r *recorder) HandleMcpOut(_ context.Context, line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mcpOuts = append(r.mcpOuts, line)
	return errors.New("this socket is not an MCP bridge")
}

func (r *recorder) count(f func(*recorder) int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return f(r)
}

// session is the id and the agent of the n-th open (from 0).
func (r *recorder) session(n int) (id, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, agent, _ = strings.Cut(r.opens[n], ":")
	return id, agent
}

func (r *recorder) writesTo(sid string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes[sid]...)
}

// startLink starts a Link to server that is closed with the test.
func startLink(t *testing.T, server string, h ClientHandler, ev Events, tweak ...func(*Options)) *Link {
	t.Helper()
	o := Options{
		Server:  server,
		Auth:    map[string]string{"role": "daemon", "token": "tok"},
		Backoff: fastBackoffOptions(),
	}
	for _, f := range tweak {
		f(&o)
	}
	l, err := New(o, h, ev)
	if err != nil {
		t.Fatal(err)
	}
	l.Start()
	t.Cleanup(func() { l.Close(time.Second) })
	return l
}

func TestLinkSendsTheAuthOnceAndHelloOnConnect(t *testing.T) {
	s := newFakeServer(t)
	ev := &events{}
	l := startLink(t, s.srv.URL, &recorder{}, ev.set())
	c := s.last(1)
	waitFor(t, "connected", l.Connected)
	if hs := s.handshakes(); len(hs) != 1 || hs[0]["role"] != "daemon" || hs[0]["token"] != "tok" || len(hs[0]) != 2 {
		t.Fatalf("handshakes = %v, want one {role:daemon, token}", hs)
	}
	hello := contract.Hello{Version: "1.2.3", Hostname: "box", Os: "linux", Arch: "amd64", Managed: true, Agents: []contract.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}}}
	reply, err := l.Hello(context.Background(), hello)
	if err != nil || reply.Protocol != 1 {
		t.Fatalf("hello = %+v, %v", reply, err)
	}
	e := c.waitEvents("hello", 1)[0]
	want := `42/agentd,0["hello",{"version":"1.2.3","hostname":"box","os":"linux","arch":"amd64","agents":[{"name":"claude","title":"Claude Code","ready":true,"reason":""}],"managed":true}]`
	if e.raw != want {
		t.Fatalf("hello went out as\n%s\nwant\n%s", e.raw, want)
	}
	if ev.connected() != 1 {
		t.Fatalf("OnConnect ran %d times", ev.connected())
	}
}

func TestHelloFailures(t *testing.T) {
	t.Run("the server's own error", func(t *testing.T) {
		s := newFakeServer(t)
		l := startLink(t, s.srv.URL, &recorder{}, Events{})
		c := s.last(1)
		go func() {
			c.waitEvents("hello", 1)
			c.write(`43/agentd,0[{"__rpcError":true,"code":"INTERNAL_ERROR","message":"not now","method":"hello"}]`)
		}()
		s.set(func() { s.noHello = true })
		_, err := l.Hello(context.Background(), contract.Hello{})
		var re *contract.RpcError
		if !errors.As(err, &re) || re.Code != contract.CodeInternalError || re.Message != "not now" {
			t.Fatalf("err = %v, want the server's RpcError", err)
		}
	})
	t.Run("no answer in time", func(t *testing.T) {
		s := newFakeServer(t)
		s.set(func() { s.noHello = true })
		l := startLink(t, s.srv.URL, &recorder{}, Events{})
		s.last(1)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := l.Hello(ctx, contract.Hello{})
		var re *contract.RpcError
		if !errors.As(err, &re) || re.Code != contract.CodeTimeout || re.Message != "operation has timed out" {
			t.Fatalf("err = %v, want TIMEOUT", err)
		}
	})
	t.Run("the socket ends while it waits", func(t *testing.T) {
		s := newFakeServer(t)
		s.set(func() { s.noHello = true })
		l := startLink(t, s.srv.URL, &recorder{}, Events{})
		c := s.last(1)
		go func() {
			c.waitEvents("hello", 1)
			c.kill()
		}()
		_, err := l.Hello(context.Background(), contract.Hello{})
		var re *contract.RpcError
		if !errors.As(err, &re) || re.Code != contract.CodeDisconnected {
			t.Fatalf("err = %v, want DISCONNECTED", err)
		}
	})
	t.Run("not connected", func(t *testing.T) {
		l, err := New(Options{Server: deadAddress(t)}, &recorder{}, Events{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = l.Hello(context.Background(), contract.Hello{})
		var re *contract.RpcError
		if !errors.As(err, &re) || re.Code != contract.CodeDisconnected {
			t.Fatalf("err = %v, want DISCONNECTED", err)
		}
	})
}

func TestServerCallsAreAnsweredThroughTheEnvelope(t *testing.T) {
	s := newFakeServer(t)
	rec := &recorder{}
	startLink(t, s.srv.URL, rec, Events{})
	c := s.last(1)

	if got := c.call("open", "s1", "claude"); len(got) != 1 || string(got[0]) != `{"pid":42,"cwd":"/work"}` {
		t.Fatalf("open answered %s", got)
	}
	if got := c.call("write", "s1", `{"a":"<b>"}`); len(got) != 1 || string(got[0]) != "true" {
		t.Fatalf("write answered %s", got)
	}
	if lines := rec.writesTo("s1"); len(lines) != 1 || lines[0] != `{"a":"<b>"}` {
		t.Fatalf("write handler saw %q", lines)
	}

	rec.mu.Lock()
	rec.write = func(string, string) (bool, error) { return false, nil }
	rec.mu.Unlock()
	if got := c.call("write", "gone", "x"); string(got[0]) != "false" {
		t.Fatalf("a write to a session that is gone answered %s", got)
	}

	rec.mu.Lock()
	rec.open = func(string, string) (contract.Opened, error) {
		return contract.Opened{}, errors.New("unknown agent \"x\"")
	}
	rec.mu.Unlock()
	if got := c.call("open", "s2", "x"); string(got[0]) != `{"__rpcError":true,"code":"INTERNAL_ERROR","message":"unknown agent \"x\"","method":"open"}` {
		t.Fatalf("a failed open answered %s", got)
	}

	rec.mu.Lock()
	rec.open = func(string, string) (contract.Opened, error) {
		return contract.Opened{}, contract.NewRpcError(contract.CodeAborted, "too late", "", nil)
	}
	rec.mu.Unlock()
	if got := c.call("open", "s3", "x"); string(got[0]) != `{"__rpcError":true,"code":"ABORTED","message":"too late","method":"open"}` {
		t.Fatalf("an RpcError from a handler answered %s", got)
	}

	rec.mu.Lock()
	rec.open = func(string, string) (contract.Opened, error) { panic("handler bug") }
	rec.mu.Unlock()
	if got := c.call("open", "s4", "x"); string(got[0]) != `{"__rpcError":true,"code":"INTERNAL_ERROR","message":"handler bug","method":"open"}` {
		t.Fatalf("a panic answered %s", got)
	}

	if got := c.call("open", "only-one-argument"); !strings.Contains(string(got[0]), `"code":"INVALID_ARGUMENT"`) {
		t.Fatalf("a call with a missing argument answered %s", got)
	}
}

func TestFireAndForgetCallsReachTheHandlerAndFailuresGoOutOfBand(t *testing.T) {
	s := newFakeServer(t)
	rec := &recorder{}
	startLink(t, s.srv.URL, rec, Events{})
	c := s.last(1)

	c.emit("close", "s1")
	c.emit("restart")
	c.emit("stop")
	waitFor(t, "close, restart and stop", func() bool {
		return rec.count(func(r *recorder) int { return len(r.closes) + r.restarts + r.stops }) == 3
	})
	if len(c.sent("__rpc:error__")) != 0 {
		t.Fatal("a handler that succeeded reported an error")
	}

	c.emit("mcpOut", "line")
	failure := c.waitEvents("__rpc:error__", 1)[0]
	var re contract.RpcError
	if err := json.Unmarshal(failure.args[0], &re); err != nil || !re.RPCError || re.Code != contract.CodeInternalError || re.Message != "this socket is not an MCP bridge" || re.Method != "mcpOut" {
		t.Fatalf("out-of-band error = %s (%v)", failure.args[0], err)
	}

	rec.mu.Lock()
	rec.restart = func() error { panic("restart bug") }
	rec.mu.Unlock()
	c.emit("restart")
	failure = c.waitEvents("__rpc:error__", 2)[1]
	if !strings.Contains(string(failure.args[0]), `"message":"restart bug"`) || !strings.Contains(string(failure.args[0]), `"method":"restart"`) {
		t.Fatalf("out-of-band error of a panic = %s", failure.args[0])
	}
}

func TestCallsAboutOneSessionKeepTheirOrderAndOthersDoNotWaitForIt(t *testing.T) {
	s := newFakeServer(t)
	rec := &recorder{}
	release := make(chan struct{})
	rec.write = func(sid, line string) (bool, error) {
		if sid == "slow" && line == "first" {
			<-release
		}
		return true, nil
	}
	startLink(t, s.srv.URL, rec, Events{})
	c := s.last(1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.call("write", "slow", "first")
	}()
	waitFor(t, "the slow write to start", func() bool { return len(rec.writesTo("slow")) == 1 })
	// the next write of the slow session queues behind it
	second := make(chan struct{})
	go func() {
		defer close(second)
		c.call("write", "slow", "second")
	}()
	// another session is answered meanwhile
	c.call("write", "fast", "x")
	if got := rec.writesTo("slow"); len(got) != 1 {
		t.Fatalf("the second write of a session ran before the first ended: %q", got)
	}
	close(release)
	<-done
	<-second
	if got := rec.writesTo("slow"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("writes = %q, want first then second", got)
	}
}

func TestSinkSendsFramesWhileItsConnectionIsUp(t *testing.T) {
	s := newFakeServer(t)
	l := startLink(t, s.srv.URL, &recorder{}, Events{})
	c := s.last(1)
	sink := l.Sink()
	sink.Send("s1", agents.FrameOut, `a<b>&"c"`+string(rune(0x2028)), 0)
	sink.Send("s1", agents.FrameExit, "", 3)
	frames := c.waitEvents("frame", 2)
	if want := `42/agentd,["frame","s1","out","a<b>&\"c\"` + "\\" + `u2028",0]`; frames[0].raw != want {
		t.Fatalf("frame went out as\n%s\nwant\n%s", frames[0].raw, want)
	}
	if want := `42/agentd,["frame","s1","exit","",3]`; frames[1].raw != want {
		t.Fatalf("exit frame went out as\n%s\nwant\n%s", frames[1].raw, want)
	}
}

func TestAStaleSinkSendsNothingAfterTheConnectionDropped(t *testing.T) {
	for name, drop := range map[string]func(*fakeConn){
		"the server ends the namespace": func(c *fakeConn) { c.disconnect() },
		"the connection breaks":         func(c *fakeConn) { c.kill() },
	} {
		t.Run(name, func(t *testing.T) {
			s := newFakeServer(t)
			ev := &events{}
			l := startLink(t, s.srv.URL, &recorder{}, ev.set())
			first := s.last(1)
			stale := l.Sink()
			stale.Send("s1", agents.FrameOut, "before", 0)
			first.waitEvents("frame", 1)

			drop(first)
			second := s.last(2)
			waitFor(t, "the link to be connected again", func() bool { return ev.connected() == 2 && l.Connected() })

			stale.Send("s1", agents.FrameExit, "", 0)
			l.Sink().Send("s1", agents.FrameOut, "after", 0)
			second.waitEvents("frame", 1)
			time.Sleep(50 * time.Millisecond)
			frames := second.sent("frame")
			if len(frames) != 1 || !strings.Contains(frames[0].raw, `"after"`) {
				t.Fatalf("the new connection received %d frames, first %q; want only the fresh one", len(frames), frames[0].raw)
			}
		})
	}
}

func TestReconnectsAfterADropAndSaysWhyItDropped(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		drop   func(*fakeServer, *fakeConn)
	}{
		{"the server ends the namespace", "io server disconnect", func(_ *fakeServer, c *fakeConn) { c.disconnect() }},
		{"the connection breaks", "transport close", func(_ *fakeServer, c *fakeConn) { c.kill() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeServer(t)
			ev := &events{}
			startLink(t, s.srv.URL, &recorder{}, ev.set())
			first := s.last(1)
			tc.drop(s, first)
			second := s.last(2)
			waitFor(t, "OnConnect twice", func() bool { return ev.connected() == 2 })
			if got := ev.disconnected(); len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("disconnect reasons = %q, want [%q]", got, tc.reason)
			}
			if n := len(s.handshakes()); n != 2 {
				t.Fatalf("%d handshakes, want exactly 2 (one per connection)", n)
			}
			// the new connection serves
			if got := second.call("open", "s1", "claude"); string(got[0]) != `{"pid":42,"cwd":"/work"}` {
				t.Fatalf("open after the reconnect answered %s", got)
			}
		})
	}
}

func TestASilentServerIsAPingTimeout(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.silent = true })
	ev := &events{}
	startLink(t, s.srv.URL, &recorder{}, ev.set())
	s.last(1)
	waitFor(t, "the ping timeout", func() bool { return len(ev.disconnected()) == 1 })
	if got := ev.disconnected()[0]; got != "ping timeout" {
		t.Fatalf("reason = %q, want ping timeout", got)
	}
	waitFor(t, "a new connection", func() bool { return len(s.handshakes()) >= 2 })
}

func TestARefusedTokenIsNeverRetried(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.refuse = Refused })
	ev := &events{}
	l := startLink(t, s.srv.URL, &recorder{}, ev.set())
	waitFor(t, "the rejection", func() bool { return len(ev.rejected()) == 1 })
	time.Sleep(200 * time.Millisecond) // many backoffs
	if n := len(s.handshakes()); n != 1 {
		t.Fatalf("%d handshakes, want 1: a refused token must not be retried", n)
	}
	if got := ev.rejected(); got[0] != "unauthorized" || len(ev.trouble()) != 0 {
		t.Fatalf("rejected %q, trouble %q", got, ev.trouble())
	}
	start := time.Now()
	l.Close(time.Second)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("closing a link that stopped took long")
	}
}

func TestAServerThatCannotCheckTheTokenIsRetried(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.refuse = "unavailable" })
	ev := &events{}
	startLink(t, s.srv.URL, &recorder{}, ev.set())
	waitFor(t, "three handshakes", func() bool { return len(s.handshakes()) >= 3 })
	if len(ev.rejected()) != 0 {
		t.Fatalf("rejected = %q: unavailable is not a refusal of the token", ev.rejected())
	}
	if got := ev.trouble(); got[0] != "unavailable" {
		t.Fatalf("trouble = %q", got)
	}
	// and it connects once the server can take the handshake
	s.set(func() { s.refuse = "" })
	waitFor(t, "connected", func() bool { return ev.connected() == 1 })
}

func TestAServerThatCannotBeReachedIsRetriedAndGivenUpOnAfterMaxRetries(t *testing.T) {
	ev := &events{}
	startLink(t, deadAddress(t), &recorder{}, ev.set(), func(o *Options) { o.MaxRetries = 2 })
	waitFor(t, "giving up", func() bool { return ev.gaveUp() == 1 })
	if got := ev.trouble(); len(got) != 3 || !strings.Contains(got[0], "connect") {
		t.Fatalf("trouble = %q, want the first attempt and 2 retries, each with the dial error", got)
	}
	time.Sleep(100 * time.Millisecond)
	if ev.gaveUp() != 1 || len(ev.trouble()) != 3 {
		t.Fatal("the link went on after giving up")
	}
}

func TestACappedConnectionAttemptEndsInTrouble(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.refuse = "unavailable" })
	ev := &events{}
	startLink(t, s.srv.URL, &recorder{}, ev.set(), func(o *Options) { o.ConnectTimeout = time.Second })
	waitFor(t, "trouble", func() bool { return len(ev.trouble()) > 0 })
}

func TestCloseSaysGoodbyeAndStopsTheRetries(t *testing.T) {
	s := newFakeServer(t)
	ev := &events{}
	l := startLink(t, s.srv.URL, &recorder{}, ev.set())
	c := s.last(1)
	start := time.Now()
	l.Close(time.Second)
	if time.Since(start) > 900*time.Millisecond {
		t.Fatalf("Close took %s, the server closes at once", time.Since(start))
	}
	waitFor(t, "the server to see the connection end", c.gone)
	if got := ev.disconnected(); len(got) != 1 || got[0] != "io client disconnect" {
		t.Fatalf("disconnect reasons = %q", got)
	}
	if l.Connected() {
		t.Fatal("still connected after Close")
	}
	l.Sink().Send("s1", agents.FrameOut, "late", 0)
	time.Sleep(100 * time.Millisecond)
	if n := len(s.handshakes()); n != 1 {
		t.Fatalf("%d handshakes: a closed link reconnected", n)
	}
	l.Close(time.Second) // closing twice is fine
}

func TestCloseGivesUpWaitingAfterGrace(t *testing.T) {
	s := newFakeServer(t)
	ev := &events{}
	l := startLink(t, s.srv.URL, &recorder{}, ev.set())
	s.last(1)
	l.Close(0) // no grace at all: the connection is cut
	waitFor(t, "OnDisconnect", func() bool { return len(ev.disconnected()) == 1 })
	if got := ev.disconnected()[0]; got != "io client disconnect" {
		t.Fatalf("reason = %q", got)
	}
}

func TestCloseOfALinkThatWasNeverStartedReturns(t *testing.T) {
	l, err := New(Options{Server: "http://127.0.0.1:1"}, &recorder{}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	l.Close(time.Second)
	l.Start() // after Close it does nothing
	l.Close(time.Second)
}

func TestCloseWhileTheServerIsUnreachable(t *testing.T) {
	ev := &events{}
	l := startLink(t, deadAddress(t), &recorder{}, ev.set(), func(o *Options) {
		o.Backoff = home.BackoffOptions{Base: time.Hour, Max: time.Hour}
	})
	waitFor(t, "the first failure", func() bool { return len(ev.trouble()) == 1 })
	start := time.Now()
	l.Close(time.Second)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("Close waited for the backoff")
	}
}

func TestNewRefusesAServerThatIsNotAnOrigin(t *testing.T) {
	for _, server := range []string{"", "localhost:3000", "ftp://x", "http://"} {
		if _, err := New(Options{Server: server}, &recorder{}, Events{}); err == nil {
			t.Errorf("New accepted %q", server)
		}
	}
}

func TestAnEventForAnotherNamespaceOrAnUnknownEventIsIgnored(t *testing.T) {
	s := newFakeServer(t)
	rec := &recorder{}
	l := startLink(t, s.srv.URL, rec, Events{})
	c := s.last(1)
	c.write(`42/other,["open","s1","claude"]`)
	c.emit("nonsense", 1)
	if got := c.call("write", "s1", "x"); string(got[0]) != "true" {
		t.Fatalf("write answered %s", got)
	}
	if n := rec.count(func(r *recorder) int { return len(r.opens) }); n != 0 {
		t.Fatalf("an event of another namespace opened %d sessions", n)
	}
	if !l.Connected() {
		t.Fatal("the link dropped on an unknown event")
	}
}

func TestACallThatArrivesAfterItsConnectionEndedIsDropped(t *testing.T) {
	s := newFakeServer(t)
	rec := &recorder{}
	gate := make(chan struct{})
	rec.open = func(sid, _ string) (contract.Opened, error) {
		if sid == "first" {
			<-gate // holds the lane of this session
		}
		return contract.Opened{Pid: 1}, nil
	}
	ev := &events{}
	startLink(t, s.srv.URL, rec, ev.set())
	c := s.last(1)
	// the second open of "first" queues behind the first, which waits; the connection drops meanwhile
	c.write(`42/agentd,0["open","first","claude"]`)
	waitFor(t, "the first open to start", func() bool { return rec.count(func(r *recorder) int { return len(r.opens) }) == 1 })
	c.write(`42/agentd,1["open","first","claude"]`)
	c.kill()
	waitFor(t, "the drop", func() bool { return len(ev.disconnected()) == 1 })
	close(gate)
	time.Sleep(100 * time.Millisecond)
	if n := rec.count(func(r *recorder) int { return len(r.opens) }); n != 1 {
		t.Fatalf("handler ran %d opens, want 1: the queued one belonged to a connection that ended", n)
	}
}
