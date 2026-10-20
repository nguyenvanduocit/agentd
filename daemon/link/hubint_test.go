package link

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// The spike of the client decision (the numbers are in sio.go): 200 clients, 20 at a time, each on the websocket
// transport with its own token against the real hub, which counts the handshakes per token. The library client
// sent a duplicate CONNECT for about 1 in 120 of them and none for about 1 in 600; this client never does.
func TestOwnClientConnectsOnceEvery200Times(t *testing.T) {
	e := newHubEnv(t, true)
	const clients, concurrency = 200, 20
	tokens := make([]string, clients)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("spike-%d", i)
	}
	var wg sync.WaitGroup
	var failures atomic.Int32
	sem := make(chan struct{}, concurrency)
	for _, token := range tokens {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := dialConn(ctx, e.srv.URL, "/agentd", map[string]string{"role": "daemon", "token": token})
			if err != nil {
				failures.Add(1)
				t.Errorf("connect: %v", err)
				return
			}
			time.Sleep(30 * time.Millisecond) // a duplicate CONNECT would arrive about now
			if c.ctx.Err() != nil {
				failures.Add(1)
				t.Errorf("the connection of %s was closed by the server", token)
			}
			c.finish(reasonClient)
		}()
	}
	wg.Wait()
	for _, token := range tokens {
		if n := e.store.handshakesOf(token); n != 1 {
			t.Errorf("token %s: %d handshakes, want 1", token, n)
		}
	}
	if failures.Load() != 0 {
		t.Fatalf("%d of %d clients did not stay connected", failures.Load(), clients)
	}
}

func TestMaxMessageBytesIsTheHubs(t *testing.T) {
	if maxMessageBytes != goserver.MaxMessageBytes {
		t.Fatalf("maxMessageBytes = %d, the hub accepts %d", maxMessageBytes, goserver.MaxMessageBytes)
	}
}

// helloOf is what a daemon says about itself in these tests.
func helloOf(managed bool) contract.Hello {
	return contract.Hello{
		Version: "1.2.3", Hostname: "box", Os: "linux", Arch: "amd64", Managed: managed,
		Agents: []contract.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Reason: "Node is not installed"}},
	}
}

// pairedLink is a Link of a daemon the hub knows, which says hello on every connect.
func pairedLink(t *testing.T, e *hubEnv, rec *recorder, managed bool, server string) (*Link, goserver.Record, *events) {
	t.Helper()
	token := "token-" + t.Name()
	r := e.pair(t, token)
	ev := &events{}
	set := ev.set()
	var l *Link
	connected := set.OnConnect
	set.OnConnect = func() {
		connected()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
			defer cancel()
			if reply, err := l.Hello(ctx, helloOf(managed)); err != nil || reply.Protocol != goserver.Protocol {
				t.Errorf("hello = %+v, %v", reply, err)
			}
		}()
	}
	if server == "" {
		server = e.srv.URL
	}
	var err error
	l, err = New(Options{Server: server, Auth: map[string]string{"role": "daemon", "token": token}, Backoff: fastBackoffOptions()}, rec, set)
	if err != nil {
		t.Fatal(err)
	}
	l.Start()
	t.Cleanup(func() { l.Close(time.Second) })
	return l, r, ev
}

func TestHubSeesWhatTheDaemonSaysAndServesSessionsThroughIt(t *testing.T) {
	e := newHubEnv(t, false)
	rec := &recorder{}
	l, r, _ := pairedLink(t, e, rec, true, "")
	info := e.waitState(t, r.ID, "online")
	if info.Version != "1.2.3" || info.Hostname != "box" || info.OS != "linux" || !info.Managed || len(info.Agents) != 2 || !info.Agents[0].Ready || info.Agents[1].Ready || info.Agents[1].Reason != "Node is not installed" {
		t.Fatalf("the hub learned %+v from hello", info)
	}

	c := e.open(t, r.ID)
	if c.Cwd() != "/work" {
		t.Fatalf("cwd = %q", c.Cwd())
	}
	sid, agent := rec.session(0)
	if agent != "claude" {
		t.Fatalf("the daemon was asked for %q", agent)
	}

	if n, err := c.Write([]byte("one\ntwo\nthree")); err != nil || n != 13 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if n, err := c.Write([]byte("\n")); err != nil || n != 1 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if got := rec.writesTo(sid); strings.Join(got, "|") != "one|two|three" {
		t.Fatalf("the daemon was written %q, want the three lines in order", got)
	}

	sink := l.Sink()
	sink.Send(sid, agents.FrameOut, "line from the agent", 0)
	if got := readLine(t, c); got != "line from the agent" {
		t.Fatalf("read %q", got)
	}
	sink.Send(sid, agents.FrameErr, "oops", 0)
	sink.Send(sid, agents.FrameExit, "", 3)
	select {
	case <-c.Done():
	case <-time.After(waitLimit):
		t.Fatal("the exit frame did not end the session")
	}
	if got := c.Stderr(); !strings.Contains(got, "oops") || !strings.Contains(got, "the agent exited with code 3") {
		t.Fatalf("stderr = %q", got)
	}

	c2 := e.open(t, r.ID)
	c2.Close()
	waitFor(t, "close to reach the daemon", func() bool { return rec.count(func(r *recorder) int { return len(r.closes) }) == 1 })
}

func TestHubErrorsFromTheDaemonReachTheApp(t *testing.T) {
	e := newHubEnv(t, false)
	rec := &recorder{open: func(string, string) (contract.Opened, error) {
		return contract.Opened{}, fmt.Errorf("unknown agent %q", "x")
	}}
	_, r, _ := pairedLink(t, e, rec, false, "")
	e.waitState(t, r.ID, "online")
	_, err := e.hub.Open(context.Background(), user, r.ID, "claude")
	if err == nil || err.Error() != `your daemon: unknown agent "x"` {
		t.Fatalf("Open = %v, want the daemon's own message", err)
	}
}

func TestTwelveMiBLinesGoInEachDirectionIntact(t *testing.T) {
	e := newHubEnv(t, false)
	rec := &recorder{}
	l, r, _ := pairedLink(t, e, rec, false, "")
	e.waitState(t, r.ID, "online")
	c := e.open(t, r.ID)
	sid, _ := rec.session(0)

	line := strings.Repeat("đ\U0001F600x", 12*1024*1024/7+1)
	line = line[:12*1024*1024]
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	got := rec.writesTo(sid)
	if len(got) != 1 || len(got[0]) != len(line) || sha256.Sum256([]byte(got[0])) != sha256.Sum256([]byte(line)) {
		t.Fatalf("the daemon received %d line(s), want one of %d bytes, intact", len(got), len(line))
	}

	l.Sink().Send(sid, agents.FrameOut, line, 0)
	back := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 1<<20)
		for {
			n, err := c.Read(buf)
			b.Write(buf[:n])
			if err != nil || strings.HasSuffix(b.String(), "\n") {
				back <- strings.TrimSuffix(b.String(), "\n")
				return
			}
		}
	}()
	select {
	case s := <-back:
		if len(s) != len(line) || sha256.Sum256([]byte(s)) != sha256.Sum256([]byte(line)) {
			t.Fatalf("the app read %d bytes, want %d, intact", len(s), len(line))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the 12 MiB line did not arrive")
	}
}

func TestRestartAndStopFromTheApp(t *testing.T) {
	t.Run("a managed daemon is told to restart, and any daemon to stop", func(t *testing.T) {
		e := newHubEnv(t, false)
		rec := &recorder{}
		_, r, _ := pairedLink(t, e, rec, true, "")
		e.waitState(t, r.ID, "online")
		if _, err := e.hub.Restart(user, r.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "restart", func() bool { return rec.count(func(r *recorder) int { return r.restarts }) == 1 })
		if _, err := e.hub.Stop(user, r.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "stop", func() bool { return rec.count(func(r *recorder) int { return r.stops }) == 1 })
	})
	t.Run("the hub does not ask a daemon that no service restarts", func(t *testing.T) {
		e := newHubEnv(t, false)
		rec := &recorder{}
		_, r, _ := pairedLink(t, e, rec, false, "")
		e.waitState(t, r.ID, "online")
		if _, err := e.hub.Restart(user, r.ID); err == nil || !strings.Contains(err.Error(), "cannot be restarted") {
			t.Fatalf("Restart = %v", err)
		}
		if n := rec.count(func(r *recorder) int { return r.restarts }); n != 0 {
			t.Fatalf("the daemon was told to restart %d times", n)
		}
	})
}

func TestARevokedTokenStopsTheLinkForGood(t *testing.T) {
	e := newHubEnv(t, false)
	l, r, ev := pairedLink(t, e, &recorder{}, false, "")
	e.waitState(t, r.ID, "online")
	token := "token-" + t.Name()
	if _, err := e.hub.Revoke(user, r.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rejection", func() bool { return len(ev.rejected()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if got := ev.rejected(); got[0] != Refused {
		t.Fatalf("rejected %q", got)
	}
	// one handshake that was accepted, one that was refused, and no more
	if n := e.store.handshakesOf(token); n != 2 {
		t.Fatalf("%d handshakes of the revoked token, want 2", n)
	}
	if l.Connected() {
		t.Fatal("still connected")
	}
}

func TestAServerThatCannotCheckTheTokenIsRetriedUntilItCan(t *testing.T) {
	e := newHubEnv(t, false)
	e.store.down.Store(true)
	_, r, ev := pairedLink(t, e, &recorder{}, false, "")
	waitFor(t, "trouble", func() bool { return len(ev.trouble()) >= 2 })
	if got := ev.trouble()[0]; got != "unavailable" {
		t.Fatalf("trouble = %q", got)
	}
	if len(ev.rejected()) != 0 {
		t.Fatal("unavailable was taken for a refusal")
	}
	e.store.down.Store(false)
	e.waitState(t, r.ID, "online")
}

func TestTheLinkComesBackAfterTheNetworkCutsIt(t *testing.T) {
	e := newHubEnv(t, false)
	p := newProxy(t, strings.TrimPrefix(e.srv.URL, "http://"))
	rec := &recorder{}
	_, r, ev := pairedLink(t, e, rec, false, p.url())
	e.waitState(t, r.ID, "online")
	c := e.open(t, r.ID)

	p.sever()
	select {
	case <-c.Done():
	case <-time.After(waitLimit):
		t.Fatal("the session did not end with the connection")
	}
	waitFor(t, "the daemon to reconnect", func() bool { return ev.connected() == 2 })
	e.waitState(t, r.ID, "online")
	if got := ev.disconnected(); len(got) != 1 || got[0] != "transport close" {
		t.Fatalf("disconnect reasons = %q", got)
	}
	e.open(t, r.ID) // serves again
}

func TestTheHubReplacingTheConnectionEndsTheFirstAndTheLinkComesBack(t *testing.T) {
	e := newHubEnv(t, false)
	_, r, ev := pairedLink(t, e, &recorder{}, false, "")
	e.waitState(t, r.ID, "online")
	// a second connection of the same daemon: the hub keeps one per daemon and closes the first
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	second, err := dialConn(ctx, e.srv.URL, "/agentd", map[string]string{"role": "daemon", "token": "token-" + t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	defer second.finish(reasonClient)
	waitFor(t, "the first connection to end", func() bool { return len(ev.disconnected()) >= 1 })
	// the hub closes the socket, and the DISCONNECT packet may or may not arrive before the transport closes
	if got := ev.disconnected()[0]; got != "io server disconnect" && got != "transport close" {
		t.Fatalf("reason = %q", got)
	}
	waitFor(t, "the link to connect again", func() bool { return ev.connected() >= 2 })
}
