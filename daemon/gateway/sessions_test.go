package gateway

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
)

type noState struct{}

func (noState) Add(int)    {}
func (noState) Remove(int) {}

func noAdapter(agents.AdapterName) (agents.AdapterState, bool) { return agents.AdapterState{}, false }

// shortDir is a temporary directory with a short path: a socket path is limited to about 100 bytes, and the
// temporary directory of a test is longer.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// agentScript writes an executable shell script and returns its path: the agent of a rig that runs it.
func agentScript(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(shortDir(t), "agent")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

// rig is a gateway whose page is granted as a browser origin, with real sessions over a fake "claude".
type rig struct {
	port     int
	gw       *Gateway
	sessions *BrowserSessions
	relay    *pagetools.McpRelay
	world    *world
	bridge   string // the pages' agentd-mcp wrapper, as `opened` names it
	dir      string // the agents' working directory
}

// startRig runs `agent` (a command line) as the "claude" agent of every page session.
func startRig(t *testing.T, agent string) *rig {
	t.Helper()
	dir := shortDir(t)
	binDir := filepath.Join(dir, "bin", "page")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relay, err := pagetools.Listen(filepath.Join(dir, "mcp.sock"))
	if err != nil {
		t.Fatal(err)
	}
	env := home.Env{"PATH": os.Getenv("PATH"), agents.SeamVariable("claude"): agent}
	find := func(program string) (string, bool) { return home.FindProgram(program, env) }
	r := &rig{relay: relay, world: newWorld(), dir: dir, bridge: filepath.Join(binDir, "agentd-mcp")}
	r.sessions = NewBrowserSessions(SessionOptions{
		Fleet: agents.FleetOptions{Cwd: dir, BinDir: binDir, Env: env, Adapter: noAdapter, State: noState{}},
		Hello: func() pagetools.Hello {
			return pagetools.NewHello("test", agents.AllStatuses(env, find, noAdapter))
		},
		Relay:         relay,
		BridgeCommand: r.bridge,
	})
	r.world.reg = r.world.reg.AddBrowser(page, time.Now(), nil)
	r.world.sessions = r.sessions
	deps := r.world.deps()
	deps.Upgrade = r.sessions.Upgrade
	r.gw = startGateway(t, deps, nil)
	r.port = r.gw.Port
	t.Cleanup(func() { r.sessions.Shutdown(2 * time.Second) })
	return r
}

func groupAlive(pid int) bool { return syscall.Kill(-pid, 0) == nil }

// pageConn is a page's socket in a test: it collects what the daemon sends.
type pageConn struct {
	t   *testing.T
	ws  *websocket.Conn
	wmu sync.Mutex // one write at a time

	mu   sync.Mutex
	msgs []map[string]any
	// closed has the close code when the daemon closes the socket (-1 for any other end).
	closed chan int
}

func dialPage(t *testing.T, port int, origin, host string) (*pageConn, error) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if host != "" {
		header.Set("Host", host)
	}
	d := websocket.Dialer{HandshakeTimeout: wait}
	ws, res, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/v1/session", header)
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	p := &pageConn{t: t, ws: ws, closed: make(chan int, 1)}
	t.Cleanup(func() { _ = ws.Close() })
	go p.read()
	return p, nil
}

func mustDial(t *testing.T, port int) *pageConn {
	t.Helper()
	p, err := dialPage(t, port, page, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *pageConn) read() {
	for {
		_, data, err := p.ws.ReadMessage()
		if err != nil {
			code := -1
			if ce, ok := err.(*websocket.CloseError); ok {
				code = ce.Code
			}
			p.closed <- code
			return
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			p.t.Errorf("the daemon sent %q", data)
			continue
		}
		p.mu.Lock()
		p.msgs = append(p.msgs, m)
		p.mu.Unlock()
	}
}

func (p *pageConn) send(m any) {
	p.t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		p.t.Fatal(err)
	}
	p.sendRaw(b)
}

func (p *pageConn) sendRaw(b []byte) {
	p.t.Helper()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := p.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pageConn) all() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.msgs)
}

func (p *pageConn) find(pred func(map[string]any) bool) (map[string]any, bool) {
	for _, m := range p.all() {
		if pred(m) {
			return m, true
		}
	}
	return nil, false
}

// next waits for a message that pred accepts.
func (p *pageConn) next(what string, pred func(map[string]any) bool) map[string]any {
	p.t.Helper()
	var found map[string]any
	waitFor(p.t, what, wait, func() bool {
		var ok bool
		found, ok = p.find(pred)
		return ok
	})
	return found
}

func kind(t string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == t }
}

func frameOf(k string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == "frame" && m["kind"] == k }
}

// open opens session id on the "claude" agent and returns what `opened` said.
func (p *pageConn) open(id string) map[string]any {
	p.t.Helper()
	p.send(map[string]any{"t": "open", "id": id, "agent": "claude"})
	opened := p.next("opened "+id, func(m map[string]any) bool { return m["t"] == "opened" && m["id"] == id })
	pid := int(opened["pid"].(float64))
	p.t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	return opened
}

func pidOf(opened map[string]any) int { return int(opened["pid"].(float64)) }

func TestAGrantedOriginGetsHelloOpensAnAgentAndTalksToItThroughFrames(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	hello := p.next("hello", kind("hello"))
	if hello["version"] != "test" {
		t.Fatalf("hello = %v", hello)
	}
	var claude map[string]any
	for _, a := range hello["agents"].([]any) {
		if a.(map[string]any)["name"] == "claude" {
			claude = a.(map[string]any)
		}
	}
	if fmt.Sprint(claude) != "map[name:claude ready:true reason: title:Claude Code]" {
		t.Fatalf("claude = %v", claude)
	}
	opened := p.open("s1")
	if opened["id"] != "s1" || opened["cwd"] != r.dir {
		t.Fatalf("opened = %v", opened)
	}
	p.send(map[string]any{"t": "write", "id": "s1", "line": `{"hello":"agent"}`})
	f := p.next("the echoed line", frameOf("out"))
	if f["id"] != "s1" || f["data"] != `{"hello":"agent"}` {
		t.Fatalf("frame = %v", f)
	}
	if r.sessions.Size() != 1 || !slices.Equal(r.sessions.Origins(), []string{page}) {
		t.Fatalf("size %d, origins %v", r.sessions.Size(), r.sessions.Origins())
	}
}

func TestTheOpenedMessageComesBeforeTheFirstFrameOfTheSession(t *testing.T) {
	r := startRig(t, agentScript(t, "echo first; sleep 60"))
	for i := range 5 {
		p := mustDial(t, r.port)
		id := "s" + strconv.Itoa(i)
		p.open(id)
		p.next("the first line", frameOf("out"))
		var order []string
		for _, m := range p.all() {
			order = append(order, m["t"].(string))
		}
		if !slices.Equal(order, []string{"hello", "opened", "frame"}) {
			t.Fatalf("messages in order: %v", order)
		}
	}
}

// numbered is the i-th line of a test that checks order: a page writes JSON-RPC messages.
func numbered(i int) string {
	return `{"jsonrpc":"2.0","method":"tick","params":{"n":` + strconv.Itoa(i) + `}}`
}

func TestLinesWrittenByAPageReachTheAgentInOrder(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.open("s1")
	const n = 300
	for i := range n {
		p.send(map[string]any{"t": "write", "id": "s1", "line": numbered(i)})
	}
	waitFor(t, "every echo", wait, func() bool {
		count := 0
		for _, m := range p.all() {
			if frameOf("out")(m) {
				count++
			}
		}
		return count == n
	})
	i := 0
	for _, m := range p.all() {
		if frameOf("out")(m) {
			if m["data"] != numbered(i) {
				t.Fatalf("echo %d is %v", i, m["data"])
			}
			i++
		}
	}
}

func TestAnUnknownAgentIsAnErrorMessageNotADeadSocket(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.send(map[string]any{"t": "open", "id": "s1", "agent": "rm -rf"})
	e := p.next("error", kind("error"))
	if e["id"] != "s1" || !strings.Contains(e["message"].(string), `unknown agent "rm -rf"`) {
		t.Fatalf("error = %v", e)
	}
	p.send(map[string]any{"t": "write", "id": "nope", "line": `{"jsonrpc":"2.0","method":"x"}`})
	e = p.next("not running", func(m map[string]any) bool { return m["t"] == "error" && m["id"] == "nope" })
	if e["message"] != "session nope is not running" {
		t.Fatalf("error = %v", e)
	}
}

func TestClosingTheSocketMidTurnEndsTheAgent(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	pid := pidOf(p.open("s1"))
	if !groupAlive(pid) {
		t.Fatal("the agent is not running")
	}
	_ = p.ws.Close()
	waitFor(t, "the agent group to end", 8*time.Second, func() bool { return !groupAlive(pid) })
	waitFor(t, "the socket to be forgotten", wait, func() bool { return len(r.sessions.Origins()) == 0 && r.sessions.Size() == 0 })
}

func TestASixthSessionOnOneSocketIsRefused(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	for i := 1; i <= MaxSessionsPerSocket; i++ {
		p.open("s" + strconv.Itoa(i))
	}
	p.send(map[string]any{"t": "open", "id": "s6", "agent": "claude"})
	e := p.next("error for s6", func(m map[string]any) bool { return m["t"] == "error" && m["id"] == "s6" })
	if !strings.Contains(e["message"].(string), "at most 5") {
		t.Fatalf("error = %v", e)
	}
}

func TestAMessageThatIsNotOneOfTheFourIsAnErrorAndChangesNothing(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.sendRaw([]byte("not json"))
	p.send(map[string]any{"t": "launch", "id": "s1"})
	waitFor(t, "two errors", wait, func() bool {
		n := 0
		for _, m := range p.all() {
			if m["t"] == "error" && m["id"] == "" {
				n++
			}
		}
		return n == 2
	})
	if r.sessions.Size() != 0 {
		t.Fatal("a session opened")
	}
}

func TestAMessageFromThePageAboveTheLimitClosesItsSocketAsTooBig(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.next("hello", kind("hello"))
	line, _ := json.Marshal(map[string]any{"t": "write", "id": "s1", "line": strings.Repeat("x", 65*1024*1024)})
	go func() { _ = p.ws.WriteMessage(websocket.TextMessage, line) }()
	select {
	case code := <-p.closed:
		if code != websocket.CloseMessageTooBig {
			t.Fatalf("close code %d, want 1009", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the socket stayed open")
	}
}

func TestAnAgentLinePastTheCapEndsTheSessionWithAnErrorThatSaysWhy(t *testing.T) {
	r := startRig(t, agentScript(t, `head -c 34603008 /dev/zero | tr '\0' x; sleep 60`))
	p := mustDial(t, r.port)
	p.open("s1")
	e := p.next("the error frame", frameOf("err"))
	if !strings.Contains(e["data"].(string), "longer than 32 MiB") {
		t.Fatalf("frame = %v", e)
	}
	p.next("the exit frame", frameOf("exit"))
	if _, has := p.find(frameOf("out")); has {
		t.Fatal("the line was passed on")
	}
}

// stalledPage dials by hand: after the handshake it sends `messages` and then reads nothing until told to.
func stalledPage(t *testing.T, port int, messages ...map[string]any) *bufio.Reader {
	t.Helper()
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET /v1/session HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nOrigin: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", port, page)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	answer, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(answer, "HTTP/1.1 101") {
		t.Fatalf("handshake answer %q, %v", answer, err)
	}
	for line, err := reader.ReadString('\n'); line != "\r\n"; line, err = reader.ReadString('\n') {
		if err != nil {
			t.Fatal(err)
		}
	}
	// A masked text frame with an all-zero mask: the payload goes as it is. Each message here is under 126 bytes.
	for _, m := range messages {
		payload, _ := json.Marshal(m)
		frame := append([]byte{0x81, 0x80 | byte(len(payload)), 0, 0, 0, 0}, payload...)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	return reader
}

// buffered is what the daemon holds for the only open socket, waiting to go out.
func (r *rig) buffered() int {
	entries := r.sessions.entries()
	if len(entries) != 1 {
		return -1
	}
	p := entries[0].page
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffered
}

func TestAPageThatStopsReadingStopsItsAgentsOutputInsteadOfFillingTheDaemonAndResumesIt(t *testing.T) {
	// An agent that writes 64 KiB lines as fast as its stdout takes them.
	r := startRig(t, agentScript(t, "exec yes "+strings.Repeat("x", 65535)))
	reader := stalledPage(t, r.port, map[string]any{"t": "open", "id": "s1", "agent": "claude"})
	waitFor(t, "the agent", wait, func() bool { return r.sessions.Size() == 1 })
	t.Cleanup(func() { r.sessions.EndOrigin(page) })
	waitFor(t, "the output to pile up to the pause", 20*time.Second, func() bool { return r.buffered() > pauseBufferedBytes })
	time.Sleep(2 * time.Second)
	stalled := r.buffered()
	time.Sleep(time.Second)
	// What the daemon holds: its socket's buffer (8 MiB) and the kernel's; without a pause it is hundreds of MB by now.
	if stalled > 64*1024*1024 {
		t.Fatalf("the daemon holds %d MiB for a page that does not read", stalled/1024/1024)
	}
	if grown := r.buffered() - stalled; grown > 1024*1024 {
		t.Fatalf("the output only slowed: %d bytes more in a second", grown)
	}
	received := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, io.LimitReader(reader, 96*1024*1024))
		received <- n
	}()
	select {
	case n := <-received:
		if n < 96*1024*1024 {
			t.Fatalf("the page read %d bytes and the output stopped", n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the agent did not write again once the page read")
	}
}

func TestDeleteFromThePageEndsItsOpenSocketAndTheAgentsOnIt(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	pid := pidOf(p.open("s1"))
	wantReply(t, call(t, r.port, "DELETE", "/v1/connect", callOpts{origin: page}), 200, nil)
	select {
	case <-p.closed:
	case <-time.After(wait):
		t.Fatal("the socket stayed open")
	}
	waitFor(t, "the agent group to end", 8*time.Second, func() bool { return !groupAlive(pid) })
	if _, err := dialPage(t, r.port, page, ""); err == nil {
		t.Fatal("a page whose grant was taken back got a socket")
	}
}

func TestEndOriginClosesTheSocketsOfThatOriginOnly(t *testing.T) {
	r := startRig(t, "cat")
	r.world.reg = r.world.reg.AddBrowser(other, time.Now(), nil)
	a := mustDial(t, r.port)
	b, err := dialPage(t, r.port, other, "")
	if err != nil {
		t.Fatal(err)
	}
	a.next("hello", kind("hello"))
	b.next("hello", kind("hello"))
	if got := r.sessions.Origins(); len(got) != 2 {
		t.Fatalf("origins = %v", got)
	}
	r.sessions.EndOrigin(other)
	select {
	case <-b.closed:
	case <-time.After(wait):
		t.Fatal("the socket of the origin stayed open")
	}
	select {
	case <-a.closed:
		t.Fatal("another origin's socket was closed")
	case <-time.After(300 * time.Millisecond):
	}
	waitFor(t, "the origin to leave", wait, func() bool { return slices.Equal(r.sessions.Origins(), []string{page}) })
}

func TestStoppingTheDaemonDoesNotWaitForAPageThatKeepsItsSocketOpen(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	pid := pidOf(p.open("s1"))
	began := time.Now()
	r.sessions.Shutdown(5 * time.Second)
	_ = r.gw.Close()
	if took := time.Since(began); took > 8*time.Second {
		t.Fatalf("took %v", took)
	}
	select {
	case <-p.closed:
	case <-time.After(wait):
		t.Fatal("the page's socket stayed open")
	}
	waitFor(t, "the agent group to end", 8*time.Second, func() bool { return !groupAlive(pid) })
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(r.port), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the gateway still listens")
	}
}

func TestNoSocketOpensAfterShutdown(t *testing.T) {
	r := startRig(t, "cat")
	r.sessions.Shutdown(time.Second)
	if _, err := dialPage(t, r.port, page, ""); err == nil {
		t.Fatal("a socket opened after shutdown")
	}
}

func TestARefusedUpgradeNeverGetsASocket(t *testing.T) {
	r := startRig(t, "cat")
	if _, err := dialPage(t, r.port, other, ""); err == nil {
		t.Error("an origin without a browser grant got a socket")
	}
	if _, err := dialPage(t, r.port, page, "evil.example"); err == nil {
		t.Error("a granted origin with a foreign Host got a socket")
	}
	if _, err := dialPage(t, r.port, "", ""); err == nil {
		t.Error("no Origin got a socket")
	}
	if _, err := dialPage(t, r.port, "null", ""); err == nil {
		t.Error("a null Origin got a socket")
	}
	d := websocket.Dialer{HandshakeTimeout: wait}
	if _, res, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(r.port)+"/v1/other", http.Header{"Origin": {page}}); err == nil {
		t.Error("another path got a socket")
	} else if res != nil {
		_ = res.Body.Close()
	}
}

func TestAnUpgradeWithoutAnUpgradeHandlerIsRefused(t *testing.T) {
	w := newWorld()
	w.reg = w.reg.AddBrowser(page, time.Now(), nil)
	g := startGateway(t, w.deps(), nil)
	if err := dialSession(t, g.Port); err == nil {
		t.Fatal("got a socket")
	}
}

// ---- a page's MCP server ----

type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
}

func mcpOf(t *testing.T, opened map[string]any) mcpServer {
	t.Helper()
	raw, err := json.Marshal(opened["mcp"])
	if err != nil {
		t.Fatal(err)
	}
	var s mcpServer
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s mcpServer) env(name string) string {
	for _, e := range s.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// fakeBridge is `agentd-mcp --page` as a test plays it: the hello with a token on the daemon's socket, then lines.
type fakeBridge struct {
	conn   net.Conn
	reader *bufio.Reader
	answer pagetools.BridgeAnswer
}

// dialBridge connects to the relay of `s` and says hello with `token` (the server's own when empty).
func dialBridge(t *testing.T, s mcpServer, token string) *fakeBridge {
	t.Helper()
	conn, err := net.Dial("unix", s.env(pagetools.McpSocketEnv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if token == "" {
		token = s.env(pagetools.McpTokenEnv)
	}
	if _, err := io.WriteString(conn, pagetools.BridgeHello(token)+"\n"); err != nil {
		t.Fatal(err)
	}
	b := &fakeBridge{conn: conn, reader: bufio.NewReader(conn)}
	line, err := b.line()
	if err != nil {
		t.Fatalf("no answer to the hello: %v", err)
	}
	b.answer = pagetools.ParseBridgeAnswer(line)
	return b
}

func (b *fakeBridge) line() (string, error) {
	_ = b.conn.SetReadDeadline(time.Now().Add(wait))
	line, err := b.reader.ReadString('\n')
	return strings.TrimSuffix(line, "\n"), err
}

func (b *fakeBridge) send(t *testing.T, m any) {
	t.Helper()
	raw, _ := json.Marshal(m)
	if _, err := io.WriteString(b.conn, string(raw)+"\n"); err != nil {
		t.Fatal(err)
	}
}

// ended is whether the daemon closed the bridge's connection.
func (b *fakeBridge) ended() bool {
	_, err := b.line()
	return err == io.EOF
}

func mcpLine(p *pageConn, id, what string, pred func(line string) bool) map[string]any {
	return p.next(what, func(m map[string]any) bool { return m["t"] == "mcp" && m["id"] == id && pred(m["line"].(string)) })
}

func contains(s string) func(string) bool {
	return func(line string) bool { return strings.Contains(line, s) }
}

var (
	mcpInit = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}}
	mcpList = map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}
	mcpCall = map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "current_page"}}
)

func TestAPagesAgentFindsAgentdMcpOnItsPathInThePagesOwnWrapperDirectory(t *testing.T) {
	r := startRig(t, agentScript(t, "command -v agentd-mcp; sleep 60"))
	if err := os.WriteFile(r.bridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustDial(t, r.port)
	p.open("s1")
	f := p.next("the wrapper path", frameOf("out"))
	if f["data"] != r.bridge {
		t.Fatalf("agentd-mcp is %v, want %s", f["data"], r.bridge)
	}
}

func TestOpenedNamesTheStdioServerToPassToSessionNew(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	// Absolute, as the ACP schema asks; the socket and the token are in env, which other OS users cannot read.
	if !filepath.IsAbs(s.Command) || s.Command != r.bridge || len(s.Args) != 0 {
		t.Fatalf("server = %+v", s)
	}
	var names []string
	for _, e := range s.Env {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"AGENTD_MCP_SOCKET", "AGENTD_MCP_TOKEN"}) {
		t.Fatalf("env = %v", names)
	}
	if s.env("AGENTD_MCP_SOCKET") != r.relay.Path() {
		t.Fatalf("socket = %s", s.env("AGENTD_MCP_SOCKET"))
	}
	if st, err := os.Stat(r.relay.Path()); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the relay path is no socket: %v", err)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(s.env("AGENTD_MCP_TOKEN")); err != nil || len(raw) < 16 {
		t.Fatalf("token %q: %v", s.env("AGENTD_MCP_TOKEN"), err)
	}
	// The wire form carries args as [], never null.
	raw, _ := json.Marshal(p.all())
	if !strings.Contains(string(raw), `"args":[]`) {
		t.Fatalf("messages %s", raw)
	}
}

func TestOpenedNamesNoServerWithoutARelay(t *testing.T) {
	r := startRig(t, "cat")
	s := NewBrowserSessions(SessionOptions{Fleet: r.sessions.o.Fleet, Hello: r.sessions.o.Hello})
	deps := r.world.deps()
	deps.Upgrade = s.Upgrade
	g := startGateway(t, deps, nil)
	t.Cleanup(func() { s.Shutdown(time.Second) })
	p := mustDial(t, g.Port)
	opened := p.open("s1")
	if _, has := opened["mcp"]; has {
		t.Fatalf("opened = %v", opened)
	}
}

func TestTheAgentsMcpLinesReachThePagesServerAndItsAnswersReachTheAgent(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	results := map[string]any{
		"initialize": map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "page"}},
		"tools/list": map[string]any{"tools": []any{map[string]any{"name": "current_page"}}},
		"tools/call": map[string]any{"content": []any{map[string]any{"type": "text", "text": "/projects/42"}}},
	}
	// The page's own MCP server: it answers each request it gets.
	go func() {
		for seen := 0; ; {
			msgs := p.all()
			for ; seen < len(msgs); seen++ {
				if msgs[seen]["t"] != "mcp" {
					continue
				}
				var req struct {
					ID     *int   `json:"id"`
					Method string `json:"method"`
				}
				_ = json.Unmarshal([]byte(msgs[seen]["line"].(string)), &req)
				if req.ID == nil {
					continue
				}
				answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": results[req.Method]})
				p.wmu.Lock()
				_ = p.ws.WriteJSON(map[string]any{"t": "mcp", "id": "s1", "line": string(answer)})
				p.wmu.Unlock()
			}
			select {
			case <-time.After(5 * time.Millisecond):
			case <-r.sessions.entries()[0].page.done:
				return
			}
		}
	}()
	b := dialBridge(t, s, "")
	if !b.answer.OK {
		t.Fatalf("the bridge was refused: %s", b.answer.Message)
	}
	for _, m := range []any{mcpInit, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, mcpList, mcpCall} {
		b.send(t, m)
	}
	for _, want := range []string{`"name":"page"`, `"name":"current_page"`, `/projects/42`} {
		line, err := b.line()
		if err != nil || !strings.Contains(line, want) {
			t.Fatalf("the bridge read %q, %v; want %s", line, err, want)
		}
	}
	mcpLine(p, "s1", "the notification", contains("notifications/initialized"))
}

func TestAWrongTokenIsRefusedAndTheBridgeSaysWhy(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	b := dialBridge(t, s, "guessed")
	if b.answer.OK || !strings.Contains(b.answer.Message, "no open session has this token") {
		t.Fatalf("answer = %+v", b.answer)
	}
	if _, has := p.find(kind("mcp")); has {
		t.Fatal("the page heard from a bridge that was refused")
	}
}

func TestClosingThePageEndsItsBridge(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	b := dialBridge(t, s, "")
	b.send(t, mcpInit)
	mcpLine(p, "s1", "initialize on the page", contains(`"initialize"`))
	_ = p.ws.Close()
	if !b.ended() {
		t.Fatal("the bridge stayed connected")
	}
}

func TestClosingTheSessionEndsItsBridgeAndItsTokenOpensNoOther(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	b := dialBridge(t, s, "")
	b.send(t, mcpInit)
	mcpLine(p, "s1", "initialize on the page", contains(`"initialize"`))
	p.send(map[string]any{"t": "close", "id": "s1"})
	if !b.ended() {
		t.Fatal("the bridge stayed connected")
	}
	late := dialBridge(t, s, "")
	if late.answer.OK || !strings.Contains(late.answer.Message, "no open session") {
		t.Fatalf("a late bridge: %+v", late.answer)
	}
}

func TestAPageLineForASessionWithNoBridgeIsAnErrorThatSaysSo(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.open("s1")
	p.send(map[string]any{"t": "mcp", "id": "s1", "line": "{}"})
	e := p.next("the error", func(m map[string]any) bool { return m["t"] == "error" && m["id"] == "s1" })
	if !strings.Contains(e["message"].(string), "no MCP bridge") {
		t.Fatalf("error = %v", e)
	}
	p.send(map[string]any{"t": "mcp", "id": "nobody", "line": "{}"})
	p.next("the error for no session", func(m map[string]any) bool { return m["t"] == "error" && m["id"] == "nobody" })
}

func TestAnotherPagesTokenReachesThatPageOnlyEvenWithTheSameSessionId(t *testing.T) {
	r := startRig(t, "cat")
	a := mustDial(t, r.port)
	b := mustDial(t, r.port)
	a.open("s1")
	sb := mcpOf(t, b.open("s1"))
	bridge := dialBridge(t, sb, "")
	bridge.send(t, mcpInit)
	mcpLine(b, "s1", "initialize on page b", contains(`"initialize"`))
	a.send(map[string]any{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":1,"result":{"from":"a"}}`})
	b.send(map[string]any{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":1,"result":{"from":"b"}}`})
	line, err := bridge.line()
	if err != nil || !strings.Contains(line, `"from":"b"`) {
		t.Fatalf("the bridge read %q, %v", line, err)
	}
	if _, has := a.find(kind("mcp")); has {
		t.Fatal("page a heard from page b's bridge")
	}
}

func TestABridgeStartedAgainForTheSessionReplacesTheOlderOne(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	s := mcpOf(t, p.open("s1"))
	older := dialBridge(t, s, "")
	older.send(t, mcpInit)
	mcpLine(p, "s1", "the older initialize", contains(`"initialize"`))
	newer := dialBridge(t, s, "")
	if !older.ended() {
		t.Fatal("the older bridge stayed connected")
	}
	newer.send(t, map[string]any{"jsonrpc": "2.0", "id": 7, "method": "initialize"})
	mcpLine(p, "s1", "the newer initialize", contains(`"id":7`))
	p.send(map[string]any{"t": "mcp", "id": "s1", "line": `{"jsonrpc":"2.0","id":7,"result":{}}`})
	if line, err := newer.line(); err != nil || !strings.Contains(line, `"id":7`) {
		t.Fatalf("the newer bridge read %q, %v", line, err)
	}
}

func TestAPageIsKeptToTheFoldersItWasGranted(t *testing.T) {
	r := startRig(t, "cat")
	allowed, elsewhere := t.TempDir(), t.TempDir()
	r.sessions.o.Folders = func(origin string) ([]string, error) {
		if origin != page {
			t.Errorf("folders asked for %q, want the page's origin", origin)
		}
		return []string{allowed}, nil
	}
	p := mustDial(t, r.port)
	if opened := p.open("s1"); opened["cwd"] != allowed {
		t.Fatalf("the agent started in %v, want the granted folder %s", opened["cwd"], allowed)
	}
	newSession := func(id int, cwd string) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/new", "params": map[string]any{"cwd": cwd}})
		return string(b)
	}
	p.send(map[string]any{"t": "write", "id": "s1", "line": newSession(1, filepath.Join(allowed, "project"))})
	p.send(map[string]any{"t": "write", "id": "s1", "line": newSession(2, elsewhere)})
	waitFor(t, "an echo and an error", wait, func() bool {
		count := 0
		for _, m := range p.all() {
			if frameOf("out")(m) {
				count++
			}
		}
		return count == 2
	})
	var echoed, refused string
	for _, m := range p.all() {
		if !frameOf("out")(m) {
			continue
		}
		if data, _ := m["data"].(string); strings.Contains(data, `"method"`) {
			echoed = data
		} else {
			refused = data
		}
	}
	if !strings.Contains(echoed, `"id":1`) {
		t.Errorf("the request inside the folder should reach the agent, got %q", echoed)
	}
	if !strings.Contains(refused, `"id":2`) || !strings.Contains(refused, "outside the folders you allowed") {
		t.Errorf("the request outside should be answered with an error, got %q", refused)
	}
}
