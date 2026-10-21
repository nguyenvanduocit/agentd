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
	// ConnectServer pairs with the server at the page's origin using the code, saves it with its access (name, folders
	// and end) and links to it now; it fails when the app refuses.
	ConnectServer func(server, code string, access registry.Access) error
	// GrantBrowser saves the origin's grant with its access.
	GrantBrowser func(origin string, access registry.Access) error
	// Revoke forgets the browser grant and the app of `origin`, and ends their sessions.
	Revoke func(origin string) error
	// Upgrade serves the WebSocket upgrade of a browser-granted origin on /v1/session, and returns when the socket
	// is done (BrowserSessions.Upgrade). Without it every upgrade is refused.
	Upgrade func(origin string, w http.ResponseWriter, r *http.Request)
	// Acp serves the WebSocket upgrade of a checked request on the standard ACP endpoint /acp (AcpSessions.Serve), and
	// returns when the connection is done. Without it /acp does not exist.
	Acp func(t AcpTarget, w http.ResponseWriter, r *http.Request)
	// AcpToken is the token a program with no Origin header must present to use /acp (acpauth). It is asked for at each
	// request, so a new token takes effect at once. Empty means there is none, and then no such program can connect.
	AcpToken func() string
}

// Options are what Start takes besides the deps and the port.
type Options struct {
	// Gate decides which connections are of the daemon's own OS user. Required.
	Gate *consent.PeerGate
	// Now is the clock of the cooldown, the lockout and the age of a page's grant; nil is time.Now.
	Now func() time.Time
	// Say is told, in a plain line, of the refusals worth a line in the log; nil says nothing. It is never given a
	// token, a query string or anything else a client sent.
	Say func(line string)
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

// refusal is the body of a refusal. Reason is the one every page SDK knows; Code, when there is one, is the specific
// Reason it refines (see Reason). RetryAfter is the whole seconds until asking again can work, for the refusals that
// are a wait.
type refusal struct {
	Granted    bool   `json:"granted"`
	Reason     string `json:"reason"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func refusalOf(r Reason, message string) refusal {
	if legacy, refines := legacyOf[r]; refines {
		return refusal{Reason: string(legacy), Code: string(r), Message: message}
	}
	return refusal{Reason: string(r), Message: message}
}

func refuse(status int, r Reason) out {
	return out{status, refusalOf(r, "")}
}

func refuseWith(status int, r Reason, err error) out {
	return out{status, refusalOf(r, err.Error())}
}

// refuseAfter is a refusal that is a wait: wait rounded up to whole seconds, at least one.
func refuseAfter(status int, r Reason, wait time.Duration) out {
	body := refusalOf(r, "")
	body.RetryAfter = max(int((wait+time.Second-1)/time.Second), 1)
	return out{status, body}
}

type statusGranted struct {
	Server  bool `json:"server"`
	Browser bool `json:"browser"`
}

// PageProtocol is the version of the page protocol, the requests and messages of this gateway: a page SDK compares it
// with the ones it speaks. It is bumped only when the page protocol changes incompatibly, and is not the protocol an
// app's server speaks to the daemon (home.Protocol). Version 2: POST /v1/connect names the app.
const PageProtocol = 2

type statusBody struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Protocol is PageProtocol: a page reads it to know whether its SDK can talk to this daemon.
	Protocol int                    `json:"protocol"`
	Granted  statusGranted          `json:"granted"`
	Agents   *[]pagetools.AgentInfo `json:"agents,omitempty"`
}

type grantedBody struct {
	Granted bool `json:"granted"`
}

// routes are the requests of /v1, one origin at a time asking the user. A page sees and changes only what belongs to
// its own origin.
type routes struct {
	deps Deps
	now  func() time.Time

	mu       sync.Mutex // guards busy, cooldown and lockout
	busy     bool
	cooldown *Cooldown
	lockout  *Lockout
}

func newRoutes(deps Deps, now func() time.Time) *routes {
	return &routes{deps: deps, now: now, cooldown: NewCooldown(cooldownFor, now), lockout: NewLockout(lockoutRefusals, lockoutWindow, now)}
}

func (r *routes) status(origin string) out {
	reg, err := r.deps.Registry()
	if err != nil {
		return failure(err)
	}
	granted := statusGranted{Server: reg.HasApp(origin, r.now()) && r.deps.Linked(origin), Browser: reg.HasBrowser(origin, r.now())}
	body := statusBody{Name: "agentd", Version: r.deps.Version, Protocol: PageProtocol, Granted: granted}
	if granted.Server || granted.Browser {
		agents := append([]pagetools.AgentInfo{}, r.deps.Agents()...)
		body.Agents = &agents
	}
	return out{http.StatusOK, body}
}

func (r *routes) connect(origin string, req *http.Request) out {
	want, ok := ParseConnect(readBody(req))
	if !ok {
		return refuse(http.StatusBadRequest, ReasonMalformed)
	}
	if want.Mode == consent.ModeBrowser {
		reg, err := r.deps.Registry()
		if err != nil {
			return failure(err)
		}
		if reg.HasBrowser(origin, r.now()) {
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

// refusedBeforeDialog says why begin was false. A lockout is the stricter wait and is named first; either way the
// origin is told to wait for the longer of the two, so that asking again after it works.
func (r *routes) refusedBeforeDialog(origin string) out {
	r.mu.Lock()
	defer r.mu.Unlock()
	locked, own := r.lockout.Wait(), r.cooldown.Wait(origin)
	switch {
	case locked > 0:
		return refuseAfter(http.StatusTooManyRequests, ReasonLocked, max(locked, own))
	case own > 0:
		return refuseAfter(http.StatusTooManyRequests, ReasonCooldown, own)
	}
	return refuse(http.StatusConflict, ReasonBusy)
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
	q := consent.Question{Origin: origin, Name: want.Name, Mode: want.Mode, Agents: ready, Titles: titles}
	decision := r.deps.Ask(q)
	answer := decision.Answer
	if answer == consent.AnswerUnavailable {
		return refuse(http.StatusServiceUnavailable, ReasonNoDialog)
	}
	if answer != consent.AnswerAllow {
		r.mu.Lock()
		r.cooldown.Block(origin)
		r.lockout.Strike()
		r.mu.Unlock()
		if answer == consent.AnswerTimeout {
			return refuse(http.StatusForbidden, ReasonTimeout)
		}
		return refuse(http.StatusForbidden, ReasonDenied)
	}
	if len(decision.Folders) == 0 {
		// An Allow without a folder gives nothing to start agents in. The dialogs never produce one (a picker that
		// returns none is a Deny), so the machine's way of asking is broken: nothing is granted. A page that does
		// not know no-folder reads it as no-dialog, which it always did.
		return refuse(http.StatusServiceUnavailable, ReasonNoFolder)
	}
	r.mu.Lock()
	r.lockout.Reset()
	r.mu.Unlock()
	access := registry.Access{Name: want.Name, Folders: decision.Folders, ExpiresAt: registry.Expiry(r.now(), decision.LastsOrDefault())}
	var err error
	failed := ReasonPairing // the app refused the code, or could not be reached
	if want.Mode == consent.ModeServer {
		err = r.deps.ConnectServer(origin, want.Code, access)
	} else {
		err = r.deps.GrantBrowser(origin, access)
		failed = ReasonNotSaved
	}
	if err != nil {
		return refuseWith(http.StatusBadGateway, failed, err)
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
	return refuseWith(http.StatusInternalServerError, ReasonError, err)
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
	if wait, ok := o.body.(refusal); ok && wait.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(wait.RetryAfter))
		if cors != nil {
			h.Set("Access-Control-Expose-Headers", "Retry-After") // a page reads only the safelisted headers otherwise
		}
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
	// acpRefused writes the log line of a program that came to /acp without the token.
	acpRefused *refusalLog
}

type connKey struct{}

func (s *server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, _ := req.Context().Value(connKey{}).(net.Conn)
	upgrading := isUpgrade(req)
	if conn == nil || !s.gate.Allowed(conn) {
		if upgrading {
			drop(w)
		} else {
			send(w, refuse(http.StatusForbidden, ReasonPeer), nil)
		}
		return
	}
	if isAcpPath(req) {
		s.serveAcp(w, req)
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
		send(w, refuse(http.StatusMisdirectedRequest, ReasonHost), nil)
		return
	}
	origin, ok := OriginOf(req.Header.Get("Origin"))
	if !ok {
		send(w, refuse(http.StatusForbidden, ReasonOrigin), nil)
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
		o = refuse(http.StatusNotFound, ReasonNotFound)
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
	if err != nil || !reg.HasBrowser(origin, s.routes.now()) {
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
		Handler: &server{routes: newRoutes(deps, now), deps: deps, gate: o.Gate, port: bound,
			acpRefused: newRefusalLog(o.Say, now, acpRefusedEvery)},
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
