package link

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

const waitLimit = 5 * time.Second

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fastBackoffOptions retries within milliseconds, so that a test of reconnecting does not wait for the real delays.
func fastBackoffOptions() home.BackoffOptions {
	return home.BackoffOptions{Base: 5 * time.Millisecond, Max: 20 * time.Millisecond, Jitter: 0.5}
}

// event is one event a client sent the fake server.
type event struct {
	name string
	args []json.RawMessage
	id   int // -1 when the client wants no answer
	raw  string
}

// fakeServer speaks the server side of the /agentd namespace by hand, so that a test sees the bytes the daemon
// sends and controls what the server does: refuse, answer, end the socket, fall silent.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	refuse    string  // when set, the answer to every CONNECT is a refusal with this message
	protocol  float64 // the protocol the hello answer names
	silent    bool    // new connections send no pings
	noHello   bool    // hello is not answered
	conns     []*fakeConn
	handshake []map[string]any // the auth of every CONNECT
}

type fakeConn struct {
	s  *fakeServer
	ws *websocket.Conn

	wmu sync.Mutex

	mu     sync.Mutex
	ready  bool // the CONNECT was accepted
	events []event
	nextID int
	acks   map[int]chan []json.RawMessage
	closed chan struct{}
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	s := &fakeServer{t: t, protocol: 1}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/socket.io/") || r.URL.Query().Get("EIO") != "4" || r.URL.Query().Get("transport") != "websocket" {
			http.Error(w, "not the websocket transport of Engine.IO v4", http.StatusBadRequest)
			return
		}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.serve(ws)
	}))
	t.Cleanup(func() {
		s.srv.Close()
		for _, c := range s.connections() {
			_ = c.ws.Close()
		}
	})
	return s
}

func (s *fakeServer) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *fakeServer) connections() []*fakeConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fakeConn(nil), s.conns...)
}

// handshakes is how many CONNECT packets the server has seen.
func (s *fakeServer) handshakes() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.handshake...)
}

// last is the newest connection that was accepted, once there is one.
func (s *fakeServer) last(n int) *fakeConn {
	s.t.Helper()
	var c *fakeConn
	waitFor(s.t, fmt.Sprintf("connection %d", n), func() bool {
		conns := s.connections()
		if len(conns) < n {
			return false
		}
		c = conns[n-1]
		return c.accepted()
	})
	return c
}

func (s *fakeServer) serve(ws *websocket.Conn) {
	c := &fakeConn{s: s, ws: ws, acks: map[int]chan []json.RawMessage{}, closed: make(chan struct{})}
	s.mu.Lock()
	silent := s.silent
	s.mu.Unlock()
	c.write(`0{"sid":"fake","upgrades":[],"pingInterval":100,"pingTimeout":100,"maxPayload":1000000}`)
	if !silent {
		go func() {
			for {
				select {
				case <-c.closed:
					return
				case <-time.After(40 * time.Millisecond):
					c.write("2")
				}
			}
		}()
	}
	defer close(c.closed)
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			return
		}
		text := string(msg)
		switch {
		case text == "3":
		case strings.HasPrefix(text, "40"):
			c.connect(text)
		case strings.HasPrefix(text, "4"):
			c.take(text[1:])
		}
	}
}

func (c *fakeConn) connect(text string) {
	var auth map[string]any
	_, body, _ := strings.Cut(text, ",")
	_ = json.Unmarshal([]byte(body), &auth)
	c.s.mu.Lock()
	c.s.handshake = append(c.s.handshake, auth)
	refuse := c.s.refuse
	if refuse == "" {
		c.s.conns = append(c.s.conns, c)
	}
	c.s.mu.Unlock()
	if refuse != "" {
		c.write(fmt.Sprintf(`44/agentd,{"message":%q,"data":{"code":%q}}`, refuse, refuse))
		return
	}
	c.write(`40/agentd,{"sid":"ns"}`)
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
}

func (c *fakeConn) accepted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *fakeConn) take(text string) {
	p, ok, err := parsePacket(text, "/agentd")
	if err != nil || !ok {
		return
	}
	switch p.kind {
	case '2':
		var parts []json.RawMessage
		if json.Unmarshal(p.data, &parts) != nil || len(parts) == 0 {
			return
		}
		var name string
		_ = json.Unmarshal(parts[0], &name)
		c.mu.Lock()
		c.events = append(c.events, event{name: name, args: parts[1:], id: p.id, raw: "4" + text})
		c.mu.Unlock()
		if name == "hello" && p.id >= 0 {
			c.s.mu.Lock()
			skip, protocol := c.s.noHello, c.s.protocol
			c.s.mu.Unlock()
			if !skip {
				c.write(fmt.Sprintf(`43/agentd,%d[{"protocol":%v}]`, p.id, protocol))
			}
		}
	case '3':
		var values []json.RawMessage
		_ = json.Unmarshal(p.data, &values)
		c.mu.Lock()
		ch := c.acks[p.id]
		c.mu.Unlock()
		if ch != nil {
			ch <- values
		}
	}
}

func (c *fakeConn) write(text string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, []byte(text))
}

// emit sends the daemon an event.
func (c *fakeConn) emit(name string, args ...any) {
	text, err := encodePacket('2', "/agentd", -1, append([]any{name}, args...))
	if err != nil {
		c.s.t.Fatal(err)
	}
	c.write(text)
}

// call sends the daemon an event that wants an answer and returns the answer's values.
func (c *fakeConn) call(name string, args ...any) []json.RawMessage {
	c.s.t.Helper()
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan []json.RawMessage, 1)
	c.acks[id] = ch
	c.mu.Unlock()
	text, err := encodePacket('2', "/agentd", id, append([]any{name}, args...))
	if err != nil {
		c.s.t.Fatal(err)
	}
	c.write(text)
	select {
	case values := <-ch:
		return values
	case <-time.After(waitLimit):
		c.s.t.Fatalf("no answer to %s", name)
		return nil
	}
}

// disconnect ends the namespace the way a server does.
func (c *fakeConn) disconnect() { c.write("41/agentd,") }

// kill breaks the connection without a word.
func (c *fakeConn) kill() { _ = c.ws.UnderlyingConn().Close() }

func (c *fakeConn) sent(name string) []event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []event
	for _, e := range c.events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

func (c *fakeConn) waitEvents(name string, n int) []event {
	c.s.t.Helper()
	var out []event
	waitFor(c.s.t, fmt.Sprintf("%d %q event(s)", n, name), func() bool {
		out = c.sent(name)
		return len(out) >= n
	})
	return out
}

func (c *fakeConn) gone() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// deadAddress is an address nothing listens on.
func deadAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

// events records what a Link reports.
type events struct {
	mu         sync.Mutex
	connects   int
	reasons    []string
	troubles   []string
	rejections []string
	giveUps    int
}

func (e *events) set() Events {
	return Events{
		OnConnect:    func() { e.mu.Lock(); e.connects++; e.mu.Unlock() },
		OnDisconnect: func(r string) { e.mu.Lock(); e.reasons = append(e.reasons, r); e.mu.Unlock() },
		OnTrouble:    func(m string) { e.mu.Lock(); e.troubles = append(e.troubles, m); e.mu.Unlock() },
		OnRejected:   func(m string) { e.mu.Lock(); e.rejections = append(e.rejections, m); e.mu.Unlock() },
		OnGiveUp:     func() { e.mu.Lock(); e.giveUps++; e.mu.Unlock() },
	}
}

func (e *events) connected() int { e.mu.Lock(); defer e.mu.Unlock(); return e.connects }
func (e *events) disconnected() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.reasons...)
}
func (e *events) trouble() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.troubles...)
}
func (e *events) rejected() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.rejections...)
}
func (e *events) gaveUp() int { e.mu.Lock(); defer e.mu.Unlock(); return e.giveUps }
