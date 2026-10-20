package pagetools

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const wait = 5 * time.Second

// newRelay listens in a short temporary directory (a socket path is limited to about 100 bytes).
func newRelay(t *testing.T) *McpRelay {
	t.Helper()
	return newRelayCapped(t, 1<<20)
}

func newRelayCapped(t *testing.T, maxLine int) *McpRelay {
	t.Helper()
	dir, err := os.MkdirTemp("", "mc-")
	if err != nil {
		t.Fatal(err)
	}
	r, err := listen(filepath.Join(dir, "mcp.sock"), maxLine)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Close()
		_ = os.RemoveAll(dir)
	})
	return r
}

// recordingPage keeps the lines it gets. Its backlog is nil unless a test sets backlog.
type recordingPage struct {
	mu      sync.Mutex
	lines   []string
	arrived chan struct{}
	backlog func(received int) <-chan struct{}
	onSend  func(line string)
}

func newPage() *recordingPage {
	return &recordingPage{arrived: make(chan struct{}, 1024)}
}

func (p *recordingPage) Send(line string) {
	p.mu.Lock()
	p.lines = append(p.lines, line)
	hook := p.onSend
	p.mu.Unlock()
	p.arrived <- struct{}{}
	if hook != nil {
		hook(line)
	}
}

func (p *recordingPage) Backlog() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backlog == nil {
		return nil
	}
	return p.backlog(len(p.lines))
}

func (p *recordingPage) got() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

// await waits for n lines.
func (p *recordingPage) await(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(wait)
	for len(p.got()) < n {
		select {
		case <-p.arrived:
		case <-deadline:
			t.Fatalf("timed out: the page has %d of %d lines: %q", len(p.got()), n, p.got())
		}
	}
}

// testBridge is a bridge as agentd-mcp --page is one.
type testBridge struct {
	conn   net.Conn
	lines  chan string
	closed chan struct{}
}

func dial(t *testing.T, path string) *testBridge {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	b := &testBridge{conn: conn, lines: make(chan string, 64), closed: make(chan struct{})}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		defer close(b.closed)
		sc := bufio.NewScanner(conn)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			b.lines <- sc.Text()
		}
	}()
	return b
}

// bridge says hello with token and waits for the daemon's answer, which it returns.
func bridge(t *testing.T, path, token string) (*testBridge, string) {
	t.Helper()
	b := dial(t, path)
	b.send(t, BridgeHello(token)+"\n")
	return b, b.next(t)
}

func (b *testBridge) send(t *testing.T, text string) {
	t.Helper()
	if _, err := b.conn.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
}

func (b *testBridge) next(t *testing.T) string {
	t.Helper()
	select {
	case line := <-b.lines:
		return line
	case <-time.After(wait):
		t.Fatal("timed out waiting for a line of the daemon")
		return ""
	}
}

func (b *testBridge) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-b.closed:
	case <-time.After(wait):
		t.Fatal("timed out waiting for the daemon to close the connection")
	}
}

func TestATokenIsUnguessableAtLeast128RandomBitsNewForEveryBinding(t *testing.T) {
	r := newRelay(t)
	a := r.Bind(newPage())
	b := r.Bind(newPage())
	raw, err := base64.RawURLEncoding.DecodeString(a.Token())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 16 {
		t.Errorf("a token of %d bytes", len(raw))
	}
	if a.Token() == b.Token() {
		t.Error("two bindings share a token")
	}
}

func TestABridgeWithTheTokenReachesItsPageAndThePagesLinesReachTheBridge(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	binding := r.Bind(p)
	b, answer := bridge(t, r.Path(), binding.Token())
	if answer != `{"ok":true}` {
		t.Fatalf("answer %s", answer)
	}
	b.send(t, "{\"id\":1}\n\n{\"id\":2}\r\n")
	p.await(t, 2)
	if want := []string{`{"id":1}`, `{"id":2}`}; !reflect.DeepEqual(p.got(), want) {
		t.Fatalf("page got %q; want %q", p.got(), want)
	}
	if !binding.ToBridge(`{"id":1,"result":{}}`) {
		t.Fatal("ToBridge: no bridge")
	}
	if got := b.next(t); got != `{"id":1,"result":{}}` {
		t.Fatalf("bridge got %s", got)
	}
}

func TestAnUnknownTokenIsRefusedWithAReasonClosedAndReachesNoPage(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	r.Bind(p)
	b, answer := bridge(t, r.Path(), "not-a-token")
	var got struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !strings.Contains(got.Message, "no open session has this token") {
		t.Fatalf("answer %s", answer)
	}
	b.waitClosed(t)
	if lines := p.got(); len(lines) != 0 {
		t.Fatalf("page got %q", lines)
	}
}

func TestAHelloThatIsNotOneIsRefusedToo(t *testing.T) {
	r := newRelay(t)
	b := dial(t, r.Path())
	b.send(t, "hello\n")
	if answer := ParseBridgeAnswer(b.next(t)); answer.OK {
		t.Fatalf("answer %+v", answer)
	}
	b.waitClosed(t)
}

func TestARefusedConnectionGetsNoSecondTryInTheSameBreath(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	binding := r.Bind(p)
	b := dial(t, r.Path())
	b.send(t, BridgeHello("not-a-token")+"\n"+BridgeHello(binding.Token())+"\n{\"id\":1}\n")
	b.waitClosed(t)
	var answers []string
	for len(b.lines) > 0 {
		answers = append(answers, <-b.lines)
	}
	if len(answers) != 1 {
		t.Fatalf("answers %q; want one", answers)
	}
	if lines := p.got(); len(lines) != 0 {
		t.Fatalf("page got %q", lines)
	}
	if binding.ToBridge("{}") {
		t.Fatal("the second hello bridged")
	}
}

func TestAPageLineWhileNoBridgeIsConnectedIsNotDelivered(t *testing.T) {
	r := newRelay(t)
	if r.Bind(newPage()).ToBridge("{}") {
		t.Fatal("delivered")
	}
}

func TestEndingTheBindingClosesItsBridgeAndRefusesItsTokenFromThenOn(t *testing.T) {
	r := newRelay(t)
	binding := r.Bind(newPage())
	b, _ := bridge(t, r.Path(), binding.Token())
	binding.End()
	b.waitClosed(t)
	if binding.ToBridge("{}") {
		t.Fatal("delivered after End")
	}
	_, answer := bridge(t, r.Path(), binding.Token())
	if ParseBridgeAnswer(answer).OK {
		t.Fatalf("answer %s", answer)
	}
	binding.End() // twice is as once
}

func TestANewerBridgeWithTheSameTokenReplacesTheOlderOne(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	binding := r.Bind(p)
	older, _ := bridge(t, r.Path(), binding.Token())
	newer, answer := bridge(t, r.Path(), binding.Token())
	older.waitClosed(t)
	if answer != `{"ok":true}` {
		t.Fatalf("answer %s", answer)
	}
	binding.ToBridge(`{"to":"newer"}`)
	if got := newer.next(t); got != `{"to":"newer"}` {
		t.Fatalf("newer got %s", got)
	}
	if len(older.lines) != 0 {
		t.Fatal("the older bridge got more than its answer")
	}
	newer.send(t, "{\"from\":\"newer\"}\n")
	p.await(t, 1)
	if want := []string{`{"from":"newer"}`}; !reflect.DeepEqual(p.got(), want) {
		t.Fatalf("page got %q", p.got())
	}
	// The older bridge's end does not unbind the newer one.
	if !binding.ToBridge("{}") {
		t.Fatal("the newer bridge was unbound")
	}
}

func TestAPageThatIsBehindPausesItsBridgesLinesUntilItHasRoom(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	room := make(chan struct{})
	p.backlog = func(received int) <-chan struct{} {
		if received == 1 {
			return room
		}
		return nil
	}
	b, _ := bridge(t, r.Path(), r.Bind(p).Token())
	b.send(t, "1\n")
	p.await(t, 1)
	b.send(t, "2\n")
	time.Sleep(200 * time.Millisecond)
	if want := []string{"1"}; !reflect.DeepEqual(p.got(), want) {
		t.Fatalf("page got %q while behind; want %q", p.got(), want)
	}
	close(room)
	p.await(t, 2)
	if want := []string{"1", "2"}; !reflect.DeepEqual(p.got(), want) {
		t.Fatalf("page got %q; want %q", p.got(), want)
	}
}

func TestABridgeThatGoesAwayWhileItsPageIsBehindFreesItsGoroutine(t *testing.T) {
	r := newRelay(t)
	p := newPage()
	never := make(chan struct{})
	p.backlog = func(received int) <-chan struct{} { // behind from its first line on
		if received == 0 {
			return nil
		}
		return never
	}
	binding := r.Bind(p)
	b, _ := bridge(t, r.Path(), binding.Token())
	b.send(t, "1\n")
	p.await(t, 1)
	binding.End() // closes the bridge, which the waiting reader sees
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Close waits on a bridge that is gone")
	}
}

func TestALineOverTheCapIsNotFromABridgeItsLinesBeforeItArriveAndThenItIsClosed(t *testing.T) {
	r := newRelayCapped(t, 64)
	p := newPage()
	b, _ := bridge(t, r.Path(), r.Bind(p).Token())
	b.send(t, "short\n"+strings.Repeat("x", 65)+"\nafter\n")
	b.waitClosed(t)
	if want := []string{"short"}; !reflect.DeepEqual(p.got(), want) {
		t.Fatalf("page got %q; want %q", p.got(), want)
	}
}

func TestALineAtTheCapGoesThrough(t *testing.T) {
	r := newRelayCapped(t, 64)
	p := newPage()
	b, _ := bridge(t, r.Path(), r.Bind(p).Token())
	b.send(t, strings.Repeat("x", 64)+"\n")
	p.await(t, 1)
}

func TestClosingTheRelayEndsEveryBridgeAndStopsListening(t *testing.T) {
	r := newRelay(t)
	b, _ := bridge(t, r.Path(), r.Bind(newPage()).Token())
	waiting := dial(t, r.Path()) // connected, no hello yet
	r.Close()
	b.waitClosed(t)
	waiting.waitClosed(t)
	if conn, err := net.Dial("unix", r.Path()); err == nil {
		_ = conn.Close()
		t.Fatal("still listening")
	}
	r.Close() // twice is as once
}

func TestListenFailsWhenThePathCannotBeASocket(t *testing.T) {
	if r, err := Listen(filepath.Join(t.TempDir(), "gone", "mcp.sock")); err == nil {
		r.Close()
		t.Fatal("listened in a directory that is not there")
	}
	if r, err := Listen("/tmp/" + strings.Repeat("a", 200) + ".sock"); err == nil {
		r.Close()
		t.Fatal("listened on a path too long for a socket")
	}
}

// A page's own MCP server reached through the relay, as the real Claude test does it (daemon/test/integration/
// pagetools.real.test.ts) without the agent: the bridge sends the agent's MCP requests, the page answers them.
func TestAPagesToolIsCalledThroughTheRelay(t *testing.T) {
	const word = "agentd1a2b3c4d"
	r := newRelay(t)
	p := newPage()
	binding := r.Bind(p)
	srv := PageMcpServer("/bin/agentd-mcp", r.Path(), binding.Token())
	env := map[string]string{}
	for _, e := range srv.Env {
		env[e.Name] = e.Value
	}
	b, answer := bridge(t, env[McpSocketEnv], env[McpTokenEnv])
	if answer != BridgeOK() {
		t.Fatalf("answer %s", answer)
	}

	called := make(chan struct{}, 1)
	p.onSend = func(line string) {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(line), &req) != nil || req.ID == nil {
			return // a notice needs no answer
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "page", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "secret_word", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			called <- struct{}{}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": word}}}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		binding.ToBridge(string(out))
	}

	for i, method := range []string{"initialize", "tools/list", "tools/call"} {
		req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": method})
		b.send(t, string(req)+"\n")
		if method == "initialize" {
			b.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
		}
		var res struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(b.next(t)), &res); err != nil || res.ID != i+1 {
			t.Fatalf("answer to %s: id %d, %v", method, res.ID, err)
		}
		if method == "tools/call" && (len(res.Result.Content) != 1 || res.Result.Content[0].Text != word) {
			t.Fatalf("tool result %+v", res.Result)
		}
	}
	select {
	case <-called:
	default:
		t.Fatal("the page's tool was not called")
	}
}
