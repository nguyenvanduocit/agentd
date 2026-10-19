package run

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// A page that was allowed to run agents: its WebSocket on /v1/session, the agents on it, and the tools the page serves
// to them through `agentd-mcp --page`. (The daemon's gateway and sessions on their own are tested in daemon/gateway.)

const otherOrigin = "http://localhost:5174"

type message = map[string]any

// page is a page's socket: what the daemon sent, in order.
type page struct {
	conn *websocket.Conn
	wmu  sync.Mutex // one write at a time
	mu   sync.Mutex
	msgs []message
	// closeCode is the code of the close frame the daemon sent, 0 until it did.
	closeCode int
	closed    chan struct{}
	onMessage func(message)
}

func (p *page) write(m any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return p.conn.WriteMessage(websocket.TextMessage, data)
}

func (p *page) send(t *testing.T, m any) {
	t.Helper()
	if err := p.write(m); err != nil {
		t.Fatalf("send %v: %v", m, err)
	}
}

func (p *page) messages() []message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]message(nil), p.msgs...)
}

// next waits for a message that pred accepts.
func (p *page) next(t *testing.T, what string, pred func(message) bool) message {
	t.Helper()
	var found message
	waitFor(t, what, func() bool {
		for _, m := range p.messages() {
			if pred(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func ofType(typ string) func(message) bool { return func(m message) bool { return m["t"] == typ } }

func frameOf(kind string) func(message) bool {
	return func(m message) bool { return m["t"] == "frame" && m["kind"] == kind }
}

// dial opens /v1/session as `from` (no Origin header when empty) with the given Host header (the real one when empty).
func dial(t *testing.T, port int, from, host string) (*page, error) {
	t.Helper()
	header := http.Header{}
	if from != "" {
		header.Set("Origin", from)
	}
	if host != "" {
		header.Set("Host", host)
	}
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/v1/session", port), header)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 30)
	p := &page{conn: conn, closed: make(chan struct{})}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		defer close(p.closed)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				if ce, ok := err.(*websocket.CloseError); ok {
					p.mu.Lock()
					p.closeCode = ce.Code
					p.mu.Unlock()
				}
				return
			}
			var m message
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			p.mu.Lock()
			p.msgs = append(p.msgs, m)
			hook := p.onMessage
			p.mu.Unlock()
			if hook != nil {
				hook(m)
			}
		}
	}()
	return p, nil
}

func mustDial(t *testing.T, port int, from string) *page {
	t.Helper()
	p, err := dial(t, port, from, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *page) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-p.closed:
	case <-time.After(waitLimit):
		t.Fatal("the socket did not close")
	}
}

func (p *page) code() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeCode
}

func number(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// startBrowserDaemon is a daemon with no apps that has allowed pageOrigin as a browser origin; its "claude" agent is
// `agent`, by default one that echoes every line.
func startBrowserDaemon(t *testing.T, agent string) *gatewayDaemon {
	t.Helper()
	box := newSandbox(t)
	box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: "2026-10-19T00:00:00.000Z"}})
	return launchGatewayDaemon(t, box, agent)
}

func launchGatewayDaemon(t *testing.T, box *sandbox, agent string) *gatewayDaemon {
	t.Helper()
	return waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": agent}), box)
}

// open opens session `id` on the page and returns the `opened` message; the agent's group is killed at the end.
func openSession(t *testing.T, p *page, id string) message {
	t.Helper()
	p.send(t, message{"t": "open", "id": id, "agent": "claude"})
	opened := p.next(t, "opened "+id, func(m message) bool { return m["t"] == "opened" && m["id"] == id })
	killGroupAtEnd(t, number(opened["pid"]))
	return opened
}

func TestBrowserGrantedOriginGetsHelloOpensAnAgentAndTalksToItThroughFrames(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	hello := p.next(t, "hello", ofType("hello"))
	claude := hello["agents"].([]any)[0].(message)
	if !jsonEqual(claude, message{"name": "claude", "title": "Claude Code", "ready": true, "reason": ""}) {
		t.Fatalf("claude = %v", claude)
	}
	opened := openSession(t, p, "s1")
	if opened["id"] != "s1" {
		t.Fatalf("opened = %v", opened)
	}
	p.send(t, message{"t": "write", "id": "s1", "line": `{"hello":"agent"}`})
	f := p.next(t, "the echoed line", frameOf("out"))
	if f["id"] != "s1" || f["data"] != `{"hello":"agent"}` {
		t.Fatalf("frame = %v", f)
	}
}

func TestBrowserAnUnknownAgentIsAnErrorMessageNotADeadSocket(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	p.send(t, message{"t": "open", "id": "s1", "agent": "rm -rf"})
	e := p.next(t, "error", ofType("error"))
	if e["id"] != "s1" || !strings.Contains(fmt.Sprint(e["message"]), `unknown agent "rm -rf"`) {
		t.Fatalf("error = %v", e)
	}
	p.send(t, message{"t": "open", "id": "s2", "agent": "claude"}) // the socket still works
	killGroupAtEnd(t, number(p.next(t, "opened", ofType("opened"))["pid"]))
}

func TestBrowserClosingTheSocketMidTurnEndsTheAgent(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	pid := number(openSession(t, p, "s1")["pid"])
	if !groupAlive(pid) {
		t.Fatal("the agent's group is not running")
	}
	_ = p.conn.Close()
	waitFor(t, "the agent group to end", func() bool { return !groupAlive(pid) })
}

func TestBrowserASixthSessionOnOneSocketIsRefused(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	for i := 1; i <= 5; i++ {
		openSession(t, p, fmt.Sprintf("s%d", i))
	}
	p.send(t, message{"t": "open", "id": "s6", "agent": "claude"})
	e := p.next(t, "error for s6", func(m message) bool { return m["t"] == "error" && m["id"] == "s6" })
	if !strings.Contains(fmt.Sprint(e["message"]), "at most 5") {
		t.Fatalf("error = %v", e)
	}
}

func TestBrowserAMessageThatIsNotOneOfOpenWriteCloseIsAnErrorAndChangesNothing(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	if err := p.conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if e := p.next(t, "error", ofType("error")); e["id"] != "" {
		t.Fatalf("error = %v", e)
	}
}

func TestBrowserAMessageAboveTheLimitClosesTheSocketAsTooBig(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	p.next(t, "hello", ofType("hello"))
	big, _ := json.Marshal(message{"t": "write", "id": "s1", "line": strings.Repeat("x", 65*1024*1024)})
	go func() { // the daemon stops reading at the limit: the rest of the message may never be taken
		p.wmu.Lock()
		defer p.wmu.Unlock()
		_ = p.conn.WriteMessage(websocket.TextMessage, big)
	}()
	p.waitClosed(t)
	if p.code() != websocket.CloseMessageTooBig {
		t.Fatalf("close code %d, want %d", p.code(), websocket.CloseMessageTooBig)
	}
}

func TestBrowserAnAgentLineAboveTheCapEndsTheSessionWithAnErrorThatSaysWhy(t *testing.T) {
	g := startBrowserDaemon(t, sh(`head -c 34603008 /dev/zero | tr "\0" x; sleep 60`))
	p := mustDial(t, g.port, pageOrigin)
	openSession(t, p, "s1")
	e := p.next(t, "the error frame", frameOf("err"))
	if !strings.Contains(fmt.Sprint(e["data"]), "longer than 32 MiB") {
		t.Fatalf("error frame = %v", e["data"])
	}
	p.next(t, "the exit frame", frameOf("exit"))
	for _, m := range p.messages() {
		if frameOf("out")(m) {
			t.Fatal("part of the line was handed on")
		}
	}
}

// stalledPage is a page on a raw TCP socket: after the handshake it sends `messages` and then reads nothing until told
// to.
func stalledPage(t *testing.T, port int, messages ...message) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	request := strings.Join([]string{
		"GET /v1/session HTTP/1.1",
		fmt.Sprintf("Host: 127.0.0.1:%d", port),
		"Origin: " + pageOrigin,
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
		"Sec-WebSocket-Version: 13",
		"", "",
	}, "\r\n")
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(waitLimit))
	n, err := conn.Read(answer)
	if err != nil || !strings.HasPrefix(string(answer[:n]), "HTTP/1.1 101") {
		t.Fatalf("handshake answer %q: %v", answer[:n], err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	// A masked text frame with an all-zero mask: the payload goes as it is. Each message here is under 126 bytes.
	for _, m := range messages {
		payload, _ := json.Marshal(m)
		frame := append([]byte{0x81, byte(0x80 | len(payload)), 0, 0, 0, 0}, payload...)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

// flooded reads what the flooding agent reports: its pid and the bytes its pipe has taken.
func flooded(report string) (pid, bytes int) {
	data, err := os.ReadFile(report)
	if err != nil {
		return 0, 0
	}
	_, _ = fmt.Sscanf(string(data), "%d %d", &pid, &bytes)
	return pid, bytes
}

func TestBrowserAPageThatStopsReadingStopsItsAgentsOutputInsteadOfFillingTheDaemonAndResumesIt(t *testing.T) {
	box := newSandbox(t)
	report := filepath.Join(box.root, "flood.count")
	box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: "2026-10-19T00:00:00.000Z"}})
	g := launchGatewayDaemon(t, box, binaries.helper+" flood "+report)
	conn := stalledPage(t, g.port, message{"t": "open", "id": "s1", "agent": "claude"})
	var pid int
	waitFor(t, "the agent's report", func() bool { pid, _ = flooded(report); return pid > 0 })
	killGroupAtEnd(t, pid)
	time.Sleep(3 * time.Second)
	_, stalled := flooded(report)
	time.Sleep(time.Second)
	// What the daemon holds: its socket's buffer (8 MiB) and the kernel's; without a pause it is hundreds of MB by now.
	if stalled >= 64*1024*1024 {
		t.Fatalf("the agent wrote %d bytes into a page that reads nothing", stalled)
	}
	if _, now := flooded(report); now >= stalled+1024*1024 {
		t.Fatalf("stopped, not slowed: %d bytes, then %d", stalled, now)
	}
	if !groupAlive(pid) {
		t.Fatal("the agent ended")
	}
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	waitForWithin(t, 10*time.Second, "the agent to write again", func() bool { _, now := flooded(report); return now > stalled+64*1024*1024 })
}

func TestBrowserDeleteFromThePageEndsItsOpenSocketAndTheAgentsOnIt(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	pid := number(openSession(t, p, "s1")["pid"])
	g.call(t, "DELETE", "/v1/connect", callOptions{origin: origin(pageOrigin)}).want(t, 200, nil)
	p.waitClosed(t)
	waitFor(t, "the agent group to end", func() bool { return !groupAlive(pid) })
	if _, err := dial(t, g.port, pageOrigin, ""); err == nil {
		t.Fatal("a page whose grant was taken back got a socket")
	}
}

func TestBrowserAppsRemoveWhileTheDaemonRunsEndsThePagesSocketAndTheAgentsOnIt(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	pid := number(openSession(t, p, "s1")["pid"])
	if code, _, stderr := runCommand(t, g.box, "apps", "remove", pageOrigin); code != 0 {
		t.Fatalf("apps remove: exit code %d: %s", code, stderr)
	}
	p.waitClosed(t)
	waitFor(t, "the agent group to end", func() bool { return !groupAlive(pid) })
	if _, err := dial(t, g.port, pageOrigin, ""); err == nil {
		t.Fatal("a page whose grant was removed got a socket")
	}
	waitFor(t, "the log line", func() bool { return g.d.says(pageOrigin + " was disconnected: ending its pages' sessions") })
}

func TestBrowserStoppingTheDaemonDoesNotWaitForAPageThatKeepsItsSocketOpen(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	openSession(t, p, "s1")
	began := time.Now()
	g.d.signal(syscall.SIGTERM)
	if code := g.d.exitCode(t, 8*time.Second); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if time.Since(began) >= 8*time.Second {
		t.Fatalf("stopping took %s", time.Since(began))
	}
}

func TestBrowserRefusedUpgrades(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	if _, err := dial(t, g.port, otherOrigin, ""); err == nil {
		t.Fatal("an origin without a browser grant got a socket")
	}
	if _, err := dial(t, g.port, pageOrigin, "evil.example"); err == nil {
		t.Fatal("a granted origin with a foreign Host got a socket")
	}
	if _, err := dial(t, g.port, "", ""); err == nil {
		t.Fatal("a request with no Origin got a socket")
	}
}

type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
}

func (s mcpServer) env(name string) string {
	for _, e := range s.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func mcpOf(t *testing.T, opened message) mcpServer {
	t.Helper()
	data, err := json.Marshal(opened["mcp"])
	if err != nil {
		t.Fatal(err)
	}
	var s mcpServer
	if err := json.Unmarshal(data, &s); err != nil || s.Command == "" {
		t.Fatalf("opened has no MCP server: %s (%v)", data, err)
	}
	return s
}

var (
	mcpInit  = message{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": message{"protocolVersion": "2025-06-18", "capabilities": message{}, "clientInfo": message{"name": "agent", "version": "1"}}}
	mcpReady = message{"jsonrpc": "2.0", "method": "notifications/initialized"}
	mcpList  = message{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}
	mcpCall  = message{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": message{"name": "current_page", "arguments": message{}}}
)

// serveTools is the page's own MCP server: one tool, current_page, that says where the page is.
func serveTools(t *testing.T, p *page, id string) {
	t.Helper()
	results := map[string]any{
		"initialize": message{"protocolVersion": "2025-06-18", "capabilities": message{"tools": message{}}, "serverInfo": message{"name": "page", "version": "1"}},
		"tools/list": message{"tools": []message{{"name": "current_page", "description": "Where the page is", "inputSchema": message{"type": "object"}}}},
		"tools/call": message{"content": []message{{"type": "text", "text": "/projects/42"}}},
	}
	p.mu.Lock()
	p.onMessage = func(m message) {
		if m["t"] != "mcp" || m["id"] != id {
			return
		}
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(fmt.Sprint(m["line"])), &req) != nil || req.ID == nil {
			return
		}
		line, _ := json.Marshal(message{"jsonrpc": "2.0", "id": *req.ID, "result": results[req.Method]})
		go func() { _ = p.write(message{"t": "mcp", "id": id, "line": string(line)}) }()
	}
	p.mu.Unlock()
}

// pageBridge is `agentd mcp --page` run by the test, outside any agent, with the environment of `server` (and `extra`).
type pageBridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *syncBuffer
	err    *syncBuffer
	exited chan struct{}
	code   int
}

func startPageBridge(t *testing.T, server mcpServer, extra map[string]string) *pageBridge {
	t.Helper()
	cmd := exec.Command(binaries.agentd, "mcp", "--page")
	cmd.Env = os.Environ()
	for _, e := range server.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	for k, v := range extra {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b := &pageBridge{cmd: cmd, out: &syncBuffer{}, err: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = b.out, b.err
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	b.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() {
		_ = cmd.Wait()
		b.code = cmd.ProcessState.ExitCode()
		close(b.exited)
	}()
	return b
}

func (b *pageBridge) send(t *testing.T, m message) {
	t.Helper()
	data, _ := json.Marshal(m)
	if _, err := b.stdin.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (b *pageBridge) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-b.exited:
		return b.code
	case <-time.After(waitLimit):
		t.Fatalf("the bridge did not exit; stderr: %s", b.err)
		return -1
	}
}

func mcpLine(t *testing.T, p *page, id, what string, pred func(line string) bool) message {
	t.Helper()
	return p.next(t, what, func(m message) bool { return m["t"] == "mcp" && m["id"] == id && pred(fmt.Sprint(m["line"])) })
}

func contains(text string) func(string) bool {
	return func(line string) bool { return strings.Contains(line, text) }
}

func TestBrowserAPagesAgentFindsAgentdMcpOnItsPathInThePagesOwnWrapperDirectory(t *testing.T) {
	g := startBrowserDaemon(t, sh("command -v agentd-mcp; sleep 60"))
	p := mustDial(t, g.port, pageOrigin)
	openSession(t, p, "s1")
	line := p.next(t, "the wrapper path", frameOf("out"))
	if !regexp.MustCompile(`agentd-[^/]+/bin/page/agentd-mcp$`).MatchString(fmt.Sprint(line["data"])) {
		t.Fatalf("the wrapper is at %v", line["data"])
	}
}

func TestBrowserOpenedNamesTheStdioServerToPassToSessionNew(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	// absolute, as the ACP schema asks: the server starts whatever PATH the agent gives its MCP servers
	if !regexp.MustCompile(`^/.+/bin/page/agentd-mcp$`).MatchString(mcp.Command) {
		t.Fatalf("command = %q", mcp.Command)
	}
	if len(mcp.Args) != 0 {
		t.Fatalf("args = %v", mcp.Args)
	}
	if len(mcp.Env) != 2 || mcp.Env[0].Name != "AGENTD_MCP_SOCKET" || mcp.Env[1].Name != "AGENTD_MCP_TOKEN" {
		t.Fatalf("env = %+v", mcp.Env)
	}
	socket := mcp.env("AGENTD_MCP_SOCKET")
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket (%v)", socket, err)
	}
	dir, err := os.Stat(filepath.Dir(socket))
	if err != nil || !regexp.MustCompile(`agentd-[0-9A-Za-z]{6}$`).MatchString(filepath.Dir(socket)) || dir.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s: mode %v (%v): another OS user may reach the socket", filepath.Dir(socket), dir.Mode(), err)
	}
	token, err := base64.RawURLEncoding.DecodeString(mcp.env("AGENTD_MCP_TOKEN"))
	if err != nil || len(token) < 16 {
		t.Fatalf("token %q: %d bytes (%v)", mcp.env("AGENTD_MCP_TOKEN"), len(token), err)
	}
}

func TestBrowserTheAgentsToolCallsReachThePagesServerAndItsAnswersReachTheAgent(t *testing.T) {
	g := startBrowserDaemon(t, binaries.helper+" mcp-agent")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	serveTools(t, p, "s1")
	start, _ := json.Marshal(message{"start": mcp})
	p.send(t, message{"t": "write", "id": "s1", "line": string(start)})
	for _, m := range []message{mcpInit, mcpReady, mcpList, mcpCall} {
		line, _ := json.Marshal(m)
		p.send(t, message{"t": "write", "id": "s1", "line": string(line)})
	}
	answer := func(id int) string {
		m := p.next(t, fmt.Sprintf("the agent's answer %d", id), func(m message) bool {
			return frameOf("out")(m) && strings.Contains(fmt.Sprint(m["data"]), fmt.Sprintf(`"id":%d,`, id))
		})
		return fmt.Sprint(m["data"])
	}
	for id, want := range map[int]string{1: `"name":"page"`, 2: `"name":"current_page"`, 3: `"text":"/projects/42"`} {
		if got := answer(id); !strings.Contains(got, want) {
			t.Fatalf("answer %d = %s, want %s", id, got, want)
		}
	}
	mcpLine(t, p, "s1", "the notification", contains("notifications/initialized"))
}

func TestBrowserAWrongTokenIsRefusedAndTheBridgeSaysWhyAndExits1(t *testing.T) {
	g := startBrowserDaemon(t, binaries.helper+" mcp-agent")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	for i := range mcp.Env {
		if mcp.Env[i].Name == "AGENTD_MCP_TOKEN" {
			mcp.Env[i].Value = "guessed"
		}
	}
	start, _ := json.Marshal(message{"start": mcp})
	p.send(t, message{"t": "write", "id": "s1", "line": string(start)})
	p.next(t, "the bridge's exit", func(m message) bool { return frameOf("out")(m) && m["data"] == `{"bridgeExit":1}` })
	var errs []string
	for _, m := range p.messages() {
		if frameOf("err")(m) {
			errs = append(errs, fmt.Sprint(m["data"]))
		}
		if m["t"] == "mcp" {
			t.Fatal("a line reached the page through a refused bridge")
		}
	}
	if !strings.Contains(strings.Join(errs, ""), "agentd-mcp: the daemon refused the bridge (no open session has this token") {
		t.Fatalf("stderr: %q", errs)
	}
}

func TestBrowserABridgeWithoutItsEnvironmentSaysWhatItNeedsAndExits1(t *testing.T) {
	startBrowserDaemon(t, "cat")
	b := startPageBridge(t, mcpServer{}, map[string]string{"AGENTD_MCP_SOCKET": "", "AGENTD_MCP_TOKEN": ""})
	if code := b.exitCode(t); code != 1 || !strings.Contains(b.err.String(), "AGENTD_MCP_SOCKET and AGENTD_MCP_TOKEN") {
		t.Fatalf("exit code %d; stderr: %s", code, b.err)
	}
}

func TestBrowserClosingThePageEndsItsBridgeWhichExits1(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	b := startPageBridge(t, mcp, nil)
	b.send(t, mcpInit)
	mcpLine(t, p, "s1", "initialize on the page", contains(`"initialize"`))
	_ = p.conn.Close()
	if code := b.exitCode(t); code != 1 || !strings.Contains(b.err.String(), "agentd-mcp: the daemon ended the bridge") {
		t.Fatalf("exit code %d; stderr: %s", code, b.err)
	}
}

func TestBrowserClosingTheSessionEndsItsBridgeAndItsTokenOpensNoOther(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	b := startPageBridge(t, mcp, nil)
	b.send(t, mcpInit)
	mcpLine(t, p, "s1", "initialize on the page", contains(`"initialize"`))
	p.send(t, message{"t": "close", "id": "s1"})
	if code := b.exitCode(t); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	late := startPageBridge(t, mcp, nil)
	if code := late.exitCode(t); code != 1 || !strings.Contains(late.err.String(), "refused the bridge") {
		t.Fatalf("exit code %d; stderr: %s", code, late.err)
	}
}

func TestBrowserAPageLineForASessionWithNoBridgeIsAnErrorThatSaysSo(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	openSession(t, p, "s1")
	p.send(t, message{"t": "mcp", "id": "s1", "line": "{}"})
	e := p.next(t, "the error", func(m message) bool { return m["t"] == "error" && m["id"] == "s1" })
	if !strings.Contains(fmt.Sprint(e["message"]), "no MCP bridge") {
		t.Fatalf("error = %v", e)
	}
}

func TestBrowserAnotherPagesTokenReachesThatPageOnlyEvenWithTheSameSessionID(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	pa := mustDial(t, g.port, pageOrigin)
	mcpOf(t, openSession(t, pa, "s1"))
	pb := mustDial(t, g.port, pageOrigin)
	b := mcpOf(t, openSession(t, pb, "s1"))
	bridge := startPageBridge(t, b, nil)
	bridge.send(t, mcpInit)
	mcpLine(t, pb, "s1", "initialize on page b", contains(`"initialize"`))
	pa.send(t, message{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":1,"result":{"from":"a"}}`})
	pb.send(t, message{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":1,"result":{"from":"b"}}`})
	waitFor(t, "b's answer on the bridge", func() bool { return strings.Contains(bridge.out.String(), `"from":"b"`) })
	if strings.Contains(bridge.out.String(), `"from":"a"`) {
		t.Fatal("page a's answer reached page b's bridge")
	}
	for _, m := range pa.messages() {
		if m["t"] == "mcp" {
			t.Fatal("page b's bridge reached page a")
		}
	}
}

func TestBrowserABridgeStartedAgainForTheSessionReplacesTheOlderOne(t *testing.T) {
	g := startBrowserDaemon(t, "cat")
	p := mustDial(t, g.port, pageOrigin)
	mcp := mcpOf(t, openSession(t, p, "s1"))
	older := startPageBridge(t, mcp, nil)
	older.send(t, mcpInit)
	mcpLine(t, p, "s1", "the older initialize", contains(`"initialize"`))
	newer := startPageBridge(t, mcp, nil)
	if code := older.exitCode(t); code != 1 {
		t.Fatalf("the older bridge exited %d, want 1", code)
	}
	again := mcpInit
	again = message{"jsonrpc": again["jsonrpc"], "id": 7, "method": again["method"], "params": again["params"]}
	newer.send(t, again)
	mcpLine(t, p, "s1", "the newer initialize", contains(`"id":7`))
	p.send(t, message{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":7,"result":{}}`})
	waitFor(t, "the answer on the newer bridge", func() bool { return strings.Contains(newer.out.String(), `"id":7`) })
}
