package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/hub"
)

// Runs the real Claude adapter through the daemon and a fake app. It costs a little money (two short turns), so it only
// runs with RUN_REAL_CLAUDE=1 and a logged-in Claude on this machine:
//
//	RUN_REAL_CLAUDE=1 go test -count=1 -run Real -v ./daemon/run/

// realAdapter is the Claude ACP adapter the agent/ package installs.
const realAdapter = "../../agent/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"

// realClaude skips the test unless a real Claude run is asked for and possible. It returns the command line of the
// "claude" agent (the AGENTD_AGENT_CLAUDE seam) and the environment a real run needs on top of the sandbox's.
func realClaude(t *testing.T) (command string, env map[string]string) {
	t.Helper()
	if os.Getenv("RUN_REAL_CLAUDE") != "1" {
		t.Skip("set RUN_REAL_CLAUDE=1 to run the real Claude adapter (it costs a few cents)")
	}
	adapter, err := filepath.Abs(realAdapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(adapter); err != nil {
		t.Skipf("the Claude adapter is not installed at %s (run bun install in agent/): %v", adapter, err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	// The sandbox's HOME is empty; the adapter finds the user's Claude login under the real one.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to find the Claude login in: %v", err)
	}
	return fmt.Sprintf("'%s' '%s'", node, adapter), map[string]string{"HOME": home}
}

// processTree is the pids of pid and everything below it.
func processTree(t *testing.T, pid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var rows [][2]int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		p, e1 := strconv.Atoi(f[0])
		pp, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			rows = append(rows, [2]int{p, pp})
		}
	}
	tree := []int{pid}
	for i := 0; i < len(tree); i++ {
		for _, r := range rows {
			if r[1] == tree[i] && !containsInt(tree, r[0]) {
				tree = append(tree, r[0])
			}
		}
	}
	return tree
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// listeningPorts is lsof's table of the listening TCP sockets of these pids: a header and a row each, or nothing (lsof
// exits 1 and prints nothing when there are none).
func listeningPorts(t *testing.T, pids []int) string {
	t.Helper()
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", strings.Join(list, ",")).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return strings.TrimSpace(string(out))
		}
		t.Fatalf("lsof: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// startAppWithMcp is a fake app that also serves the MCP bridges of its sessions (`agentd-mcp --session <id>` dials
// the app with role "mcp"): one tool, ping, answers "pong".
func startAppWithMcp(t *testing.T) *fakeApp {
	t.Helper()
	a := &fakeApp{token: testToken}
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(hub.MaxMessageBytes)
	a.io = socket.NewServer(nil, opts)
	ns := a.io.Of(hub.Namespace, nil)
	ns.Use(func(sock *socket.Socket, next func(*socket.ExtendedError)) {
		if role, _ := sock.Handshake().Auth["role"].(string); role == "mcp" {
			if token, _ := sock.Handshake().Auth["token"].(string); token == a.token {
				next(nil)
				return
			}
			next(socket.NewExtendedError("unauthorized", nil))
			return
		}
		a.authenticate(sock, next)
	})
	if err := ns.On("connection", func(c ...any) {
		raw, ok := c[0].(*socket.Socket)
		if !ok {
			t.Errorf("connection event carries %T", c[0])
			return
		}
		if role, _ := raw.Handshake().Auth["role"].(string); role == "mcp" {
			serveBridge(t, raw)
			return
		}
		a.onConnection(t, c)
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/socket.io/", a.io.ServeHandler(nil))
	a.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		a.io.Close(nil)
		a.srv.CloseClientConnections()
		a.srv.Close()
	})
	return a
}

// serveBridge serves the ping tool to one bridge socket: its lines feed an in-memory MCP server, whose lines go back
// to the bridge.
func serveBridge(t *testing.T, raw *socket.Socket) {
	t.Helper()
	client, err := contract.NewClient(raw, &contract.ClientOptions{Timeout: callTimeout})
	if err != nil {
		t.Errorf("rpc client: %v", err)
		raw.Disconnect(true)
		return
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := mcp.NewServer(&mcp.Implementation{Name: "app", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping", Description: "Returns the text pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	if _, err := srv.Connect(context.Background(), &mcp.IOTransport{Reader: inR, Writer: outW}, nil); err != nil {
		t.Errorf("mcp server: %v", err)
		raw.Disconnect(true)
		return
	}
	go func() {
		var pending []byte
		buf := make([]byte, 64<<10)
		for {
			n, err := outR.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				_ = client.CallMcpOut(context.Background(), string(pending[:i]))
				pending = pending[i+1:]
			}
			if err != nil {
				return
			}
		}
	}()
	_ = raw.On("disconnect", func(...any) { _ = inW.Close() })
	if _, err := contract.BindServer(raw, mcpBridgeHandler{in: inW}); err != nil {
		t.Errorf("bind: %v", err)
		raw.Disconnect(true)
	}
}

// mcpBridgeHandler is the app's side of an MCP bridge socket: only MCP lines are for it.
type mcpBridgeHandler struct{ in io.Writer }

var errNotDaemon = fmt.Errorf("not a daemon")

func (mcpBridgeHandler) HandleFrame(context.Context, string, contract.FrameKind, string, float64) error {
	return errNotDaemon
}

func (mcpBridgeHandler) HandleHello(context.Context, contract.Hello) (contract.HelloReply, error) {
	return contract.HelloReply{}, errNotDaemon
}

func (mcpBridgeHandler) HandleAgents(context.Context, []contract.AgentInfo) error {
	return errNotDaemon
}

func (h mcpBridgeHandler) HandleMcp(_ context.Context, line string) error {
	_, err := h.in.Write([]byte(line + "\n"))
	return err
}

// acp speaks ACP JSON-RPC to the agent through the fake app, by hand.
type acp struct {
	t       *testing.T
	app     *fakeApp
	session string
	nextID  int
	// answered is how many of the agent's lines have been looked at for requests of its own.
	answered int
}

// lines are what the agent wrote on its stdout, as JSON-RPC messages.
func (c *acp) lines() []map[string]any {
	var out []map[string]any
	for _, f := range c.app.framesOf(c.session) {
		if f.Kind != "out" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(f.Data), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (c *acp) send(m map[string]any) {
	c.t.Helper()
	m["jsonrpc"] = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	c.app.write(c.t, c.session, string(data))
}

// serve answers what the agent asks of its client while a request is pending: permission requests get the first
// option.
func (c *acp) serve() {
	c.t.Helper()
	all := c.lines()
	for _, m := range all[c.answered:] {
		method, isRequest := m["method"].(string)
		if !isRequest || m["id"] == nil {
			continue
		}
		if method != "session/request_permission" {
			c.send(map[string]any{"id": m["id"], "error": map[string]any{"code": -32601, "message": "not supported"}})
			continue
		}
		var optionID any
		if params, ok := m["params"].(map[string]any); ok {
			if options, ok := params["options"].([]any); ok && len(options) > 0 {
				if first, ok := options[0].(map[string]any); ok {
					optionID = first["optionId"]
				}
			}
		}
		c.send(map[string]any{"id": m["id"], "result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}})
	}
	c.answered = len(all)
}

// request sends a request and waits (up to 3 minutes) for its result.
func (c *acp) request(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"id": id, "method": method, "params": params})
	var result map[string]any
	waitForWithin(c.t, 3*time.Minute, "the answer to "+method, func() bool {
		c.serve()
		for _, m := range c.lines() {
			if number(m["id"]) != id || m["method"] != nil {
				continue
			}
			if m["error"] != nil {
				c.t.Fatalf("%s: %v", method, m["error"])
			}
			result, _ = m["result"].(map[string]any)
			return result != nil
		}
		return false
	})
	return result
}

// text is everything the agent's session produced, frame by frame.
func (c *acp) text() string {
	var parts []string
	for _, f := range c.app.framesOf(c.session) {
		parts = append(parts, f.Data)
	}
	return strings.Join(parts, "\n")
}

func TestRealClaudeATurnThenAGraphToolCallThroughAgentdMcpAndTheGatewayIsTheOnlyListeningPort(t *testing.T) {
	command, env := realClaude(t)
	r := &rig{app: startAppWithMcp(t), box: newSandbox(t)}
	r.box.writeServers(t, r.app.origin())
	r.start(t, command, env)
	r.app.waitHellos(t, 1)
	r.app.openOK(t, "s1", "claude")
	c := &acp{t: t, app: r.app, session: "s1"}

	c.request("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "daemon-test", "version": "1"}})
	server := map[string]any{"name": "app", "command": "agentd-mcp", "args": []string{"--session", "s1"}, "env": []any{}}
	meta := map[string]any{"claudeCode": map[string]any{"emitRawSDKMessages": true, "options": map[string]any{"settingSources": []any{}}}}
	created := c.request("session/new", map[string]any{"cwd": r.box.work, "mcpServers": []any{server}, "_meta": meta})
	sessionID, _ := created["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("session/new answered %v", created)
	}
	c.request("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "mode", "value": "bypassPermissions"})

	turn1 := c.request("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "Setup check. Reply with the single word: ready."}}})
	if turn1["stopReason"] != "end_turn" {
		t.Fatalf("turn 1 stopReason = %v", turn1["stopReason"])
	}
	if !strings.Contains(strings.ToLower(c.text()), "ready") {
		t.Fatalf("turn 1 did not say ready: %s", c.text())
	}

	turn2 := c.request("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "Call the ping tool of the app MCP server and tell me exactly what it returned."}}})
	if turn2["stopReason"] != "end_turn" {
		t.Fatalf("turn 2 stopReason = %v", turn2["stopReason"])
	}
	events := c.text()
	for _, want := range []string{"mcp__app__ping", "pong"} {
		if !strings.Contains(events, want) {
			t.Fatalf("the agent's events do not contain %q", want)
		}
	}

	listeners := listeningPorts(t, processTree(t, r.d.pid()))
	var costs []string
	for _, m := range regexp.MustCompile(`"total_cost_usd":([0-9.]+)`).FindAllStringSubmatch(events, -1) {
		costs = append(costs, m[1])
	}
	t.Logf("REAL CLAUDE: turn1=%v turn2=%v listeners=%q cost_usd=%s", turn1["stopReason"], turn2["stopReason"], listeners, strings.Join(costs, ","))
	rows := strings.Split(listeners, "\n")
	if len(rows) < 1 {
		t.Fatal("no lsof output")
	}
	rows = rows[1:]
	if len(rows) != 1 || !strings.Contains(rows[0], "127.0.0.1:") {
		t.Fatalf("the gateway should be the only listening port; lsof says:\n%s", listeners)
	}
}
