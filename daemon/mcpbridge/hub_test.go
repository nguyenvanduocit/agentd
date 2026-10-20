package mcpbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/link"
	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// These tests run the bridge against the real hub package, as agentd-mcp runs against an app's server.

const (
	hubUser   = "alice"
	hubToken  = "token-a"
	hubGrant  = "s1"
	longWait  = 30 * time.Second
	fastRetry = 20 * time.Millisecond
)

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// store is the hub's token store: it counts the handshakes per token and fails its lookups while down, as a
// database does, which the hub answers "unavailable".
type store struct {
	goserver.Tokens
	down atomic.Bool

	mu     sync.Mutex
	counts map[string]int
}

func (s *store) ByHash(hash string) (goserver.Record, error) {
	s.mu.Lock()
	s.counts[hash]++
	s.mu.Unlock()
	if s.down.Load() {
		return goserver.Record{}, errors.New("connection refused: the database is down")
	}
	return s.Tokens.ByHash(hash)
}

func (s *store) handshakes(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[sha256hex(token)]
}

// tools is the app's MCP tool server: one tool, ping, answers pong. Serve runs one in-memory MCP server per bridge.
type tools struct {
	serving atomic.Int32
	kick    chan struct{} // closed: every tool session ends, which makes the hub disconnect its bridge
}

func (*tools) Owns(user, _, grant string) bool { return user == hubUser && grant == hubGrant }

func (t *tools) Serve(ctx context.Context, _, _ string, transport mcp.Transport) error {
	t.serving.Add(1)
	srv := mcp.NewServer(&mcp.Implementation{Name: "app", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping", Description: "Returns the text pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-t.kick:
			cancel()
		case <-ctx.Done():
		}
	}()
	return srv.Run(ctx, transport)
}

// hubEnv is the real hub on an httptest server, with one paired daemon whose token the bridge carries.
type hubEnv struct {
	store *store
	tools *tools
	srv   *httptest.Server
}

func newHubEnv(t *testing.T) *hubEnv {
	t.Helper()
	tokens := goserver.NewMemoryTokens()
	e := &hubEnv{tools: &tools{kick: make(chan struct{})}}
	e.store = &store{Tokens: tokens, counts: map[string]int{}}
	if _, err := tokens.Add(goserver.Record{ID: sha256hex(hubToken)[:32], UserID: hubUser, TokenHash: sha256hex(hubToken), Name: "laptop", Hostname: "laptop", OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	h := goserver.New(goserver.Options{Tokens: e.store, Tools: e.tools})
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(goserver.MaxMessageBytes)
	sio := socket.NewServer(nil, opts)
	mux := http.NewServeMux()
	h.Mount(sio, mux)
	mux.Handle("/socket.io/", sio.ServeHandler(nil))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() { sio.Close(nil); e.srv.Close() })
	return e
}

// config is a daemon.json that pairs the daemon with server under token.
func config(t *testing.T, server, token string) *string {
	t.Helper()
	doc, err := json.Marshal(map[string]any{"apps": []map[string]string{{"server": server, "token": token}}, "browsers": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return configFile(t, string(doc))
}

// dialFast is Dial with the retry waits of a test.
func dialFast(o link.Options, h link.ClientHandler, ev link.Events) (Session, error) {
	o.Backoff.Base, o.Backoff.Max = fastRetry, fastRetry
	return Dial(o, h, ev)
}

func realBridge(t *testing.T, cfg *string) *run {
	t.Helper()
	return start(t, Deps{Host: hostWithEnv(nil), Connect: dialFast},
		func(d Deps) int { return RunBridge(cli.MCP{Session: hubGrant, Config: cfg}, d) })
}

func (r *run) exitWithin(t *testing.T, d time.Duration) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(d):
		t.Fatalf("the bridge did not exit within %v; stderr: %q", d, r.stderr.String())
		return -1
	}
}

const (
	initialize  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	initialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	ping        = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}`
)

func TestAnAgentGetsPongFromTheServersPingToolThroughTheBridge(t *testing.T) {
	e := newHubEnv(t)
	r := realBridge(t, config(t, e.srv.URL, hubToken))
	r.send(t, initialize+"\n"+initialized+"\n"+ping+"\n")
	waitFor(t, "the ping result on stdout", func() bool { return strings.Contains(r.stdout.String(), `"id":2`) })
	var reply struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	lines := strings.Split(strings.TrimSpace(r.stdout.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &reply); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout.String(), err)
	}
	if reply.ID != 2 || len(reply.Result.Content) != 1 || reply.Result.Content[0].Text != "pong" {
		t.Fatalf("the ping result = %+v", reply)
	}
	if n := e.tools.serving.Load(); n != 1 {
		t.Fatalf("%d tool sessions, want 1", n)
	}
	_ = r.stdin.Close()
	if code := r.exitWithin(t, longWait); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestTheBridgeExitsWith1WhenTheServerEndsItsConnection(t *testing.T) {
	e := newHubEnv(t)
	r := realBridge(t, config(t, e.srv.URL, hubToken))
	r.send(t, initialize+"\n")
	waitFor(t, "the initialize reply", func() bool { return strings.Contains(r.stdout.String(), `"id":1`) })
	close(e.tools.kick)
	if code := r.exitWithin(t, longWait); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr: %q", code, r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "the server connection ended (") {
		t.Fatalf("stderr = %q", r.stderr.String())
	}
}

func TestARefusedTokenExitsWith1AndSaysSo(t *testing.T) {
	e := newHubEnv(t)
	r := realBridge(t, config(t, e.srv.URL, "revoked"))
	if code := r.exitWithin(t, longWait); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "refused the bridge (unauthorized)") {
		t.Fatalf("stderr = %q", r.stderr.String())
	}
}

func TestAServerThatCannotCheckTheTokenNowIsRetriedThenTheBridgeGetsThrough(t *testing.T) {
	e := newHubEnv(t)
	e.store.down.Store(true)
	r := realBridge(t, config(t, e.srv.URL, hubToken))
	waitFor(t, "two refused handshakes", func() bool { return e.store.handshakes(hubToken) >= 2 })
	e.store.down.Store(false)
	waitFor(t, "the bridge's tool session", func() bool { return e.tools.serving.Load() == 1 })
	_ = r.stdin.Close()
	if code := r.exitWithin(t, longWait); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %q", code, r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "(unavailable)") || strings.Contains(r.stderr.String(), "refused the bridge") {
		t.Fatalf("stderr = %q: want the retries reported as trouble, not as a refusal", r.stderr.String())
	}
}

func TestAServerThatStaysUnavailableEndsTheBridgeAfterAFewTries(t *testing.T) {
	e := newHubEnv(t)
	e.store.down.Store(true)
	r := realBridge(t, config(t, e.srv.URL, hubToken))
	if code := r.exitWithin(t, longWait); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "(unavailable)") {
		t.Fatalf("stderr = %q", r.stderr.String())
	}
	if n := e.store.handshakes(hubToken); n != 1+reconnectAttempts {
		t.Fatalf("%d handshakes, want %d (the first try and %d retries)", n, 1+reconnectAttempts, reconnectAttempts)
	}
}

func TestAServerThatCannotBeReachedEndsTheBridgeAfterAFewTries(t *testing.T) {
	e := newHubEnv(t)
	url := e.srv.URL
	e.srv.Close() // the address is now closed
	r := realBridge(t, config(t, url, hubToken))
	if code := r.exitWithin(t, longWait); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "cannot reach") {
		t.Fatalf("stderr = %q", r.stderr.String())
	}
}

func TestEndOfStdinAfterAnAnswerClosesTheBridgeWith0(t *testing.T) {
	e := newHubEnv(t)
	r := realBridge(t, config(t, e.srv.URL, hubToken))
	r.send(t, initialize+"\n\n") // the empty line is not forwarded
	waitFor(t, "the initialize answer on stdout", func() bool { return strings.Contains(r.stdout.String(), `"id":1`) })
	_ = r.stdin.Close()
	if code := r.exitWithin(t, longWait); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %q", code, r.stderr.String())
	}
	if !strings.Contains(r.stdout.String(), `"serverInfo"`) || strings.Contains(r.stderr.String(), "connection ended") {
		t.Fatalf("stdout = %q, stderr = %q", r.stdout.String(), r.stderr.String())
	}
}
