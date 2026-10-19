package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/hub"
)

// The app's server the daemon dials, as the daemon's tests need it: the contract's server side on the /agentd
// namespace exactly as the real server serves it (hub.MaxMessageBytes of buffer), and calls a test makes on the
// connected daemon (open, write, close, restart, stop) or against the socket (kick, revoke). It also answers the pairing
// request of an app with its code.

const (
	testToken   = "test-token"
	pairingCode = "TESTCODE"
	callTimeout = 90 * time.Second
	inlineLimit = 64 << 10 // longer frame data is reported as its length and sha256 only
)

// frame is one frame the daemon sent, in arrival order.
type frame struct {
	Session string
	Kind    string
	Data    string
	Len     int
	SHA     string
	Code    int
}

type peer struct {
	raw    *socket.Socket
	client *contract.Client
}

type fakeApp struct {
	token string
	io    *socket.Server
	srv   *httptest.Server

	mu          sync.Mutex
	daemon      *peer // the daemon that is connected now
	hellos      []contract.Hello
	agentLists  [][]contract.AgentInfo
	frames      []frame
	connects    int // accepted sockets of a daemon
	rejects     int
	revoked     bool
	unavailable int // handshakes still to refuse the way the hub does while its token store is down
}

// startApp serves a fake app that accepts testToken; t.Cleanup stops it.
func startApp(t *testing.T) *fakeApp {
	t.Helper()
	a := &fakeApp{token: testToken}
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(hub.MaxMessageBytes)
	a.io = socket.NewServer(nil, opts)
	ns := a.io.Of(hub.Namespace, nil)
	ns.Use(a.authenticate)
	if err := ns.On("connection", func(c ...any) { a.onConnection(t, c) }); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/socket.io/", a.io.ServeHandler(nil))
	mux.HandleFunc("POST /agentd/pair", a.pair)
	a.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		a.io.Close(nil)
		a.srv.CloseClientConnections()
		a.srv.Close()
	})
	return a
}

func (a *fakeApp) origin() string { return a.srv.URL }

func (a *fakeApp) authenticate(sock *socket.Socket, next func(*socket.ExtendedError)) {
	auth := sock.Handshake().Auth
	role, _ := auth["role"].(string)
	token, _ := auth["token"].(string)
	a.mu.Lock()
	revoked, down := a.revoked, a.unavailable > 0
	if down {
		a.unavailable--
		a.rejects++
	}
	a.mu.Unlock()
	if down {
		next(socket.NewExtendedError("unavailable", map[string]any{"code": "unavailable"}))
		return
	}
	if token != a.token || revoked || role != "daemon" {
		a.mu.Lock()
		a.rejects++
		a.mu.Unlock()
		next(socket.NewExtendedError("unauthorized", nil))
		return
	}
	next(nil)
}

func (a *fakeApp) onConnection(t *testing.T, clients []any) {
	raw, ok := clients[0].(*socket.Socket)
	if !ok {
		t.Errorf("connection event carries %T", clients[0])
		return
	}
	client, err := contract.NewClient(raw, &contract.ClientOptions{Timeout: callTimeout})
	if err != nil {
		t.Errorf("rpc client: %v", err)
		raw.Disconnect(true)
		return
	}
	p := &peer{raw: raw, client: client}
	_ = raw.On("disconnect", func(...any) {
		a.mu.Lock()
		if a.daemon == p {
			a.daemon = nil
		}
		a.mu.Unlock()
	})
	// The daemon is current before its first call can arrive: it says hello the moment it connects.
	a.mu.Lock()
	a.connects++
	a.daemon = p
	a.mu.Unlock()
	if _, err := contract.BindServer(raw, handler{a}); err != nil {
		t.Errorf("bind: %v", err)
		raw.Disconnect(true)
	}
}

// handler implements contract.ServerHandler for the daemon's socket.
type handler struct{ a *fakeApp }

func (h handler) HandleFrame(_ context.Context, sid string, kind contract.FrameKind, data string, code float64) error {
	sum := sha256.Sum256([]byte(data))
	rec := frame{Session: sid, Kind: string(kind), Len: len(data), SHA: hex.EncodeToString(sum[:]), Code: int(code)}
	if len(data) <= inlineLimit {
		rec.Data = data
	}
	h.a.mu.Lock()
	h.a.frames = append(h.a.frames, rec)
	h.a.mu.Unlock()
	return nil
}

func (h handler) HandleHello(_ context.Context, info contract.Hello) (contract.HelloReply, error) {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	h.a.hellos = append(h.a.hellos, info)
	return contract.HelloReply{Protocol: hub.Protocol}, nil
}

func (h handler) HandleAgents(_ context.Context, list []contract.AgentInfo) error {
	h.a.mu.Lock()
	h.a.agentLists = append(h.a.agentLists, list)
	h.a.mu.Unlock()
	return nil
}

func (h handler) HandleMcp(context.Context, string) error { return nil }

// pair trades the app's code for its token.
func (a *fakeApp) pair(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Code != pairingCode {
		http.Error(w, "unknown or used code", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": a.token})
}

// current is the connected daemon, or nil.
func (a *fakeApp) current() *peer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.daemon
}

func (a *fakeApp) connected() bool { return a.current() != nil }

func (a *fakeApp) helloCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.hellos)
}

func (a *fakeApp) firstHello(t *testing.T) contract.Hello {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.hellos) == 0 {
		t.Fatal("the daemon has not said hello")
	}
	return a.hellos[0]
}

func (a *fakeApp) rejected() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rejects
}

func (a *fakeApp) accepted() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connects
}

// waitHellos waits until a daemon is connected and has said hello n times.
func (a *fakeApp) waitHellos(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "hello", func() bool { return a.helloCount() >= n && a.connected() })
}

// framesOf are the frames of a session (all of them when session is empty).
func (a *fakeApp) framesOf(session string) []frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []frame
	for _, f := range a.frames {
		if session == "" || f.Session == session {
			out = append(out, f)
		}
	}
	return out
}

// frameWhere waits for a frame of session that pred accepts.
func (a *fakeApp) frameWhere(t *testing.T, session, what string, pred func(frame) bool) frame {
	t.Helper()
	var found frame
	waitFor(t, what, func() bool {
		for _, f := range a.framesOf(session) {
			if pred(f) {
				found = f
				return true
			}
		}
		return false
	})
	return found
}

func isKind(kind string) func(frame) bool { return func(f frame) bool { return f.Kind == kind } }

func (a *fakeApp) must(t *testing.T) *peer {
	t.Helper()
	p := a.current()
	if p == nil {
		t.Fatal("no daemon is connected")
	}
	return p
}

// open asks the daemon to start the agent; the error is the daemon's RpcError.
func (a *fakeApp) open(t *testing.T, session, agent string) (contract.Opened, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return a.must(t).client.CallOpen(ctx, session, agent)
}

// openOK opens a session and returns its process group, failing the test on an error; the group is killed at the end.
func (a *fakeApp) openOK(t *testing.T, session, agent string) contract.Opened {
	t.Helper()
	opened, err := a.open(t, session, agent)
	if err != nil {
		t.Fatalf("open %s: %v", session, err)
	}
	killGroupAtEnd(t, int(opened.Pid))
	return opened
}

func (a *fakeApp) write(t *testing.T, session, line string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ok, err := a.must(t).client.CallWrite(ctx, session, line)
	if err != nil {
		t.Fatalf("write %s: %v", session, err)
	}
	return ok
}

func (a *fakeApp) closeSession(t *testing.T, session string) {
	t.Helper()
	if err := a.must(t).client.CallClose(context.Background(), session); err != nil {
		t.Fatalf("close %s: %v", session, err)
	}
}

func (a *fakeApp) restart(t *testing.T) {
	t.Helper()
	if err := a.must(t).client.CallRestart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func (a *fakeApp) stopDaemon(t *testing.T) {
	t.Helper()
	if err := a.must(t).client.CallStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// revoke refuses every later handshake and drops the daemon that is connected.
func (a *fakeApp) revoke() {
	a.mu.Lock()
	a.revoked = true
	p := a.daemon
	a.mu.Unlock()
	if p != nil {
		p.raw.Disconnect(false)
	}
}

// kick drops the daemon's socket: transport closes the connection (the client sees "transport close"), otherwise only
// the namespace socket is disconnected ("io server disconnect").
func (a *fakeApp) kick(t *testing.T, transport bool) {
	t.Helper()
	a.must(t).raw.Disconnect(transport)
}
