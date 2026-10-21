package gateway

import (
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

func TestAcpClosingTheSocketEndsTheAgent(t *testing.T) {
	r, _, pid := newAcpRig(t, acpRigOptions{})
	c := r.mustDial("/acp/claude", "")
	c.send(initializeLine)
	_ = c.next()
	agent := acpPid(t, pid)
	if !groupAlive(agent) || r.acp.Size() != 1 {
		t.Fatalf("agent alive %v, size %d", groupAlive(agent), r.acp.Size())
	}
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = c.ws.Close()
	waitGone(t, agent)
	waitFor(t, "no agent left", acpWait, func() bool { return r.acp.Size() == 0 })
}

func TestAcpADroppedSocketEndsTheAgentToo(t *testing.T) {
	r, _, pid := newAcpRig(t, acpRigOptions{})
	c := r.mustDial("/acp/claude", "")
	agent := acpPid(t, pid)
	_ = c.ws.UnderlyingConn().Close() // no close frame
	waitGone(t, agent)
	waitFor(t, "no agent left", acpWait, func() bool { return r.acp.Size() == 0 })
}

func TestAcpAnAgentThatExitsClosesTheSocketNormally(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{agent: agentScript(t, `read line; echo '{"bye":1}'; exit 7`)})
	c := r.mustDial("/acp/claude", "")
	c.send(`{"hello":1}`)
	if got := c.next(); got != `{"bye":1}` {
		t.Fatalf("frame = %q", got)
	}
	c.expectClose(1000, "agent exited with code 7")
	waitFor(t, "no agent left", acpWait, func() bool { return r.acp.Size() == 0 })
}

// An agent that is "ready" (a seam command counts as ready) but cannot be started is found out after the upgrade: the
// socket closes with 1011 and says why, and nothing is left behind. (An agent that is not ready is refused with a 503
// before the upgrade; see TestAcpRoutesAndTheirRefusals.)
func TestAcpAnAgentThatCannotStartClosesTheSocketWith1011AndSaysWhy(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{agent: "/definitely/not/here --flag"})
	c, res, err := r.dial("/acp/claude", "")
	if err != nil {
		t.Fatalf("dial: %v %v", err, res)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101: the failure is found out after the upgrade", res.StatusCode)
	}
	c.expectClose(websocket.CloseInternalServerErr, `agent claude: command "/definitely/not/here" not found in PATH`)
	waitFor(t, "no agent left", acpWait, func() bool { return r.acp.Size() == 0 })
	// The place is free again.
	for range MaxAcpConnections + 1 {
		c, _, err := r.dial("/acp/claude", "")
		if err != nil {
			t.Fatalf("a failed start kept its place: %v", err)
		}
		c.expectClose(websocket.CloseInternalServerErr, "not found in PATH")
	}
}

func TestAcpShuttingDownClosesWithGoingAwayAndRefusesNewOnes(t *testing.T) {
	r, _, pid := newAcpRig(t, acpRigOptions{})
	web := r.mustDial("/acp/claude", page)
	local := r.mustDial("/acp/claude", "")
	agent := acpPid(t, pid)
	r.acp.Drain()
	got := r.get(http.MethodGet, "/acp/claude", "", true)
	wantReply(t, got, http.StatusServiceUnavailable, map[string]any{"reason": "error"})
	if msg, _ := got.body["message"].(string); !strings.Contains(msg, "stopping") {
		t.Errorf("message %q", msg)
	}
	go r.acp.Shutdown(3 * time.Second)
	web.expectClose(1001, "stopping")
	local.expectClose(1001, "stopping")
	waitGone(t, agent)
}

func TestAcpWithdrawingAnOriginEndsItsConnectionsAndLeavesLocalOnesAlone(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})
	web := r.mustDial("/acp/claude", page)
	local := r.mustDial("/acp/claude", "")
	if got := r.acp.Origins(); !slices.Equal(got, []string{page}) {
		t.Fatalf("Origins = %v, want only the web page", got)
	}
	if r.acp.Size() != 2 {
		t.Fatalf("Size = %d, want 2", r.acp.Size())
	}
	r.acp.EndOrigin(page)
	web.expectClose(1008, "access")
	local.send(`{"still":"here"}`)
	if got := local.next(); got != `{"still":"here"}` {
		t.Fatalf("local connection answered %q", got)
	}
}

func TestAcpDisconnectingThroughTheGatewayEndsTheConnections(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})
	web := r.mustDial("/acp/claude", page)
	got := call(t, r.port, http.MethodDelete, "/v1/connect", callOpts{origin: page})
	wantReply(t, got, http.StatusOK, nil)
	web.expectClose(1008, "access")
}

func TestAcpLimits(t *testing.T) {
	t.Run("eight connections in all, four for one page", func(t *testing.T) {
		r, _, _ := newAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
		r.world.mu.Lock()
		r.world.reg = r.world.reg.AddBrowser(other, time.Now(), registry.Access{})
		r.world.mu.Unlock()
		var held []*acpConn
		for range 4 {
			held = append(held, r.mustDial("/acp/claude", page))
		}
		wantReply(t, r.get(http.MethodGet, "/acp/claude", page, true), http.StatusTooManyRequests, map[string]any{"reason": "busy"})
		for range 4 {
			held = append(held, r.mustDial("/acp/claude", other))
		}
		wantReply(t, r.get(http.MethodGet, "/acp/claude", "", true), http.StatusTooManyRequests, map[string]any{"reason": "busy"})

		// A slot comes back when its connection ends.
		_ = held[0].ws.Close()
		waitFor(t, "a free slot", acpWait, func() bool {
			c, _, err := r.dial("/acp/claude", page)
			if err != nil {
				return false
			}
			held = append(held, c)
			return true
		})
	})
	t.Run("local programs share the total", func(t *testing.T) {
		r, _, _ := newAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
		for range 8 {
			r.mustDial("/acp/claude", "")
		}
		wantReply(t, r.get(http.MethodGet, "/acp/claude", "", true), http.StatusTooManyRequests, map[string]any{"reason": "busy"})
	})
}

func TestAcpAClientThatNeverAnswersPingsIsDropped(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{tune: func(o *AcpOptions) {
		o.PingEvery, o.PongWithin = 40*time.Millisecond, 400*time.Millisecond
	}})
	c := r.mustDialDeaf("/acp/claude", "") // nothing reads it, so no pong is ever sent
	waitFor(t, "the agent to start", acpWait, func() bool { return r.acp.Size() == 1 })
	waitFor(t, "the silent client to be dropped and its agent gone", acpWait, func() bool { return r.acp.Size() == 0 })
	_ = c
}

func TestAcpAClientThatReadsKeepsItsConnection(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{tune: func(o *AcpOptions) {
		o.PingEvery, o.PongWithin = 40*time.Millisecond, 400*time.Millisecond
	}})
	c := r.mustDial("/acp/claude", "")
	c.silence(1500 * time.Millisecond) // reading answers every ping on its own
	c.send(`{"alive":1}`)
	if got := c.next(); got != `{"alive":1}` {
		t.Fatalf("got %q", got)
	}
}

// The daemon cuts a client that does not read loose, and nothing of the connection stays: no Shutdown helps it here, and
// the count of goroutines must be back to what it was with the gateway up and no connection, with no allowance.
func TestAcpASlowReaderIsCutLooseAndLeavesNothingBehind(t *testing.T) {
	pid := acpPidFileIn(shortDir(t))
	agent := agentScript(t, `echo $$ > `+pid+`; yes '{"jsonrpc":"2.0","method":"session/update","params":{"text":"`+strings.Repeat("x", 4000)+`"}}'`)
	r, _, _ := newAcpRig(t, acpRigOptions{agent: agent, tune: func(o *AcpOptions) { o.WriteWithin = 300 * time.Millisecond }})
	before := settledGoroutines()
	c := r.mustDialDeaf("/acp/claude", "") // connected, and never reads
	agentPid := acpPid(t, pid)
	waitGone(t, agentPid)
	waitFor(t, "no agent left", acpWait, func() bool { return r.acp.Size() == 0 })
	_ = c.ws.Close()
	waitFor(t, fmt.Sprintf("the goroutines of the connection to end (back to %d)", before), 5*time.Second, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= before
	})
}

func TestAcpAccountingCountsRunningAgents(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
	if r.acp.Size() != 0 || len(r.acp.Origins()) != 0 {
		t.Fatalf("an idle endpoint reports %d agents %v", r.acp.Size(), r.acp.Origins())
	}
	a := r.mustDial("/acp/claude", page)
	b := r.mustDial("/acp/claude", page)
	_ = r.mustDial("/acp/claude", "")
	waitFor(t, "three agents", acpWait, func() bool { return r.acp.Size() == 3 })
	if got := r.acp.Origins(); !slices.Equal(got, []string{page}) {
		t.Fatalf("Origins = %v, want the page once", got)
	}
	_ = a.ws.Close()
	_ = b.ws.Close()
	waitFor(t, "one agent", acpWait, func() bool { return r.acp.Size() == 1 })
}
