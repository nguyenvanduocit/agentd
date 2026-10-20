package run

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
)

// The last paired app leaving ends the daemon (see TestTheLastAppLeavingStopsTheDaemonUnlessAPageGrantIsLive), unless
// something may still use it. A program on this computer that is connected over /acp is using it: the editor must not
// be cut off with 1001 in the middle of a session.
func TestTheLastAppLeavingKeepsTheDaemonUpWhileAProgramIsConnectedOverACP(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := startRig(t, acpAgent(pidFile), map[string]string{"AGENTD_LOCAL_PORT": "0"})
	var port int
	waitFor(t, "the gateway port", func() bool {
		m := gatewayLine.FindStringSubmatch(r.d.out.String())
		if m == nil {
			return false
		}
		port, _ = strconv.Atoi(m[1])
		return true
	})

	ws, _ := dialACP(t, r.box, port, "/acp/claude", "")
	if err := ws.WriteMessage(websocket.TextMessage, []byte(acpInitialize)); err != nil {
		t.Fatal(err)
	}
	readText(t, ws)

	r.app.stopDaemon(t) // the only paired app leaves
	waitFor(t, "the daemon to decide", func() bool { return r.d.hasExited() || r.d.says("staying up") })
	if r.d.hasExited() {
		t.Fatalf("the daemon ended when the last app left, with a program connected over /acp; output:\n%s", r.d.out)
	}
	if !r.d.says("the last app left; staying up for the programs connected over /acp") {
		t.Errorf("the log does not say why the daemon stays up; output:\n%s", r.d.out)
	}

	// The connection is still served.
	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"still":"here"}`)); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, ws); got != `{"still":"here"}` {
		t.Fatalf("the connection answered %q", got)
	}
}
