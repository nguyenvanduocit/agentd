package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const (
	cooldownFor     = 30 * time.Second
	lockoutRefusals = 3
	lockoutWindow   = 10 * time.Minute
	maxBody         = 4096
)

// ErrPortsTaken is the error of Start when every port of the range is in use.
var ErrPortsTaken = errors.New("every port of the gateway's range is taken")

// Deps is what the gateway needs from the daemon.
type Deps struct {
	Version string
	// Agents are the agents and whether each is ready now.
	Agents   func() []pagetools.AgentInfo
	Registry func() (registry.Registry, error)
	// Ask shows the dialogs and waits for the decision: the answer and, for an Allow, the folders chosen.
	Ask func(q consent.Question) consent.Decision
	// Linked is whether the daemon is linked to the app at `server` now (an app that Stop or a refused token detached
	// is saved, not linked).
	Linked func(server string) bool
	// ConnectServer pairs with the server at the page's origin using the code, saves it with its folders and links to
	// it now; it fails when the app refuses.
	ConnectServer func(server, code string, folders []string) error
	// GrantBrowser saves the origin's grant with its folders.
	GrantBrowser func(origin string, folders []string) error
	// Revoke forgets the browser grant and the app of `origin`, and ends their sessions.
	Revoke func(origin string) error
	// Upgrade serves the WebSocket upgrade of a browser-granted origin on /v1/session, and returns when the socket
	// is done (BrowserSessions.Upgrade). Without it every upgrade is refused.
	Upgrade func(origin string, w http.ResponseWriter, r *http.Request)
}

// Options are what Start takes besides the deps and the port.
type Options struct {
	// Gate decides which connections are of the daemon's own OS user. Required.
	Gate *consent.PeerGate
	// Now is the clock of the cooldown and the lockout; nil is time.Now.
	Now func() time.Time
}

// Gateway is the running server.
type Gateway struct {
	// Port is the port it listens on.
	Port int
	srv  *http.Server
	done chan struct{}
}

// Close stops listening and closes every connection that is not an upgraded socket (BrowserSessions.Shutdown ends
// those).
func (g *Gateway) Close() error {
	err := g.srv.Close()
	<-g.done
	return err
}

// out is an answer: a status and the JSON of its body.
type out struct {
	status int
	body   any
}

type refusal struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

func refuse(status int, reason string) out {
	return out{status, refusal{Reason: reason}}
}

func refuseWith(status int, reason string, err error) out {
	return out{status, refusal{Reason: reason, Message: err.Error()}}
}

type statusGranted struct {
	Server  bool `json:"server"`
	Browser bool `json:"browser"`
}

type statusBody struct {
	Name    string                 `json:"name"`
	Version string                 `json:"version"`
	Granted statusGranted          `json:"granted"`
	Agents  *[]pagetools.AgentInfo `json:"agents,omitempty"`
}

type grantedBody struct {
	Granted bool `json:"granted"`
}

// routes are the requests of /v1, one origin at a time asking the user. A page sees and changes only what belongs to
// its own origin.
type routes struct {
	deps Deps

	mu       sync.Mutex // guards busy, cooldown and lockout
	busy     bool
	cooldown *Cooldown
	lockout  *Lockout
}

func newRoutes(deps Deps, now func() time.Time) *routes {
	return &routes{deps: deps, cooldown: NewCooldown(cooldownFor, now), lockout: NewLockout(lockoutRefusals, lockoutWindow, now)}
}

func (r *routes) status(origin string) out {
	reg, err := r.deps.Registry()
	if err != nil {
		return failure(err)
	}
	granted := statusGranted{Server: reg.HasApp(origin) && r.deps.Linked(origin), Browser: reg.HasBrowser(origin)}
	body := statusBody{Name: "agentd", Version: r.deps.Version, Granted: granted}
	if granted.Server || granted.Browser {
		agents := append([]pagetools.AgentInfo{}, r.deps.Agents()...)
		body.Agents = &agents
	}
	return out{http.StatusOK, body}
}

func (r *routes) connect(origin string, req *http.Request) out {
	want, ok := ParseConnect(readBody(req))
	if !ok {
		return refuse(http.StatusBadRequest, "malformed")
	}
	if want.Mode == consent.ModeBrowser {
		reg, err := r.deps.Registry()
		if err != nil {
			return failure(err)
		}
		if reg.HasBrowser(origin) {
			return out{http.StatusOK, grantedBody{true}}
		}
	}
	if !r.begin(origin) {
		return r.refusedBeforeDialog(origin)
	}
	defer r.end()
	return r.decide(origin, want)
}

// begin takes the one dialog; false when the origin waits, every dialog is locked, or another dialog is open.
func (r *routes) begin(origin string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cooldown.Blocked(origin) || r.lockout.Locked() || r.busy {
		return false
	}
	r.busy = true
	return true
}

func (r *routes) refusedBeforeDialog(origin string) out {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cooldown.Blocked(origin) || r.lockout.Locked() {
		return refuse(http.StatusTooManyRequests, "cooldown")
	}
	return refuse(http.StatusConflict, "busy")
}

func (r *routes) end() {
	r.mu.Lock()
	r.busy = false
	r.mu.Unlock()
}

func (r *routes) decide(origin string, want ConnectRequest) out {
	var ready, titles []string
	for _, a := range r.deps.Agents() {
		if a.Ready {
			ready = append(ready, a.Name)
			titles = append(titles, cmp.Or(a.Title, a.Name))
		}
	}
	decision := r.deps.Ask(consent.Question{Origin: origin, Mode: want.Mode, Agents: ready, Titles: titles})
	answer := decision.Answer
	if answer == consent.AnswerUnavailable {
		return refuse(http.StatusServiceUnavailable, "no-dialog")
	}
	if answer != consent.AnswerAllow {
		r.mu.Lock()
		r.cooldown.Block(origin)
		r.lockout.Strike()
		r.mu.Unlock()
		if answer == consent.AnswerTimeout {
			return refuse(http.StatusForbidden, "timeout")
		}
		return refuse(http.StatusForbidden, "denied")
	}
	if len(decision.Folders) == 0 {
		// An Allow without a folder gives nothing to start agents in. The dialogs never produce one (a picker that
		// returns none is a Deny), so the machine's way of asking is broken: nothing is granted.
		return refuse(http.StatusServiceUnavailable, "no-dialog")
	}
	r.mu.Lock()
	r.lockout.Reset()
	r.mu.Unlock()
	var err error
	if want.Mode == consent.ModeServer {
		err = r.deps.ConnectServer(origin, want.Code, decision.Folders)
	} else {
		err = r.deps.GrantBrowser(origin, decision.Folders)
	}
	if err != nil {
		return refuseWith(http.StatusBadGateway, "pairing", err)
	}
	return out{http.StatusOK, grantedBody{true}}
}

func (r *routes) disconnect(origin string) out {
	if err := r.deps.Revoke(origin); err != nil {
		return failure(err)
	}
	return out{http.StatusOK, grantedBody{false}}
}

func failure(err error) out {
	return refuseWith(http.StatusInternalServerError, "error", err)
}

// readBody is the body of a request up to maxBody bytes; a longer one reads as nothing (malformed).
func readBody(req *http.Request) []byte {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil
	}
	return body
}

// route is the answer to a request of /v1, or false when it names none.
func (r *routes) route(origin string, req *http.Request) (out, bool) {
	switch path := req.URL.EscapedPath(); {
	case path == "/v1/status" && req.Method == http.MethodGet:
		return r.status(origin), true
	case path == "/v1/connect" && req.Method == http.MethodPost:
		return r.connect(origin, req), true
	case path == "/v1/connect" && req.Method == http.MethodDelete:
		return r.disconnect(origin), true
	}
	return out{}, false
}

// send writes an answer: JSON as JSON.stringify writes it (no HTML escaping), never cached.
func send(w http.ResponseWriter, o out, cors map[string]string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(o.body) // bodies are structs of strings and booleans: they always encode
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	for k, v := range cors {
		h.Set(k, v)
	}
	w.WriteHeader(o.status)
	_, _ = w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// server is the gateway's http.Handler: every request and upgrade waits for the peer gate first, so a connection from
// another OS user's process gets nothing.
type server struct {
	routes *routes
	deps   Deps
	gate   *consent.PeerGate
	port   int
}

type connKey struct{}

func (s *server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, _ := req.Context().Value(connKey{}).(net.Conn)
	upgrading := isUpgrade(req)
	if conn == nil || !s.gate.Allowed(conn) {
		if upgrading {
			drop(w)
		} else {
			send(w, refuse(http.StatusForbidden, "peer"), nil)
		}
		return
	}
	if upgrading {
		s.upgrade(w, req)
		return
	}
	s.handle(w, req)
}

func (s *server) handle(w http.ResponseWriter, req *http.Request) {
	if !HostAllowed(req.Host, s.port) {
		send(w, refuse(http.StatusMisdirectedRequest, "host"), nil)
		return
	}
	origin, ok := OriginOf(req.Header.Get("Origin"))
	if !ok {
		send(w, refuse(http.StatusForbidden, "origin"), nil)
		return
	}
	cors := CORSHeaders(origin, req.Header)
	if req.Method == http.MethodOptions {
		for k, v := range cors {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	o, found := s.routes.route(origin, req)
	if !found {
		o = refuse(http.StatusNotFound, "not-found")
	}
	send(w, o, cors)
}

// isUpgrade is whether the request asks to leave HTTP, as Node tells its 'upgrade' event from a request.
func isUpgrade(req *http.Request) bool {
	if req.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range req.Header.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (s *server) upgrade(w http.ResponseWriter, req *http.Request) {
	origin, ok := OriginOf(req.Header.Get("Origin"))
	if !ok || !HostAllowed(req.Host, s.port) || req.URL.EscapedPath() != "/v1/session" || s.deps.Upgrade == nil {
		drop(w)
		return
	}
	reg, err := s.deps.Registry()
	if err != nil || !reg.HasBrowser(origin) {
		drop(w)
		return
	}
	s.deps.Upgrade(origin, w, req)
}

// drop closes the connection of a request without an answer.
func drop(w http.ResponseWriter) {
	if h, ok := w.(http.Hijacker); ok {
		if conn, _, err := h.Hijack(); err == nil {
			_ = conn.Close()
		}
	}
}

// listen is the listener of the first free port of port ... port+PortRange-1 (any free port when port is 0).
func listen(port int) (net.Listener, error) {
	last := port + PortRange - 1
	if port == 0 {
		last = 0
	}
	for p := port; p <= last; p++ {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, ErrPortsTaken
}

// Start starts the gateway on 127.0.0.1: the first free port of port ... port+9 (any free port when port is 0). It
// returns ErrPortsTaken when all are in use. It answers only connections whose owners are this process's OS user
// (o.Gate).
func Start(deps Deps, port int, o Options) (*Gateway, error) {
	if o.Gate == nil {
		return nil, errors.New("gateway: Options.Gate is required")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	l, err := listen(port)
	if err != nil {
		return nil, err
	}
	bound := l.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Handler:           &server{routes: newRoutes(deps, now), deps: deps, gate: o.Gate, port: bound},
		ReadHeaderTimeout: time.Minute,
		IdleTimeout:       5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnContext:       func(ctx context.Context, c net.Conn) context.Context { return context.WithValue(ctx, connKey{}, c) },
		ConnState: func(c net.Conn, state http.ConnState) {
			// The gate's answer is per connection: a closed or upgraded one is forgotten.
			if state == http.StateClosed || state == http.StateHijacked {
				o.Gate.Forget(c)
			}
		},
	}
	g := &Gateway{Port: bound, srv: srv, done: make(chan struct{})}
	go func() {
		defer close(g.done)
		_ = srv.Serve(l)
	}()
	return g, nil
}
