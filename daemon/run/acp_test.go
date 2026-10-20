package run

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The standard ACP endpoint of a whole daemon: a real process, a real gateway, a fake agent. (The endpoint's own rules
// are tested in daemon/gateway.)

var connectionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const acpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`

// acpAgent is a fake agent: it writes its pid to pidFile and answers initialize.
func acpAgent(pidFile string) string {
	return sh(`echo $$ > ` + pidFile + `; while read line; do case "$line" in *initialize*) echo "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1}}";; *) echo "$line";; esac; done`)
}

// dialACP opens /acp<path> as a web page (origin not empty) or as a program on this computer, which carries the
// daemon's token.
func dialACP(t *testing.T, box *sandbox, port int, path, origin string) (*websocket.Conn, *http.Response) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	} else {
		header.Set("Authorization", "Bearer "+box.acpToken(t))
	}
	ws, res, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+path, header)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws, res
}

func agentPid(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	waitFor(t, "the agent's pid", func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	})
	return pid
}

func readText(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestARealDaemonServesTheStandardACPEndpointToALocalProgram(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(pidFile)})

	ws, res := dialACP(t, g.box, g.port, "/acp/claude", "")
	if id := res.Header.Get("Acp-Connection-Id"); !connectionID.MatchString(id) {
		t.Fatalf("Acp-Connection-Id = %q, want a version 4 uuid", id)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte(acpInitialize)); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, ws); !strings.Contains(got, `"protocolVersion":1`) {
		t.Fatalf("initialize answered %q", got)
	}
	pid := agentPid(t, pidFile)
	if !groupAlive(pid) {
		t.Fatal("the agent is not running while the connection is open")
	}

	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = ws.Close()
	waitGroupGone(t, pid)
	waitFor(t, "the daemon to log the connection", func() bool { return g.d.says("acp connection ") && g.d.says("claude for a local program") })
}

func TestARealDaemonHoldsAWebPageToItsGrant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(pidFile)})

	denied := g.call(t, http.MethodGet, "/acp/claude", callOptions{origin: origin(pageOrigin), headers: map[string]string{
		"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
	}})
	if denied.status != http.StatusForbidden || denied.body["reason"] != "denied" {
		t.Fatalf("a page with no grant: %d %v", denied.status, denied.body)
	}

	g.box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: registry.GrantStamp(time.Now())}})
	web, _ := dialACP(t, g.box, g.port, "/acp/claude", pageOrigin)
	if err := web.WriteMessage(websocket.TextMessage, []byte(acpInitialize)); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, web); !strings.Contains(got, `"protocolVersion":1`) {
		t.Fatalf("initialize answered %q", got)
	}
}

func TestTakingAPagesGrantAwayEndsItsACPConnectionsAndNotALocalOne(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	g.box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: registry.GrantStamp(time.Now())}})

	web, _ := dialACP(t, g.box, g.port, "/acp/claude", pageOrigin)
	local, _ := dialACP(t, g.box, g.port, "/acp/claude", "")

	// Another process removes the grant (`agentd apps remove`): the daemon reads its registry every two seconds.
	g.box.writeApps(t, nil, nil)
	_ = web.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, _, err := web.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != websocket.ClosePolicyViolation {
		t.Fatalf("the page's connection ended with %v, want close 1008", err)
	}
	if err := local.WriteMessage(websocket.TextMessage, []byte(`{"still":"here"}`)); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, local); got != `{"still":"here"}` {
		t.Fatalf("the local connection answered %q", got)
	}
}

func TestStoppingTheDaemonClosesACPConnectionsWithGoingAway(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(pidFile)})
	ws, _ := dialACP(t, g.box, g.port, "/acp/claude", "")
	pid := agentPid(t, pidFile)

	g.d.signal(syscall.SIGTERM)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, _, err := ws.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != websocket.CloseGoingAway {
		t.Fatalf("the connection ended with %v, want close 1001", err)
	}
	waitGroupGone(t, pid)
	g.d.exitCode(t, 20*time.Second)
}

func TestTheAppCountsACPConnectionsAmongTheRunningAgents(t *testing.T) {
	box := newSandbox(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	d, app := startWithApp(t, box, map[string]string{"AGENTD_LOCAL_PORT": "0", "AGENTD_AGENT_CLAUDE": acpAgent(pidFile)})
	g := waitForGateway(t, d, box)

	// sessions is what the newest state line says; -1 before there is one.
	sessions := func() float64 {
		app.mu.Lock()
		defer app.mu.Unlock()
		for i := len(app.got) - 1; i >= 0; i-- {
			if app.got[i]["t"] == "state" {
				n, _ := app.got[i]["sessions"].(float64)
				return n
			}
		}
		return -1
	}
	waitFor(t, "a state with no running agent", func() bool { return sessions() == 0 })
	ws, _ := dialACP(t, g.box, g.port, "/acp/claude", "")
	_ = agentPid(t, pidFile)
	waitFor(t, "the ACP connection among the running agents", func() bool { return sessions() == 1 })
	_ = ws.Close()
	waitFor(t, "no running agent again", func() bool { return sessions() == 0 })
}
