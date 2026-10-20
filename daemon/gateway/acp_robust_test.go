package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Tests of what holds the /acp endpoint together under load and at its edges: the size of a message, an agent that does
// not read, a handshake that fails, a client that keeps talking but never answers a ping, a shutdown that overtakes a
// connection, and the goroutines a connection leaves behind.

// jsonOfSize is a one-line JSON object of exactly n bytes.
func jsonOfSize(n int) string {
	const frame = `{"x":""}`
	return `{"x":"` + strings.Repeat("a", n-len(frame)) + `"}`
}

// A message may be as long as the read limit (32 MiB); the inbox that holds what an agent has not read yet is smaller
// (16 MiB), but an empty one takes any message the limit let in.
func TestAcpMessagesUpToTheReadLimitReachTheAgent(t *testing.T) {
	for _, mib := range []int{17, 31} {
		t.Run(fmt.Sprintf("%d MiB", mib), func(t *testing.T) {
			if testing.Short() && mib > 17 {
				t.Skip("-short: a message of this size takes about a second and some memory")
			}
			r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
			c := r.mustDial("/acp/claude", "")
			line := jsonOfSize(mib << 20)
			c.send(line)
			if got := c.next(); got != line {
				t.Fatalf("the agent gave back %d bytes, want the %d bytes sent", len(got), len(line))
			}
		})
	}
}

func TestAcpAMessageBeyondTheReadLimitClosesTheSocketWith1009(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: sends 33 MiB")
	}
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
	c := r.mustDial("/acp/claude", "")
	_ = c.ws.WriteMessage(websocket.TextMessage, []byte(jsonOfSize(33<<20))) // the daemon reads on, so this ends
	c.expectClose(websocket.CloseMessageTooBig, "")
}

// An agent that does not read its input must not make the daemon hold what the client keeps sending: past the bound the
// client is told to try again later (1013) and the agent ends.
func TestAcpAnAgentThatDoesNotReadIsCutOffInsteadOfBuffered(t *testing.T) {
	t.Run("many small messages", func(t *testing.T) {
		r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "sleep 300")})
		c := r.mustDial("/acp/claude", "")
		go func() { // enough to fill the pipe to the agent and then the inbox's 1024 messages
			line := []byte(jsonOfSize(100))
			for range 20000 {
				if c.ws.WriteMessage(websocket.TextMessage, line) != nil {
					return
				}
			}
		}()
		c.expectClose(websocket.CloseTryAgainLater, "not reading")
		waitFor(t, "the agent to end", acpWait, func() bool { return r.acp.Size() == 0 })
	})
	t.Run("large messages", func(t *testing.T) {
		if testing.Short() {
			t.Skip("-short: sends about 100 MiB")
		}
		r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "sleep 300")})
		c := r.mustDial("/acp/claude", "")
		go func() {
			line := []byte(jsonOfSize(20 << 20))
			for range 6 {
				if c.ws.WriteMessage(websocket.TextMessage, line) != nil {
					return
				}
			}
		}()
		c.expectClose(websocket.CloseTryAgainLater, "not reading")
		waitFor(t, "the agent to end", acpWait, func() bool { return r.acp.Size() == 0 })
	})
}

// failedUpgrade makes a request the gateway lets through to the upgrade (it carries the token), which then fails (the
// key is not one), and returns the status the library answered.
func failedUpgrade(t *testing.T, port int, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/acp/claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range map[string]string{
		"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "not-a-key",
	} {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// A connection that never got past the handshake gives its place back: otherwise a few bad requests would use up the
// eight places for good.
func TestAcpFailedUpgradesGiveTheirPlacesBack(t *testing.T) {
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
	for i := range MaxAcpConnections + 4 {
		if got := failedUpgrade(t, r.port, r.token()); got != http.StatusBadRequest {
			t.Fatalf("failed upgrade %d answered %d, want the library's 400 (a 429 means the places ran out)", i, got)
		}
	}
	// A failed upgrade gives its place back once: not too few (a 429 on a place that is free) and not too many (a ninth
	// connection let in because the count went below what is open).
	var held []*acpConn
	for range MaxAcpConnections {
		held = append(held, r.mustDial("/acp/claude", ""))
	}
	if _, res, err := r.dialRaw("/acp/claude", "", bearer(r.token())); err == nil || res == nil || res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the connection after the %d places: %v (response %v), want a 429", MaxAcpConnections, err, res)
	}
	_ = held
}

// Data is a sign of life as much as a pong is: a client that keeps sending stays, though it never reads and so never
// answers a ping.
func TestAcpAClientThatKeepsSendingIsNotDroppedForSilence(t *testing.T) {
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat >/dev/null"), tune: func(o *AcpOptions) {
		o.PingEvery, o.PongWithin = 40*time.Millisecond, 400*time.Millisecond
	}})
	c := r.mustDialDeaf("/acp/claude", "") // nothing reads it, so no ping is ever answered
	waitFor(t, "the agent to start", acpWait, func() bool { return r.acp.Size() == 1 })
	for i := range 15 { // 1.5 s, almost four times the time a silent client has
		time.Sleep(100 * time.Millisecond)
		if err := c.ws.WriteMessage(websocket.TextMessage, []byte(`{"alive":1}`)); err != nil {
			t.Fatalf("message %d: the connection was dropped while the client was sending: %v", i, err)
		}
	}
	if got := r.acp.Size(); got != 1 {
		t.Fatalf("%d agents running, want 1: the connection ended while the client was sending", got)
	}
}

// A connection that reaches the sessions after their shutdown began (it was admitted a moment before) is closed with
// 1001 and starts no agent.
func TestAcpAConnectionThatArrivesAfterShutdownBeganStartsNoAgent(t *testing.T) {
	r, _, pidFile := newAcpRig(t, acpRigOptions{})
	r.acp.Drain()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := r.acp.upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.acp.serve(conn, "late", AcpTarget{Agent: "claude"}, func() {}) // as Serve does, past its admission
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = ws.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != websocket.CloseGoingAway || !strings.Contains(ce.Text, "stopping") {
		t.Fatalf("read: %v, want a close with 1001 and \"stopping\"", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("an agent was started for a connection that arrived after the shutdown began")
	}
	if got := r.acp.Size(); got != 0 {
		t.Fatalf("%d agents running, want none", got)
	}
}

// settledGoroutines is the number of goroutines once it has stopped changing.
func settledGoroutines() int {
	last := -1
	for range 40 {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n == last {
			return n
		}
		last = n
		time.Sleep(75 * time.Millisecond)
	}
	return last
}

// Connections that the client ended leave no goroutine behind: no Shutdown, no allowance. Every connection starts a
// reader, a pump and a pinger; a leak of one of them per connection shows over twenty-five connections.
func TestAcpConnectionsTheClientEndedLeaveNoGoroutinesBehind(t *testing.T) {
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
	cycle := func() {
		c := r.mustDial("/acp/claude", "")
		c.send(`{"n":1}`)
		if got := c.next(); got != `{"n":1}` {
			t.Fatalf("echo = %q", got)
		}
		_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = c.ws.Close()
		waitFor(t, "the agent of the connection to end", acpWait, func() bool { return r.acp.Size() == 0 })
	}
	for range 3 { // the server's and the library's own goroutines exist from here on
		cycle()
	}
	before := settledGoroutines()
	for range 25 {
		cycle()
	}
	waitFor(t, fmt.Sprintf("the goroutines to be back at %d", before), 5*time.Second, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= before
	})
}
