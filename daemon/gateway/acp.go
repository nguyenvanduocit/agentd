package gateway

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// The standard ACP endpoint: `GET /acp/<agent>` upgraded to a WebSocket, as the WebSocket profile of ACP's transports
// RFD (https://agentclientprotocol.com/rfds/streamable-http-websocket-transport) describes it. Every message is one
// text frame of JSON-RPC, the first one is the client's `initialize`, and the connection's end is the end of its
// sessions. The RFD names no way to choose among several agents, frames nothing about a trailing newline, and gives no
// close codes: agentd names the agent in the path, tolerates one trailing newline, and uses the close codes below.
//
// Who may connect (the front door of the gateway, the same-user check, the Host check, has been passed): a request
// with an Origin header is a web page, which needs the live browser grant /v1/session needs, and is held to the same
// rules (page guard, the grant's folders). A request with none is not a browser: a browser always sends Origin on a
// WebSocket handshake and script cannot remove it. It is a program on this computer, or something that reaches
// 127.0.0.1 through a forwarder running as the user: a dev container, a sandboxed app, an `ssh -R` tunnel. The
// same-user check cannot tell those apart, and the second kind could not start an agent on the host itself, so a
// program must present the local token (acpauth), which lives in a file only the user's own processes on the host
// read. With it, it gets no dialog and no folder limit: it could start the adapter itself.

const (
	// MaxAcpConnections is how many /acp connections may be open in all.
	MaxAcpConnections = 8
	// MaxAcpPerOrigin is how many of them one web page may hold.
	MaxAcpPerOrigin = 4

	// acpPingEvery and acpPongWithin keep a dead client from holding an agent: a ping goes out every acpPingEvery,
	// and a connection that sends nothing, not even a pong, for acpPongWithin is dropped. The RFD leaves keepalive to
	// the implementer.
	acpPingEvery  = 30 * time.Second
	acpPongWithin = 60 * time.Second
	// acpWriteWithin is how long one message may take to reach a client that does not read: past it the client is
	// cut loose and its agent ended.
	acpWriteWithin = 30 * time.Second
	// acpEndGrace is how long ending a connection waits for its agent to be gone before it kills it.
	acpEndGrace = 3 * time.Second

	// acpInboxBytes and acpInboxMessages bound what may wait for an agent that does not read its input.
	acpInboxBytes    = 16 << 20
	acpInboxMessages = 1024

	// acpReasonBytes is the longest close reason a WebSocket close frame can carry.
	acpReasonBytes = 123
)

// AcpTarget is what a checked /acp request asks for: the agent, and the web page's origin (empty for a program on
// this computer).
type AcpTarget struct {
	Agent  string
	Origin string
}

func (t AcpTarget) local() bool { return t.Origin == "" }

// AcpOptions are what AcpSessions need.
type AcpOptions struct {
	// Fleet is the fleet of each connection; Sink, Folders and Ended are the connection's own.
	Fleet agents.FleetOptions
	// Folders are the folders the user allowed a web page, read when they matter; nil sets no limit.
	Folders func(origin string) ([]string, error)
	// MaxMessageBytes is the largest message a client may send; zero is the longest line (proc.MaxLineBytes).
	MaxMessageBytes int
	// MaxConnections and MaxPerOrigin are the limits; zero is MaxAcpConnections and MaxAcpPerOrigin.
	MaxConnections, MaxPerOrigin int
	// PingEvery, PongWithin and WriteWithin replace the defaults (tests).
	PingEvery, PongWithin, WriteWithin time.Duration
}

// AcpSessions are the open /acp connections. Each has its own fleet with one agent, ended when the connection is.
type AcpSessions struct {
	o        AcpOptions
	upgrader websocket.Upgrader

	mu        sync.Mutex
	open      []*acpEntry
	total     int // admitted, until the agent is gone
	perOrigin map[string]int
	closed    bool
}

// NewAcpSessions makes sessions with no connection open.
func NewAcpSessions(o AcpOptions) *AcpSessions {
	if o.Fleet.Log == nil {
		o.Fleet.Log = proc.NewLogger(io.Discard, io.Discard)
	}
	o.MaxMessageBytes = cmp.Or(o.MaxMessageBytes, proc.MaxLineBytes)
	o.MaxConnections = cmp.Or(o.MaxConnections, MaxAcpConnections)
	o.MaxPerOrigin = cmp.Or(o.MaxPerOrigin, MaxAcpPerOrigin)
	o.PingEvery = cmp.Or(o.PingEvery, acpPingEvery)
	o.PongWithin = cmp.Or(o.PongWithin, acpPongWithin)
	o.WriteWithin = cmp.Or(o.WriteWithin, acpWriteWithin)
	return &AcpSessions{
		o: o,
		// The gateway checked Host and Origin before it got here; the library's own check would compare Origin with Host.
		upgrader:  websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		perOrigin: map[string]int{},
	}
}

func (s *AcpSessions) entries() []*acpEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.open)
}

// Size is the number of agents running for /acp connections.
func (s *AcpSessions) Size() int {
	total := 0
	for _, e := range s.entries() {
		total += e.fleet.Size()
	}
	return total
}

// Origins are the web pages with an open connection (a program on this computer has none).
func (s *AcpSessions) Origins() []string {
	var origins []string
	for _, e := range s.entries() {
		if !e.target.local() && !slices.Contains(origins, e.target.Origin) {
			origins = append(origins, e.target.Origin)
		}
	}
	return origins
}

// EndOrigin closes every connection of the web page `origin`: its access was withdrawn.
func (s *AcpSessions) EndOrigin(origin string) {
	for _, e := range s.entries() {
		if e.target.Origin == origin && !e.target.local() {
			e.sock.finish(websocket.ClosePolicyViolation, "this site's access to agentd ended")
			time.AfterFunc(2*time.Second, func() { _ = e.conn.Close() }) // a client that does not read still lets go
		}
	}
}

// Drain refuses new connections; the open ones go on. Shutdown calls it too.
func (s *AcpSessions) Drain() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Shutdown refuses new connections, closes the open ones with 1001 and ends their agents; it returns once they are
// gone or after grace.
func (s *AcpSessions) Shutdown(grace time.Duration) {
	s.Drain()
	var wg sync.WaitGroup
	for _, e := range s.entries() {
		e.sock.finish(websocket.CloseGoingAway, "agentd is stopping")
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.fleet.Shutdown(grace)
			select { // the close frame goes out before the socket is cut
			case <-e.sock.done:
			case <-time.After(time.Second):
			}
			_ = e.conn.Close()
		}()
	}
	wg.Wait()
}

// admit takes a place for one more connection; release gives it back. A refusal is the answer to send instead.
func (s *AcpSessions) admit(origin string) (release func(), refused *out) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		o := out{http.StatusServiceUnavailable, refusalOf(ReasonError, "agentd is stopping")}
		return nil, &o
	case s.total >= s.o.MaxConnections, origin != "" && s.perOrigin[origin] >= s.o.MaxPerOrigin:
		o := out{http.StatusTooManyRequests, refusalOf(ReasonBusy, fmt.Sprintf("at most %d ACP connections at once (%d for one site)", s.o.MaxConnections, s.o.MaxPerOrigin))}
		return nil, &o
	}
	s.total++
	if origin != "" {
		s.perOrigin[origin]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.total--
			if origin != "" {
				if s.perOrigin[origin]--; s.perOrigin[origin] <= 0 {
					delete(s.perOrigin, origin)
				}
			}
		})
	}, nil
}

// Serve completes the WebSocket handshake of a request the gateway already checked, and serves the connection until
// it ends.
func (s *AcpSessions) Serve(t AcpTarget, w http.ResponseWriter, r *http.Request) {
	release, refused := s.admit(t.Origin)
	if refused != nil {
		send(w, *refused, nil)
		return
	}
	id := newConnectionID()
	conn, err := s.upgrader.Upgrade(w, r, http.Header{"Acp-Connection-Id": {id}})
	if err != nil {
		release()
		return // the library answered the request
	}
	s.serve(conn, id, t, release)
}

func (s *AcpSessions) serve(conn *websocket.Conn, id string, t AcpTarget, release func()) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &acpEntry{s: s, id: id, target: t, conn: conn, cancel: cancel, done: make(chan struct{}), in: newAcpInbox()}
	e.sock = newPageSocketWithin(conn, s.o.WriteWithin)
	fleet := s.o.Fleet
	fleet.Sink = func() agents.FrameSink { return acpSink{e} }
	fleet.Folders = nil
	if !t.local() && s.o.Folders != nil {
		fleet.Folders = func() ([]string, error) { return s.o.Folders(t.Origin) }
	}
	e.fleet = agents.NewFleet(fleet)
	defer func() {
		e.end()
		release()
	}()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		e.sock.finish(websocket.CloseGoingAway, "agentd is stopping")
		<-e.sock.done
		return
	}
	s.open = append(s.open, e)
	s.mu.Unlock()

	who := "a local program"
	if !t.local() {
		who = t.Origin
	}
	s.o.Fleet.Log.Say(fmt.Sprintf("acp connection %s: %s for %s", id, t.Agent, who))

	if _, err := e.fleet.OpenOn(ctx, id, t.Agent); err != nil {
		e.sock.finish(websocket.CloseInternalServerErr, clipReason(err.Error(), acpReasonBytes))
		<-e.sock.done
		return
	}
	go e.pump()
	go e.pings()
	e.read()
}

// forget removes e from the open connections.
func (s *AcpSessions) forget(e *acpEntry) {
	s.mu.Lock()
	s.open = slices.DeleteFunc(s.open, func(o *acpEntry) bool { return o == e })
	s.mu.Unlock()
}

// acpEntry is one connection: a socket, and the one agent behind it.
type acpEntry struct {
	s      *AcpSessions
	id     string
	target AcpTarget
	conn   *websocket.Conn
	sock   *pageSocket
	fleet  *agents.Fleet
	in     *acpInbox
	cancel context.CancelFunc
	done   chan struct{} // closed when the connection has ended

	mu     sync.Mutex
	stderr []byte // the last of what the agent wrote on stderr
}

// end: the socket is gone, and with it the client's way to reach the agent. It returns once the agent is gone.
func (e *acpEntry) end() {
	e.cancel()
	e.sock.close()
	close(e.done)
	e.fleet.Shutdown(acpEndGrace)
	e.in.close()
	e.s.forget(e)
}

// read takes the client's messages until the connection ends.
func (e *acpEntry) read() {
	limits := e.s.o
	e.conn.SetReadLimit(int64(limits.MaxMessageBytes))
	extend := func() { _ = e.conn.SetReadDeadline(time.Now().Add(limits.PongWithin)) }
	extend()
	e.conn.SetPongHandler(func(string) error { extend(); return nil })
	for {
		kind, data, err := e.conn.ReadMessage()
		if err != nil {
			if errors.Is(err, websocket.ErrReadLimit) { // the library has sent 1009
				_ = e.conn.SetReadDeadline(time.Now().Add(lingerAfterTooBig))
				_, _ = io.Copy(io.Discard, e.conn.UnderlyingConn())
			}
			return
		}
		extend()
		if kind != websocket.TextMessage {
			continue // "Binary frames are ignored."
		}
		line, problem := acpLine(data)
		if problem != "" {
			e.sock.finish(websocket.CloseInvalidFramePayloadData, problem)
			<-e.sock.done
			return
		}
		if line == "" {
			continue
		}
		if !e.in.push(line) {
			e.sock.finish(websocket.CloseTryAgainLater, "the agent is not reading its input")
			<-e.sock.done
			return
		}
	}
}

// pump hands the client's messages to the agent in order. It is a goroutine of its own so that an agent that does not
// read its input never stops the socket from answering pings and closes.
func (e *acpEntry) pump() {
	for {
		select {
		case <-e.in.wake:
		case <-e.done:
			return
		}
		for {
			line, ok := e.in.pop()
			if !ok {
				break
			}
			e.deliver(line)
		}
	}
}

// deliver gives one message to the agent, unless the rules of a web page refuse it. The folder limit of a page's grant
// is the fleet's (Fleet.Write refuses a place outside it with a JSON-RPC error, which goes back to the client).
func (e *acpEntry) deliver(line string) {
	if !e.target.local() {
		// There is no bridge to the page's tools on /acp, so a page may list no MCP server and set no _meta.
		if reply, why, refused := agents.GuardPageLine(agents.PageBridge{}, line); refused {
			if reply != "" {
				e.sock.sendText([]byte(reply))
			} else {
				e.sock.finish(websocket.ClosePolicyViolation, clipReason("not passed to the agent: "+why, acpReasonBytes))
			}
			return
		}
	}
	e.fleet.Write(e.id, line)
}

// pings keeps asking the client whether it is there.
func (e *acpEntry) pings() {
	tick := time.NewTicker(e.s.o.PingEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := e.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(e.s.o.PongWithin)); err != nil {
				return
			}
		case <-e.done:
			return
		}
	}
}

func (e *acpEntry) noteStderr(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stderr = append(e.stderr, text...)
	if len(e.stderr) > 1024 {
		e.stderr = e.stderr[len(e.stderr)-1024:]
	}
}

// lastWords is the last line the agent wrote on stderr, without control characters.
func (e *acpEntry) lastWords() string {
	e.mu.Lock()
	text := strings.TrimSpace(strings.ToValidUTF8(string(e.stderr), "?"))
	e.mu.Unlock()
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(text))
}

// acpSink is where the agent's output goes: its stdout lines are the client's messages, its stderr is its own log,
// and its end is the end of the connection.
type acpSink struct{ e *acpEntry }

func (k acpSink) Send(_ string, kind agents.FrameKind, data string, code int) {
	switch kind {
	case agents.FrameOut:
		if line := strings.TrimRight(data, "\r"); line != "" {
			k.e.sock.sendText([]byte(line))
		}
	case agents.FrameErr:
		k.e.noteStderr(data)
	case agents.FrameExit:
		k.e.sock.finish(websocket.CloseNormalClosure, acpExitReason(code, k.e.lastWords()))
	}
}

func (k acpSink) Backlog() <-chan struct{} { return k.e.sock.backlog() }

// acpInbox is what a client sent that the agent has not yet been given: bounded, so a client cannot make the daemon
// hold more than it can say.
type acpInbox struct {
	wake chan struct{}

	mu     sync.Mutex
	lines  []string
	bytes  int
	closed bool
}

func newAcpInbox() *acpInbox { return &acpInbox{wake: make(chan struct{}, 1)} }

// push queues line; false when too much already waits. The bound is on what waits behind a message, not on the message:
// an empty inbox takes any message the read limit let in (up to 32 MiB, twice acpInboxBytes), so that one large message
// reaches an agent that reads. It is the second one, with the first still unread, that shows an agent that does not.
func (b *acpInbox) push(line string) bool {
	b.mu.Lock()
	if b.closed || len(b.lines) >= acpInboxMessages || (len(b.lines) > 0 && b.bytes+len(line) > acpInboxBytes) {
		b.mu.Unlock()
		return false
	}
	b.lines = append(b.lines, line)
	b.bytes += len(line)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *acpInbox) pop() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return "", false
	}
	line := b.lines[0]
	b.lines[0] = ""
	b.lines = b.lines[1:]
	b.bytes -= len(line)
	return line, true
}

func (b *acpInbox) close() {
	b.mu.Lock()
	b.closed = true
	b.lines = nil
	b.bytes = 0
	b.mu.Unlock()
}

// acpLine is the message in one text frame: at most one trailing newline is dropped, and a newline or carriage return
// anywhere else, or text that is not UTF-8, is a problem (the stdio framing agents speak allows neither: a message is
// one line, and two in one frame would be two messages that nobody checked).
func acpLine(data []byte) (line, problem string) {
	if !utf8.Valid(data) {
		return "", "a message is UTF-8"
	}
	text := string(data)
	switch {
	case strings.HasSuffix(text, "\r\n"):
		text = text[:len(text)-2]
	case strings.HasSuffix(text, "\n"):
		text = text[:len(text)-1]
	}
	if strings.ContainsAny(text, "\r\n") {
		return "", "a message is one line"
	}
	return text, ""
}

// acpRouted is how a path and query of /acp name an agent.
type acpRouted int

const (
	acpNamed    acpRouted = iota // one known agent
	acpMissing                   // none: /acp
	acpUnknown                   // a name that is no agent, or a path that is no /acp path
	acpConflict                  // two different names
)

// routeAcp reads `/acp/<agent>` or `/acp?agent=<agent>` (both may be given when they agree).
func routeAcp(escapedPath string, query url.Values) (agent string, routed acpRouted) {
	rest, ok := strings.CutPrefix(escapedPath, "/acp")
	if !ok {
		return "", acpUnknown
	}
	var fromPath string
	switch {
	case rest == "" || rest == "/":
	case strings.HasPrefix(rest, "/") && !strings.Contains(rest[1:], "/"):
		fromPath = rest[1:]
	default:
		return "", acpUnknown
	}
	names := query["agent"]
	if len(names) > 1 || (fromPath != "" && len(names) == 1 && names[0] != fromPath) {
		return "", acpConflict
	}
	agent = fromPath
	if agent == "" && len(names) == 1 {
		agent = names[0]
	}
	switch {
	case agent == "":
		return "", acpMissing
	case !agents.IsAgentName(agent):
		return "", acpUnknown
	}
	return agent, acpNamed
}

// acpOrigin reads the Origin header of an /acp request: a page's origin, or none for a program on this computer. A
// header that repeats, is empty or `null`, or is not an origin in its serialized form is refused (ok is false).
func acpOrigin(h http.Header) (origin string, local, ok bool) {
	switch values := h.Values("Origin"); len(values) {
	case 0:
		return "", true, true
	case 1:
		origin, ok = OriginOf(values[0])
		return origin, false, ok
	}
	return "", false, false
}

// newConnectionID is a random version 4 UUID, the value of Acp-Connection-Id.
func newConnectionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// acpExitReason is the close reason of a connection whose agent ended; a failure adds the last thing the agent said.
func acpExitReason(code int, lastWords string) string {
	reason := fmt.Sprintf("agent exited with code %d", code)
	if code != 0 && lastWords != "" {
		reason += ": " + lastWords
	}
	return clipReason(reason, acpReasonBytes)
}

// clipReason cuts s to at most max bytes, on a character boundary.
func clipReason(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// acpChoice is the answer to a request that names no agent: the ready ones.
type acpChoice struct {
	refusal
	Agents []string `json:"agents"`
}

// isAcpPath is whether the request is for the endpoint.
func isAcpPath(req *http.Request) bool {
	path := req.URL.EscapedPath()
	return path == "/acp" || strings.HasPrefix(path, "/acp/")
}

// serveAcp is the gateway's part of /acp, after the peer check: Host, Origin, the grant of a web page or the token of a
// program, the method, the upgrade, the agent and whether it is ready, in that order. A caller that is not let in learns
// nothing about the agents.
func (s *server) serveAcp(w http.ResponseWriter, req *http.Request) {
	if !HostAllowed(req.Host, s.port) {
		send(w, refuse(http.StatusMisdirectedRequest, ReasonHost), nil)
		return
	}
	origin, local, ok := acpOrigin(req.Header)
	if !ok {
		send(w, refuse(http.StatusForbidden, ReasonOrigin), nil)
		return
	}
	if s.deps.Acp == nil {
		send(w, refuse(http.StatusNotFound, ReasonNotFound), nil)
		return
	}
	if local {
		// Before the method, the path, the agent list, the readiness of an agent and the connection limits: a caller
		// without the token learns nothing and takes no place among the connections.
		if !s.acpTokenOK(req) {
			s.acpRefused.refused(acpRefusedLine)
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentd"`)
			send(w, out{http.StatusUnauthorized, refusalOf(ReasonDenied, acpNeedsToken)}, nil)
			return
		}
	} else {
		reg, err := s.deps.Registry()
		if err != nil {
			send(w, failure(err), nil)
			return
		}
		if !reg.HasBrowser(origin, s.routes.now()) {
			send(w, out{http.StatusForbidden, refusalOf(ReasonDenied, "this site has no access yet: ask first with POST /v1/connect")}, nil)
			return
		}
	}
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		send(w, out{http.StatusMethodNotAllowed, refusalOf(ReasonMalformed, "the ACP endpoint takes GET with a WebSocket upgrade")}, nil)
		return
	}
	if !isUpgrade(req) {
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		send(w, out{http.StatusUpgradeRequired, refusalOf(ReasonMalformed, "this is a WebSocket endpoint: connect with Upgrade: websocket")}, nil)
		return
	}
	agent, routed := routeAcp(req.URL.EscapedPath(), req.URL.Query())
	switch routed {
	case acpMissing:
		var ready []string
		for _, a := range s.deps.Agents() {
			if a.Ready {
				ready = append(ready, a.Name)
			}
		}
		choice := acpChoice{refusal: refusalOf(ReasonMalformed, "say which agent: /acp/claude, /acp/codex or /acp/grok"), Agents: append([]string{}, ready...)}
		send(w, out{http.StatusBadRequest, choice}, nil)
		return
	case acpConflict:
		send(w, out{http.StatusBadRequest, refusalOf(ReasonMalformed, "the agent is named twice and differently")}, nil)
		return
	case acpUnknown:
		send(w, refuse(http.StatusNotFound, ReasonNotFound), nil)
		return
	}
	var info *pagetools.AgentInfo
	for _, a := range s.deps.Agents() {
		if a.Name == agent {
			info = &a
			break
		}
	}
	switch {
	case info == nil:
		send(w, refuse(http.StatusNotFound, ReasonNotFound), nil)
		return
	case !info.Ready:
		send(w, out{http.StatusServiceUnavailable, refusalOf(ReasonError, cmp.Or(info.Reason, agent+" is not ready"))}, nil)
		return
	}
	s.deps.Acp(AcpTarget{Agent: agent, Origin: origin}, w, req)
}
