package pagemcp

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// tokenBytes is 256 random bits: the token is all that tells one page session's bridge from another's.
const tokenBytes = 32

// writeTimeout is how long a line for a bridge may wait for the bridge to read it; past it the bridge is dropped.
const writeTimeout = 30 * time.Second

// drainTimeout is how long a refused bridge may keep its connection open after the refusal.
const drainTimeout = 5 * time.Second

const noSessionMessage = "no open session has this token: its page or session closed, or it was never one"

// McpPage is the page end of one session's bridge: where the agent's MCP lines go. Send may be called from the
// relay's goroutines.
type McpPage interface {
	Send(line string)
	// Backlog is nil while the page has room, else a channel that is closed once it has room again. The bridge is not
	// read meanwhile (as agents.BackloggedSink).
	Backlog() <-chan struct{}
}

// bridgeConn is the connection of one bridge. Its lines are written whole, one at a time.
type bridgeConn struct {
	conn net.Conn
	gone chan struct{} // closed with conn

	wmu  sync.Mutex // one write at a time
	once sync.Once
}

func newBridgeConn(conn net.Conn) *bridgeConn {
	return &bridgeConn{conn: conn, gone: make(chan struct{})}
}

func (c *bridgeConn) close() {
	c.once.Do(func() {
		close(c.gone)
		_ = c.conn.Close()
	})
}

// write sends text to the bridge; false when it could not (the bridge is then closed).
func (c *bridgeConn) write(text string) bool {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write([]byte(text)); err != nil {
		c.close()
		return false
	}
	return true
}

type bound struct {
	page   McpPage
	bridge *bridgeConn // nil while no bridge is connected
}

// Binding is one page session's claim on the relay; it lasts until End.
type Binding struct {
	relay *McpRelay
	token string
	b     *bound
}

// Token is what the session's bridge says in its hello.
func (b *Binding) Token() string { return b.token }

// ToBridge sends one line of the page's MCP server to the bridge; false when no bridge is connected.
func (b *Binding) ToBridge(line string) bool {
	b.relay.mu.Lock()
	bridge := b.b.bridge
	b.relay.mu.Unlock()
	if bridge == nil {
		return false
	}
	return bridge.write(line + "\n")
}

// End: the session or its page is gone. It closes the session's bridge and refuses its token from now on.
func (b *Binding) End() {
	r := b.relay
	r.mu.Lock()
	delete(r.bound, b.token)
	bridge := b.b.bridge
	b.b.bridge = nil
	r.mu.Unlock()
	if bridge != nil {
		bridge.close()
	}
}

// McpRelay is the daemon's Unix socket for `agentd-mcp --page`: a bridge sends its session's token, then each line it
// reads goes to that session's page and each line of the page goes back to it. The socket lives in the daemon's own
// temporary directory (mode 0700), so only this OS user reaches it; the token picks the page session. It opens no
// port.
type McpRelay struct {
	path     string
	listener net.Listener
	maxLine  int

	mu     sync.Mutex
	bound  map[string]*bound
	conns  map[*bridgeConn]struct{} // every open connection, claimed or not
	closed bool

	wg sync.WaitGroup
}

// Listen starts the relay on the Unix socket `path`; it fails when it cannot (a path too long for a socket, a
// directory that is gone, a socket already there).
func Listen(path string) (*McpRelay, error) {
	return listen(path, proc.MaxLineBytes)
}

// listen is Listen with a cap on the lines a bridge sends.
func listen(path string, maxLine int) (*McpRelay, error) {
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	r := &McpRelay{
		path:     path,
		listener: l,
		maxLine:  maxLine,
		bound:    map[string]*bound{},
		conns:    map[*bridgeConn]struct{}{},
	}
	r.wg.Add(1)
	go r.acceptLoop()
	return r, nil
}

// Path is the socket's path, as a bridge gets it in McpSocketEnv.
func (r *McpRelay) Path() string { return r.path }

// Bind gives a page session a token of its own.
func (r *McpRelay) Bind(page McpPage) *Binding {
	raw := make([]byte, tokenBytes)
	_, _ = rand.Read(raw) // crypto/rand.Read does not fail
	token := base64.RawURLEncoding.EncodeToString(raw)
	b := &bound{page: page}
	r.mu.Lock()
	r.bound[token] = b
	r.mu.Unlock()
	return &Binding{relay: r, token: token, b: b}
}

// Close ends every bridge and stops listening.
func (r *McpRelay) Close() {
	r.mu.Lock()
	r.closed = true
	r.bound = map[string]*bound{}
	conns := make([]*bridgeConn, 0, len(r.conns))
	for c := range r.conns {
		conns = append(conns, c)
	}
	r.mu.Unlock()
	_ = r.listener.Close()
	for _, c := range conns {
		c.close()
	}
	r.wg.Wait()
}

func (r *McpRelay) acceptLoop() {
	defer r.wg.Done()
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		c := newBridgeConn(conn)
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			c.close()
			return
		}
		r.conns[c] = struct{}{}
		r.wg.Add(1)
		r.mu.Unlock()
		go r.serve(c)
	}
}

// serve reads one bridge: its hello, then its lines for the page.
func (r *McpRelay) serve(c *bridgeConn) {
	defer r.wg.Done()
	var claimed *bound
	defer func() {
		r.mu.Lock()
		delete(r.conns, c)
		if claimed != nil && claimed.bridge == c {
			claimed.bridge = nil
		}
		r.mu.Unlock()
		c.close()
	}()

	splitter := proc.NewLineSplitter(r.maxLine)
	buf := make([]byte, 64*1024)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			for _, line := range splitter.Push(buf[:n]) {
				if claimed != nil {
					if strings.TrimSpace(line) != "" {
						claimed.page.Send(line)
					}
					continue
				}
				if claimed = r.claim(c, line); claimed == nil {
					r.drain(c)
					return
				}
			}
			// The bridge cuts its own lines at the cap: a longer one is not from a bridge.
			if splitter.TooLong() {
				return
			}
			if claimed != nil && !waitForRoom(c, claimed.page) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// claim is the session of the hello `line`, which c now bridges (a bridge before it is closed); nil when it is refused
// (the refusal is already sent).
func (r *McpRelay) claim(c *bridgeConn, line string) *bound {
	// The answer is written before any line of the page: ToBridge waits on wmu until it is out.
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var b *bound
	var older *bridgeConn
	if token, ok := ParseBridgeHello(line); ok {
		r.mu.Lock()
		if b = r.bound[token]; b != nil {
			older, b.bridge = b.bridge, c
		}
		r.mu.Unlock()
	}
	if b == nil {
		r.writeLocked(c, BridgeRefusal(noSessionMessage)+"\n")
		return nil
	}
	if older != nil {
		older.close()
	}
	if !r.writeLocked(c, BridgeOK()+"\n") {
		r.mu.Lock()
		if b.bridge == c {
			b.bridge = nil
		}
		r.mu.Unlock()
		return nil
	}
	return b
}

// writeLocked is c.write for a caller that holds c.wmu.
func (r *McpRelay) writeLocked(c *bridgeConn, text string) bool {
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write([]byte(text)); err != nil {
		return false
	}
	return true
}

// drain ends a refused connection: the refusal is sent, the bridge may still be writing and its bytes are ignored
// until it closes (or drainTimeout passes).
func (r *McpRelay) drain(c *bridgeConn) {
	if u, ok := c.conn.(*net.UnixConn); ok {
		_ = u.CloseWrite()
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(drainTimeout))
	buf := make([]byte, 4096)
	for {
		if _, err := c.conn.Read(buf); err != nil {
			return
		}
	}
}

// waitForRoom stops reading the bridge while its page is behind; false when the bridge went away meanwhile.
func waitForRoom(c *bridgeConn, page McpPage) bool {
	room := page.Backlog()
	if room == nil {
		return true
	}
	select {
	case <-room:
		return true
	case <-c.gone:
		return false
	}
}
