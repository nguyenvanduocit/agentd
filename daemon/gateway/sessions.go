package gateway

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// MaxSessionsPerSocket is how many sessions one page may keep: a page chooses what runs on the machine, so the
// number is bounded.
const MaxSessionsPerSocket = 5

// maxPageMessageBytes is the largest message a page may send: a `write` (or `mcp`) of a line at the cap, every byte of
// it escaped, with room for the envelope. A prompt with images of several MB is far below it.
const maxPageMessageBytes = 2*proc.MaxLineBytes + 64*1024

// pauseBufferedBytes is how much a page's socket may have waiting to go out before its agents' output (and their
// bridges' lines) is paused. A page that reads keeps it near zero (the socket is local); this much is a page that
// stopped (a frozen or throttled tab) and would otherwise make the daemon hold all the agents write. It still holds a
// few large messages, so a burst does not pause anything.
const pauseBufferedBytes = 8 * 1024 * 1024

// resumeBufferedBytes is where paused output resumes: well below the pause, so it does not flap.
const resumeBufferedBytes = 1024 * 1024

// lingerAfterTooBig is how long a socket that sent a message past the limit may keep sending after the daemon's close
// frame: closing at once would reset the connection and the page would never read why.
const lingerAfterTooBig = 2 * time.Second

// pageSocket is a page's socket as the agents' sink: it sends messages in order, and tells the agents to wait while
// too much is unsent.
type pageSocket struct {
	conn *websocket.Conn
	wake chan struct{} // a message is queued
	done chan struct{} // closed with the socket

	mu       sync.Mutex
	queue    [][]byte
	buffered int
	closed   bool
	room     chan struct{} // set while the agents wait; closed when they may go on
}

func newPageSocket(conn *websocket.Conn) *pageSocket {
	p := &pageSocket{conn: conn, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go p.writeLoop()
	return p
}

// send queues m; a closed socket drops it.
func (p *pageSocket) send(m pagetools.FromDaemon) {
	data := []byte(pagetools.Encode(m))
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.queue = append(p.queue, data)
	p.buffered += len(data)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pageSocket) pop() ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return nil, false
	}
	data := p.queue[0]
	p.queue[0] = nil
	p.queue = p.queue[1:]
	return data, true
}

// sent is a message gone to the kernel: the socket is that much emptier.
func (p *pageSocket) sent(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buffered -= n
	if p.buffered <= resumeBufferedBytes {
		p.releaseLocked()
	}
}

func (p *pageSocket) writeLoop() {
	for {
		select {
		case <-p.wake:
		case <-p.done:
			return
		}
		for {
			data, ok := p.pop()
			if !ok {
				break
			}
			err := p.conn.WriteMessage(websocket.TextMessage, data)
			p.sent(len(data))
			if err != nil {
				p.close()
				return
			}
		}
	}
}

// backlog is nil while the socket has room, else a channel that is closed once it has room again.
func (p *pageSocket) backlog() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.room == nil && p.buffered > pauseBufferedBytes {
		p.room = make(chan struct{})
	}
	return p.room
}

func (p *pageSocket) releaseLocked() {
	if p.room != nil {
		close(p.room)
		p.room = nil
	}
}

// close ends the socket: what is unsent is dropped and the agents that wait go on (they are being ended).
func (p *pageSocket) close() {
	p.mu.Lock()
	first := !p.closed
	p.closed = true
	p.queue = nil
	p.buffered = 0
	p.releaseLocked()
	p.mu.Unlock()
	if first {
		close(p.done)
	}
	_ = p.conn.Close()
}

// frameSink is where a session's frames go. The frames of a session start after its `opened` went out: a session
// that is opening holds the lock.
type frameSink struct {
	p       *pageSocket
	opening *sync.RWMutex
}

func (s frameSink) Send(id string, kind agents.FrameKind, data string, code int) {
	s.opening.RLock()
	defer s.opening.RUnlock()
	s.p.send(pagetools.NewFrame(id, kind, data, code))
}

func (s frameSink) Backlog() <-chan struct{} { return s.p.backlog() }

// bridgePage is the page end of one session's bridge.
type bridgePage struct {
	p  *pageSocket
	id string
}

func (b bridgePage) Send(line string)         { b.p.send(pagetools.NewMcp(b.id, line)) }
func (b bridgePage) Backlog() <-chan struct{} { return b.p.backlog() }

// lanes run jobs one after another per key, each lane on its own goroutine while it has work: the socket's read loop
// never waits on an agent that does not read its stdin.
type lanes struct {
	mu      sync.Mutex
	pending map[string][]func()
}

func (l *lanes) run(key string, job func()) {
	l.mu.Lock()
	jobs, running := l.pending[key]
	l.pending[key] = append(jobs, job)
	l.mu.Unlock()
	if !running {
		go l.drain(key)
	}
}

func (l *lanes) drain(key string) {
	for {
		l.mu.Lock()
		jobs := l.pending[key]
		if len(jobs) == 0 {
			delete(l.pending, key)
			l.mu.Unlock()
			return
		}
		job := jobs[0]
		l.pending[key] = jobs[1:]
		l.mu.Unlock()
		job()
	}
}

// SessionOptions are what BrowserSessions need.
type SessionOptions struct {
	// Fleet is the fleet of each socket; Sink and Ended are the sessions' own.
	Fleet agents.FleetOptions
	Hello func() pagetools.Hello
	// Relay is the socket the agents' `agentd-mcp --page` bridges reach, closed by Shutdown; without it `opened`
	// names no MCP server.
	Relay *pagetools.McpRelay
	// BridgeCommand is the absolute path of the pages' `agentd-mcp` wrapper, the command `opened` names.
	BridgeCommand string
	// Folders are the folders the user allowed the origin, read when they matter (agents.FleetOptions.Folders); nil
	// sets no limit.
	Folders func(origin string) ([]string, error)
}

// pageEntry is one page's socket: its agents, and the bridges of their sessions to the page's MCP server.
type pageEntry struct {
	conn    *websocket.Conn
	page    *pageSocket
	fleet   *agents.Fleet
	origin  string
	opening sync.RWMutex
	lanes   lanes

	mu      sync.Mutex // guards bridges and ended
	bridges map[string]*pagetools.Binding
	ended   bool
}

func (e *pageEntry) binding(id string) *pagetools.Binding {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bridges[id]
}

// unbind ends the bridge of session id.
func (e *pageEntry) unbind(id string) {
	e.mu.Lock()
	b := e.bridges[id]
	delete(e.bridges, id)
	e.mu.Unlock()
	if b != nil {
		b.End()
	}
}

// BrowserSessions are the sockets of pages that were allowed to run agents; each socket has its own fleet, ended when
// it closes.
type BrowserSessions struct {
	o        SessionOptions
	upgrader websocket.Upgrader

	mu     sync.Mutex
	open   []*pageEntry
	closed bool
}

// NewBrowserSessions makes sessions with no socket open.
func NewBrowserSessions(o SessionOptions) *BrowserSessions {
	if o.Fleet.Log == nil {
		o.Fleet.Log = proc.NewLogger(io.Discard, io.Discard)
	}
	return &BrowserSessions{
		o: o,
		// The gateway checked Host and Origin before it got here; the library's own check would compare Origin with Host.
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

func (s *BrowserSessions) entries() []*pageEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.open)
}

// Size is the number of agents running for pages.
func (s *BrowserSessions) Size() int {
	total := 0
	for _, e := range s.entries() {
		total += e.fleet.Size()
	}
	return total
}

// Origins are the origins with an open socket.
func (s *BrowserSessions) Origins() []string {
	var origins []string
	for _, e := range s.entries() {
		if !slices.Contains(origins, e.origin) {
			origins = append(origins, e.origin)
		}
	}
	return origins
}

// EndOrigin closes every socket of `origin`, which ends the agents on them: the page was disconnected.
func (s *BrowserSessions) EndOrigin(origin string) {
	for _, e := range s.entries() {
		if e.origin == origin {
			_ = e.conn.Close()
		}
	}
}

// Shutdown closes every socket (an upgraded socket belongs to no server that could close it) and the relay, and ends
// the agents; it returns once they are gone or after graceMs. No socket opens after this.
func (s *BrowserSessions) Shutdown(grace time.Duration) {
	s.mu.Lock()
	s.closed = true
	all := slices.Clone(s.open)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range all {
		_ = e.conn.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.fleet.Shutdown(grace)
		}()
	}
	wg.Wait()
	if s.o.Relay != nil {
		s.o.Relay.Close()
	}
}

// Upgrade completes the WebSocket handshake of a request the gateway already checked, and serves the socket until it
// closes.
func (s *BrowserSessions) Upgrade(origin string, w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		drop(w)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the library answered the request
	}
	s.o.Fleet.Log.Say(origin + " opened a browser session")
	s.serve(conn, origin)
}

func (s *BrowserSessions) serve(conn *websocket.Conn, origin string) {
	conn.SetReadLimit(maxPageMessageBytes)
	e := &pageEntry{conn: conn, page: newPageSocket(conn), origin: origin, bridges: map[string]*pagetools.Binding{}, lanes: lanes{pending: map[string][]func(){}}}
	sink := frameSink{p: e.page, opening: &e.opening}
	fleet := s.o.Fleet
	fleet.Sink = func() agents.FrameSink { return sink }
	fleet.Ended = e.unbind
	if s.o.Folders != nil {
		fleet.Folders = func() ([]string, error) { return s.o.Folders(origin) }
	}
	e.fleet = agents.NewFleet(fleet)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		e.page.close()
		return
	}
	s.open = append(s.open, e)
	s.mu.Unlock()
	defer s.end(e)

	e.page.send(s.o.Hello())
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if errors.Is(err, websocket.ErrReadLimit) {
				_ = conn.SetReadDeadline(time.Now().Add(lingerAfterTooBig))
				_, _ = io.Copy(io.Discard, conn.UnderlyingConn())
			}
			return
		}
		s.handle(e, string(data))
	}
}

// end: the socket is gone, and with it the page's way to reach its agents.
func (s *BrowserSessions) end(e *pageEntry) {
	e.fleet.EndAll()
	e.mu.Lock()
	e.ended = true
	ids := make([]string, 0, len(e.bridges))
	for id := range e.bridges {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.unbind(id)
	}
	e.page.close()
	s.mu.Lock()
	s.open = slices.DeleteFunc(s.open, func(o *pageEntry) bool { return o == e })
	s.mu.Unlock()
}

func (s *BrowserSessions) handle(e *pageEntry, text string) {
	m, ok := pagetools.ParseToDaemon(text)
	if !ok {
		e.page.send(pagetools.NewError("", "expected a JSON message: open, write, close or mcp"))
		return
	}
	switch m.T {
	case pagetools.ToOpen:
		s.openSession(e, m.ID, m.Agent)
	case pagetools.ToWrite:
		e.lanes.run("write:"+m.ID, func() {
			if !e.fleet.Write(m.ID, m.Line) {
				e.page.send(pagetools.NewError(m.ID, fmt.Sprintf("session %s is not running", m.ID)))
			}
		})
	case pagetools.ToClose:
		e.fleet.Close(m.ID)
	case pagetools.ToMcp:
		e.lanes.run("mcp:"+m.ID, func() {
			if b := e.binding(m.ID); b == nil || !b.ToBridge(m.Line) {
				e.page.send(pagetools.NewError(m.ID, fmt.Sprintf("session %s has no MCP bridge connected", m.ID)))
			}
		})
	}
}

func (s *BrowserSessions) openSession(e *pageEntry, id, agent string) {
	if e.fleet.Size() >= MaxSessionsPerSocket {
		e.page.send(pagetools.NewError(id, fmt.Sprintf("a page may keep at most %d sessions", MaxSessionsPerSocket)))
		return
	}
	e.opening.Lock()
	defer e.opening.Unlock()
	o, err := e.fleet.Open(id, agent)
	if err != nil {
		e.page.send(pagetools.NewError(id, err.Error()))
		return
	}
	e.page.send(pagetools.NewOpened(id, o.PID, o.Cwd, s.bridgeFor(e, id)))
}

// bridgeFor is the stdio server through which session `id`'s agent reaches the page's MCP server: a token of its own,
// bound to this page socket and session, that ends with either. Nil when the daemon has no relay socket.
func (s *BrowserSessions) bridgeFor(e *pageEntry, id string) *pagetools.McpServer {
	relay := s.o.Relay
	if relay == nil {
		return nil
	}
	b := relay.Bind(bridgePage{p: e.page, id: id})
	e.mu.Lock()
	e.bridges[id] = b
	gone := e.ended
	e.mu.Unlock()
	if gone {
		b.End()
	}
	server := pagetools.PageMcpServer(s.o.BridgeCommand, relay.Path(), b.Token())
	return &server
}
