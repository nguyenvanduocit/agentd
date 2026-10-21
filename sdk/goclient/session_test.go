package goclient_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

const wait = 5 * time.Second

type msg = map[string]any

// fakeDaemon is a daemon's /v1/session as a test plays it: it keeps what the app sent, and says what the test tells it.
type fakeDaemon struct {
	t      *testing.T
	srv    *httptest.Server
	Daemon goclient.Daemon

	conns   chan *websocket.Conn
	conn    *websocket.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	sent    []msg
	origin  string
	onsend  func(msg)
	gotSome chan struct{}
}

// startFakeDaemon serves one socket at a time on /v1/session.
func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{t: t, conns: make(chan *websocket.Conn, 4), gotSome: make(chan struct{}, 1000)}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/session" {
			http.NotFound(w, r)
			return
		}
		d.mu.Lock()
		d.origin = r.Header.Get("Origin")
		d.mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		d.conns <- conn
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			d.mu.Lock()
			d.sent = append(d.sent, m)
			onsend := d.onsend
			d.mu.Unlock()
			d.gotSome <- struct{}{}
			if onsend != nil {
				onsend(m)
			}
		}
	}))
	t.Cleanup(d.srv.Close)
	d.Daemon = goclient.Daemon{Port: d.srv.Listener.Addr().(*net.TCPAddr).Port}
	return d
}

// accept is the socket the app opened.
func (d *fakeDaemon) accept() {
	d.t.Helper()
	select {
	case d.conn = <-d.conns:
	case <-time.After(wait):
		d.t.Fatal("the app opened no socket")
	}
}

func (d *fakeDaemon) say(m msg) {
	d.t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		d.t.Fatal(err)
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if err := d.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		d.t.Fatalf("say %v: %v", m, err)
	}
}

func (d *fakeDaemon) messages() []msg {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.sent)
}

// nextSent waits for the n-th message (from 0) the app sent.
func (d *fakeDaemon) nextSent(n int) msg {
	d.t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if all := d.messages(); len(all) > n {
			return all[n]
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.t.Fatalf("the app sent %d messages, want %d", len(d.messages()), n+1)
	return nil
}

func (d *fakeDaemon) sentOf(kind string) []string {
	var lines []string
	for _, m := range d.messages() {
		if m["t"] == kind {
			lines = append(lines, fmt.Sprint(m["line"]))
		}
	}
	return lines
}

// waitSent waits until `kind` messages carry exactly these lines.
func (d *fakeDaemon) waitSent(kind string, want ...string) {
	d.t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if slices.Equal(d.sentOf(kind), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.t.Fatalf("%s lines = %q, want %q", kind, d.sentOf(kind), want)
}

// openFakeSession plays the daemon's side of the opening: hello, the app's open, opened. mcp nil: a daemon that sends
// no `mcp` in opened.
func openFakeSession(t *testing.T, mcp msg, version string) (*fakeDaemon, *goclient.Session, string) {
	t.Helper()
	d := startFakeDaemon(t)
	type result struct {
		s   *goclient.Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := goclient.OpenSession(context.Background(), d.Daemon, "http://localhost:5173", "claude")
		done <- result{s, err}
	}()
	d.accept()
	d.say(msg{"t": "hello", "version": version, "agents": []any{}})
	id := fmt.Sprint(d.nextSent(0)["id"])
	opened := msg{"t": "opened", "id": id, "pid": 42, "cwd": "/work"}
	if mcp != nil {
		opened["mcp"] = mcp
	}
	d.say(opened)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		t.Cleanup(func() { _ = r.s.Close() })
		return d, r.s, id
	case <-time.After(wait):
		t.Fatal("the session did not open")
	}
	return nil, nil, ""
}

func (d *fakeDaemon) out(id, data string) {
	d.say(msg{"t": "frame", "id": id, "kind": "out", "data": data, "code": 0})
}

// readAll reads until EOF, or fails.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		got <- string(b)
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(wait):
		t.Fatal("the stream did not end")
	}
	return ""
}

func TestOpensOneAgentOnItsOwnSocketAndRelaysLinesBothWays(t *testing.T) {
	d := startFakeDaemon(t)
	type result struct {
		s   *goclient.Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := goclient.OpenSession(context.Background(), d.Daemon, "http://localhost:5173", "claude")
		done <- result{s, err}
	}()
	d.accept()
	d.mu.Lock()
	origin := d.origin
	d.mu.Unlock()
	if origin != "http://localhost:5173" {
		t.Fatalf("Origin = %q", origin)
	}
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	open := d.nextSent(0)
	if open["t"] != "open" || open["agent"] != "claude" || open["id"] == "" {
		t.Fatalf("open = %v", open)
	}
	if len(fmt.Sprint(open["id"])) != 36 {
		t.Fatalf("id = %v, want a UUID", open["id"])
	}
	d.say(msg{"t": "opened", "id": open["id"], "pid": 42, "cwd": "/work"})
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	s := r.s
	if s.Pid != 42 || s.Cwd != "/work" {
		t.Fatalf("pid %d cwd %q", s.Pid, s.Cwd)
	}

	lines := make(chan string, 4)
	s.OnFrame(func(f goclient.Frame) {
		if f.Kind == goclient.FrameOut {
			lines <- f.Data
		}
	})
	d.out(fmt.Sprint(open["id"]), "hello")
	d.out("someone-else", "ignored")
	d.out(fmt.Sprint(open["id"]), "second")
	for _, want := range []string{"hello", "second"} {
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("frame = %q, want %q", got, want)
			}
		case <-time.After(wait):
			t.Fatalf("no frame %q", want)
		}
	}
	if err := s.Write("ping"); err != nil {
		t.Fatal(err)
	}
	if got := d.nextSent(1); fmt.Sprint(got) != fmt.Sprint(msg{"t": "write", "id": open["id"], "line": "ping"}) {
		t.Fatalf("write = %v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := d.nextSent(2); fmt.Sprint(got) != fmt.Sprint(msg{"t": "close", "id": open["id"]}) {
		t.Fatalf("close = %v", got)
	}
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("Done is not closed after Close")
	}
	if err := s.Write("late"); err != goclient.ErrClosed {
		t.Fatalf("a write after Close = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close = %v", err)
	}
}

func TestTheWireBytesOfTheMessagesAreTheJSOnesWithoutHTMLEscapes(t *testing.T) {
	raw := make(chan string, 8)
	// This daemon keeps the bytes of the messages, not their meaning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"hello","version":"1","agents":[]}`))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			raw <- string(data)
			var m msg
			_ = json.Unmarshal(data, &m)
			if m["t"] == "open" {
				_ = conn.WriteJSON(msg{"t": "opened", "id": m["id"], "pid": 1, "cwd": "/"})
			}
		}
	}))
	defer srv.Close()
	s, err := goclient.OpenSession(context.Background(), goclient.Daemon{Port: srv.Listener.Addr().(*net.TCPAddr).Port}, "http://localhost:5173", "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	open := <-raw
	var m msg
	_ = json.Unmarshal([]byte(open), &m)
	if want := `{"t":"open","id":"` + fmt.Sprint(m["id"]) + `","agent":"claude"}`; open != want {
		t.Fatalf("open = %s, want %s", open, want)
	}
	if err := s.Write(`{"a":"<b>&"}`); err != nil {
		t.Fatal(err)
	}
	if want := `{"t":"write","id":"` + fmt.Sprint(m["id"]) + `","line":"{\"a\":\"<b>&\"}"}`; <-raw != want {
		t.Fatal("write bytes differ")
	}
}

func TestAnErrorForTheOpenIsTheDaemonsMessage(t *testing.T) {
	d := startFakeDaemon(t)
	errs := make(chan error, 1)
	go func() {
		_, err := goclient.OpenSession(context.Background(), d.Daemon, "http://localhost:5173", "rm -rf")
		errs <- err
	}()
	d.accept()
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	d.say(msg{"t": "error", "id": "someone-else", "message": "not for this session"})
	d.say(msg{"t": "error", "id": d.nextSent(0)["id"], "message": `unknown agent "rm -rf"`})
	if err := <-errs; err == nil || err.Error() != `unknown agent "rm -rf"` {
		t.Fatalf("error = %v", err)
	}
}

func TestADaemonThatClosesBeforeOpenedIsAnError(t *testing.T) {
	d := startFakeDaemon(t)
	errs := make(chan error, 1)
	go func() {
		_, err := goclient.OpenSession(context.Background(), d.Daemon, "http://localhost:5173", "claude")
		errs <- err
	}()
	d.accept()
	_ = d.conn.Close()
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "the daemon closed the session") {
		t.Fatalf("error = %v", err)
	}
}

func TestADaemonThatRefusesTheSocketIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	_, err := goclient.OpenSession(context.Background(), goclient.Daemon{Port: srv.Listener.Addr().(*net.TCPAddr).Port}, "http://localhost:5173", "claude")
	if err == nil || !strings.Contains(err.Error(), "cannot open a session: the daemon refused the connection") {
		t.Fatalf("error = %v", err)
	}
	_, err = goclient.OpenSession(context.Background(), goclient.Daemon{Port: 1}, "http://localhost:5173", "claude")
	if err == nil {
		t.Fatal("no daemon on port 1")
	}
}

func TestOpeningEndsWithItsContext(t *testing.T) {
	d := startFakeDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := goclient.OpenSession(ctx, d.Daemon, "http://localhost:5173", "claude")
		errs <- err
	}()
	d.accept() // the daemon never says hello
	cancel()
	select {
	case err := <-errs:
		if err != context.Canceled {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(wait):
		t.Fatal("OpenSession ignores its context")
	}
}

// ---- the session's streams ----

func TestStdoutIsTheAgentsStdoutOneLinePerFrameAndEndsWhenTheAgentExits(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.4.0")
	stdout := s.Stdout()
	if s.Stdout() != stdout {
		t.Fatal("Stdout is one reader however often it is asked for")
	}
	d.out(id, `{"a":1}`)
	d.say(msg{"t": "frame", "id": id, "kind": "err", "data": "stderr is not stdout", "code": 0})
	d.out(id, "héllo")
	d.say(msg{"t": "frame", "id": id, "kind": "exit", "data": "", "code": 0})
	if got := readAll(t, stdout); got != "{\"a\":1}\nhéllo\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStdoutEndsWhenTheSocketCloses(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.4.0")
	stdout := s.Stdout()
	d.out(id, "last")
	time.Sleep(50 * time.Millisecond)
	_ = d.conn.Close()
	if got := readAll(t, stdout); got != "last\n" {
		t.Fatalf("stdout = %q", got)
	}
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("Done is not closed when the socket ends")
	}
}

func TestStdoutTakenAfterTheAgentIsGoneIsAlreadyOver(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "0.4.0")
	_ = d.conn.Close()
	<-s.Done()
	if got := readAll(t, s.Stdout()); got != "" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStdoutKeepsNoLineFromBeforeItWasTaken(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.4.0")
	seen := make(chan struct{}, 4)
	s.OnFrame(func(goclient.Frame) { seen <- struct{}{} })
	d.out(id, "before")
	<-seen
	stdout := s.Stdout()
	d.out(id, "after")
	<-seen
	d.say(msg{"t": "frame", "id": id, "kind": "exit", "code": 0})
	if got := readAll(t, stdout); got != "after\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestCloseEndsTheStreamsAReaderWaitsOn(t *testing.T) {
	_, s, _ := openFakeSession(t, nil, "0.4.0")
	got := make(chan string, 1)
	go func() { got <- readAll(t, s.Stdout()) }()
	time.Sleep(20 * time.Millisecond)
	_ = s.Close()
	if r := <-got; r != "" {
		t.Fatalf("stdout = %q", r)
	}
}

func TestStdinSendsEachCompleteLineOnceWhateverTheChunkBoundaries(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "0.4.0")
	bytes := []byte("{\"a\":1}\n{\"b\":\"é\"}\n\n{\"c\"")
	cut := slices.Index(bytes, 0xc3) + 1 // inside the two bytes of "é"
	stdin := s.Stdin()
	for _, chunk := range [][]byte{bytes[:3], bytes[3:cut], bytes[cut:]} {
		if n, err := stdin.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	d.waitSent("write", `{"a":1}`, `{"b":"é"}`)
	if _, err := stdin.Write([]byte(":3}\n")); err != nil {
		t.Fatal(err)
	}
	d.waitSent("write", `{"a":1}`, `{"b":"é"}`, `{"c":3}`)
}

func TestClosingStdinSendsTheLineStillWaitingForItsNewline(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "0.4.0")
	if _, err := s.Stdin().Write([]byte("one\ntwo")); err != nil {
		t.Fatal(err)
	}
	if err := s.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	d.waitSent("write", "one", "two")
	if _, err := s.Stdin().Write([]byte("three\n")); err != io.ErrClosedPipe {
		t.Fatalf("a write after Close = %v", err)
	}
}

func TestAWriteToASessionWhoseSocketEndedFails(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "0.4.0")
	_ = d.conn.Close()
	<-s.Done()
	if _, err := s.Stdin().Write([]byte("late\n")); err == nil {
		t.Fatal("a write to a dead session succeeded")
	}
}

// ---- the official ACP library over a session ----

// actAsAgent makes the fake daemon answer ACP's initialize and session/new as an agent would on its stdout; asked
// collects the params of each request by method.
func actAsAgent(d *fakeDaemon, id string) (asked func(method string) json.RawMessage) {
	var mu sync.Mutex
	params := map[string]json.RawMessage{}
	d.mu.Lock()
	d.onsend = func(m msg) {
		if m["t"] != "write" {
			return
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal([]byte(fmt.Sprint(m["line"])), &req)
		mu.Lock()
		params[req.Method] = req.Params
		mu.Unlock()
		var answer msg
		switch req.Method {
		case "initialize":
			answer = msg{"result": msg{"protocolVersion": acp.ProtocolVersionNumber, "agentCapabilities": msg{}, "authMethods": []any{}}}
		case "session/new":
			answer = msg{"result": msg{"sessionId": "s-1"}}
		default:
			answer = msg{"error": msg{"code": -32601, "message": "no such method"}}
		}
		answer["jsonrpc"] = "2.0"
		answer["id"] = req.ID
		b, _ := json.Marshal(answer)
		d.out(id, string(b))
	}
	d.mu.Unlock()
	return func(method string) json.RawMessage {
		mu.Lock()
		defer mu.Unlock()
		return params[method]
	}
}

// pageClient is the app's side of ACP: it takes no file or terminal request and answers a permission request with a cancel.
type pageClient struct{ acp.Client }

func (pageClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

func (pageClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func TestTheACPLibraryRunsInitializeAndSessionNewThroughTheStreams(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.4.0")
	actAsAgent(d, id)
	conn := acp.NewClientSideConnection(pageClient{}, s.Stdin(), s.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	init, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil || init.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("Initialize = %+v, %v", init, err)
	}
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: s.Cwd, McpServers: []acp.McpServer{}})
	if err != nil || sess.SessionId != "s-1" {
		t.Fatalf("NewSession = %+v, %v", sess, err)
	}
}

func TestARequestInFlightFailsWhenTheAgentExits(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.4.0")
	asked := actAsAgent(d, id)
	_ = asked
	conn := acp.NewClientSideConnection(pageClient{}, s.Stdin(), s.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.onsend = func(msg) { d.say(msg{"t": "frame", "id": id, "kind": "exit", "data": "", "code": 1}) }
	d.mu.Unlock()
	if _, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: "s-1", Prompt: []acp.ContentBlock{acp.TextBlock("hi")}}); err == nil {
		t.Fatal("the prompt of an agent that exited succeeded")
	}
}

// ---- the app's MCP server ----

var mcpWire = msg{
	"command": "/tmp/agentd-x1/bin/page/agentd-mcp",
	"args":    []any{},
	"env": []any{
		msg{"name": "AGENTD_MCP_SOCKET", "value": "/tmp/agentd-abc123/mcp.sock"},
		msg{"name": "AGENTD_MCP_TOKEN", "value": "tok"},
	},
}

var mcpServer = goclient.McpServer{
	Name:    "page",
	Command: "/tmp/agentd-x1/bin/page/agentd-mcp",
	Args:    []string{},
	Env:     []goclient.EnvVar{{Name: "AGENTD_MCP_SOCKET", Value: "/tmp/agentd-abc123/mcp.sock"}, {Name: "AGENTD_MCP_TOKEN", Value: "tok"}},
}

func TestSessionNewTakesMcpServerAsOneOfItsMcpServers(t *testing.T) {
	d, s, id := openFakeSession(t, mcpWire, "0.4.0")
	asked := actAsAgent(d, id)
	conn := acp.NewClientSideConnection(pageClient{}, s.Stdin(), s.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	server, err := s.McpServer("page")
	if err != nil {
		t.Fatal(err)
	}
	env := make([]acp.EnvVariable, len(server.Env))
	for i, e := range server.Env {
		env[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
	}
	stdio := acp.McpServer{Stdio: &acp.McpServerStdio{Name: server.Name, Command: server.Command, Args: server.Args, Env: env}}
	if _, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: s.Cwd, McpServers: []acp.McpServer{stdio}}); err != nil {
		t.Fatal(err)
	}
	want := `{"cwd":"/work","mcpServers":[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]}`
	var got, wantJSON any
	_ = json.Unmarshal(asked("session/new"), &got)
	_ = json.Unmarshal([]byte(want), &wantJSON)
	if fmt.Sprint(got) != fmt.Sprint(wantJSON) {
		t.Fatalf("session/new params = %s", asked("session/new"))
	}
}

func TestMcpServerIsTheStdioServerTheDaemonNamedUnderThatNameACopyEachTime(t *testing.T) {
	_, s, _ := openFakeSession(t, mcpWire, "0.4.0")
	got, err := s.McpServer("aiocean")
	if err != nil {
		t.Fatal(err)
	}
	want := mcpServer
	want.Name = "aiocean"
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("McpServer = %+v", got)
	}
	if b, _ := json.Marshal(got); !strings.Contains(string(b), `"args":[]`) {
		t.Fatalf("args must be an array on the wire: %s", b)
	}
	a, _ := s.McpServer("a")
	a.Env[0].Value = "changed"
	a.Args = append(a.Args, "x")
	again, _ := s.McpServer("a")
	if len(again.Env) != 2 || again.Env[0].Value != "/tmp/agentd-abc123/mcp.sock" || len(again.Args) != 0 {
		t.Fatalf("each call is a copy: %+v", again)
	}
}

func TestMcpServerOnADaemonThatBridgesNoMcpServerSaysSoAndNamesTheDaemonsVersion(t *testing.T) {
	_, s, _ := openFakeSession(t, nil, "0.3.0")
	if _, err := s.McpServer("page"); err == nil || !strings.Contains(err.Error(), "agentd 0.3.0 does not bridge a page's MCP server") {
		t.Fatalf("error = %v", err)
	}
}

func TestMcpIsTheAgentsMcpLinesForThisSessionAndItsWritesGoBackAsMcp(t *testing.T) {
	d, s, id := openFakeSession(t, mcpWire, "0.4.0")
	mcp := s.Mcp()
	d.say(msg{"t": "mcp", "id": "someone-else", "line": `{"id":9}`})
	d.say(msg{"t": "mcp", "id": id, "line": `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`})
	line, err := bufio.NewReader(mcp).ReadString('\n')
	if err != nil || line != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n" {
		t.Fatalf("read %q, %v", line, err)
	}
	if _, err := mcp.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	d.waitSent("mcp", `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	if len(d.sentOf("write")) != 0 {
		t.Fatal("an MCP line went to the agent's stdin")
	}
}

// serveTools is the app's MCP server of the README ("Tools from the page"), as it is there.
func serveTools(rw io.ReadWriter) {
	in := bufio.NewScanner(rw)
	for in.Scan() {
		var req struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil || req.Method == "" {
			continue // a notification, or an answer
		}
		var result any
		switch req.Method {
		case "initialize":
			result = msg{"protocolVersion": "2025-06-18", "capabilities": msg{"tools": msg{}}, "serverInfo": msg{"name": "page", "version": "1"}}
		case "tools/list":
			result = msg{"tools": []any{msg{"name": "current_page", "inputSchema": msg{"type": "object"}}}}
		case "tools/call":
			result = msg{"content": []any{msg{"type": "text", "text": "/projects/42"}}}
		}
		answer := msg{"jsonrpc": "2.0", "id": req.ID}
		if result == nil {
			answer["error"] = msg{"code": -32601, "message": "no method " + req.Method}
		} else {
			answer["result"] = result
		}
		b, _ := json.Marshal(answer)
		_, _ = rw.Write(append(b, '\n'))
	}
}

func TestAnMCPServerOverMcpAnswersTheAgentsToolsList(t *testing.T) {
	d, s, id := openFakeSession(t, mcpWire, "0.4.0")
	go serveTools(s.Mcp())
	d.say(msg{"t": "mcp", "id": id, "line": `{"jsonrpc":"2.0","method":"notifications/initialized"}`})
	d.say(msg{"t": "mcp", "id": id, "line": `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`})
	line := d.nextSent(1)
	if line["t"] != "mcp" {
		t.Fatalf("sent %v", line)
	}
	if want := `{"id":1,"jsonrpc":"2.0","result":{"tools":[{"inputSchema":{"type":"object"},"name":"current_page"}]}}`; line["line"] != want {
		t.Fatalf("answer = %v", line["line"])
	}
}

func TestMcpEndsWhenTheAgentExitsOrTheSocketCloses(t *testing.T) {
	d, s, id := openFakeSession(t, mcpWire, "0.4.0")
	mcp := s.Mcp()
	d.say(msg{"t": "mcp", "id": id, "line": "last"})
	d.say(msg{"t": "frame", "id": id, "kind": "exit", "code": 0})
	if got := readAll(t, mcp); got != "last\n" {
		t.Fatalf("mcp = %q", got)
	}

	d2, s2, _ := openFakeSession(t, mcpWire, "0.4.0")
	rest := s2.Mcp()
	_ = d2.conn.Close()
	if got := readAll(t, rest); got != "" {
		t.Fatalf("mcp = %q", got)
	}
}

func TestClosingMcpSendsTheLineStillWaiting(t *testing.T) {
	d, s, _ := openFakeSession(t, mcpWire, "0.4.0")
	mcp := s.Mcp()
	_, _ = mcp.Write([]byte("a\nb"))
	_ = mcp.Close()
	d.waitSent("mcp", "a", "b")
}

func TestChdirAsksTheDaemonToMoveTheAgentAndTakesItsNewPidAndCwd(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.9.0")
	moved := make(chan error, 1)
	go func() { moved <- s.Chdir(context.Background(), "/work/app") }()
	if got := d.nextSent(1); got["t"] != "chdir" || got["id"] != id || got["dir"] != "/work/app" {
		t.Fatalf("the daemon got %v", got)
	}
	d.say(msg{"t": "moved", "id": id, "pid": 43, "cwd": "/work/app"})
	select {
	case err := <-moved:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(wait):
		t.Fatal("Chdir did not return")
	}
	if s.Pid != 43 || s.Cwd != "/work/app" {
		t.Fatalf("pid %d, cwd %q", s.Pid, s.Cwd)
	}
	d.out(id, "from the new agent")
	line, _ := bufio.NewReader(s.Stdout()).ReadString('\n')
	if line != "from the new agent\n" {
		t.Fatalf("read %q", line)
	}
}

func TestChdirRefusedByTheDaemonIsItsMessageAndLeavesCwdAlone(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "0.9.0")
	moved := make(chan error, 1)
	go func() { moved <- s.Chdir(context.Background(), "/etc") }()
	d.nextSent(1)
	d.say(msg{"t": "error", "id": id, "message": "/etc is outside the folders you allowed this app"})
	var refusal *goclient.SessionError
	select {
	case err := <-moved:
		if !errors.As(err, &refusal) || refusal.Message != "/etc is outside the folders you allowed this app" {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(wait):
		t.Fatal("Chdir did not return")
	}
	if s.Cwd != "/work" {
		t.Fatalf("cwd %q", s.Cwd)
	}
}

func TestChdirEndsWithItsContextAndWithTheSession(t *testing.T) {
	_, s, _ := openFakeSession(t, nil, "0.9.0")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Chdir(ctx, "/work/app"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	_ = s.Close()
	if err := s.Chdir(context.Background(), "/work/app"); !errors.Is(err, goclient.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}
