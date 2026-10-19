// The daemon's Socket.IO client: Engine.IO v4 and Socket.IO v5 over one WebSocket, for one namespace, written
// against gorilla/websocket.
//
// Decision (spike; TestOwnClientConnectsOnceEvery200Times is its regression test): the daemon does not use
// github.com/zishang520/socket.io/clients/socket/v3, the library client in go.mod. hub/fakedaemon_test.go:19-26
// documents that it sends the namespace CONNECT twice or never; the second CONNECT makes the hub replace the
// daemon's first socket and close the transport the two share. The spike connected 200 clients (websocket
// transport only, one auth token each, an in-process hub counting the handshakes per token), three runs each:
//
//	library, one client at a time:     200/200 connected, every client exactly 1 handshake (3 runs).
//	library, 20 clients at a time:     run 1: 199 connected, 1 with no CONNECT ack in 3 s (0 handshakes), 2 with 2;
//	                                   run 2: 200 connected, 2 with 2 handshakes; run 3: 200 connected, 1 with 2.
//	                                   In all, 5 of 600 clients sent a duplicate CONNECT and 1 of 600 sent none.
//	this client, 20 clients at a time: every client exactly 1 handshake and connected (the test asserts it).
//
// A watchdog over the library (reconnect when no CONNECT ack comes, ignore a duplicate) heals both failures, but a
// duplicate has already dropped the connection by then, and the library keeps the ack timeout of a call in a flag
// of the socket that concurrent callers share (contract/server.generated.go:14-18). This client is a few hundred
// lines, sends CONNECT once by construction, and the Link's single reconnect loop is the only retry there is.
//
// Wire format (https://socket.io/docs/v4/socket-io-protocol/): a text WebSocket message is one Engine.IO packet,
// a type digit then data. "0{json}" is the server's open (sid, pingInterval, pingTimeout), "2" a ping to answer
// with "3", "1" a close. Type "4" carries a Socket.IO packet: "40/ns,{auth}" connect, "40/ns,{sid}" its answer,
// "44/ns,{message,data}" a refusal, "41/ns," disconnect, "42/ns,<id>[name,args...]" an event, "43/ns,<id>[args...]"
// its acknowledgement. The daemon sends and receives no binary attachments.
package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Why a connection ended, in the words socket.io-client uses for its `disconnect` event.
const (
	reasonServer    = "io server disconnect"
	reasonClient    = "io client disconnect"
	reasonPing      = "ping timeout"
	reasonTransport = "transport close"
	reasonParse     = "parse error"
)

// maxMessageBytes is the largest message either side sends: hub.MaxMessageBytes (a test holds the two equal).
const maxMessageBytes = 64 << 20

// writeTimeout bounds one write: a peer that stays alive for pings but takes nothing is a dead connection.
const writeTimeout = 2 * time.Minute

var (
	errTimedOut     = errors.New("operation has timed out")
	errDisconnected = errors.New("socket has been disconnected")
)

// refusal is the server answering the namespace CONNECT with an error: the handshake was refused.
type refusal struct{ message string }

func (r *refusal) Error() string { return r.message }

// packet is a decoded Socket.IO packet.
type packet struct {
	kind byte            // '0' connect, '1' disconnect, '2' event, '3' ack, '4' connect error
	id   int             // -1 when the packet has none
	data json.RawMessage // the JSON after the id
}

// parsePacket decodes one Socket.IO packet of namespace ns (s starts with its type digit); a packet of another
// namespace is ok=false.
func parsePacket(s, ns string) (p packet, ok bool, err error) {
	if s == "" {
		return packet{}, false, errors.New("empty packet")
	}
	p = packet{kind: s[0], id: -1}
	if p.kind < '0' || p.kind > '4' {
		return packet{}, false, fmt.Errorf("unsupported packet type %q", s[0])
	}
	rest := s[1:]
	got := "/"
	if strings.HasPrefix(rest, "/") {
		comma := strings.IndexByte(rest, ',')
		if comma < 0 {
			got, rest = rest, ""
		} else {
			got, rest = rest[:comma], rest[comma+1:]
		}
	}
	if got != ns {
		return packet{}, false, nil
	}
	if p.kind == '2' || p.kind == '3' {
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits > 0 {
			n, err := strconv.Atoi(rest[:digits])
			if err != nil {
				return packet{}, false, fmt.Errorf("bad packet id: %w", err)
			}
			p.id, rest = n, rest[digits:]
		}
	}
	p.data = json.RawMessage(rest)
	return p, true, nil
}

// encodePacket is the text of a Socket.IO event ('2') or acknowledgement ('3') of namespace ns, as an Engine.IO
// message; an id below zero sends none.
func encodePacket(kind byte, ns string, id int, values []any) (string, error) {
	var b strings.Builder
	b.WriteString("4")
	b.WriteByte(kind)
	if ns != "/" {
		b.WriteString(ns)
		b.WriteByte(',')
	}
	if id >= 0 {
		b.WriteString(strconv.Itoa(id))
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false) // as JSON.stringify writes it
	if err := enc.Encode(values); err != nil {
		return "", err
	}
	b.Write(bytes.TrimRight(body.Bytes(), "\n"))
	return b.String(), nil
}

// conn is one connected Socket.IO socket. Its reads belong to serve; everything else may be called from any goroutine.
type conn struct {
	ws  *websocket.Conn
	ns  string
	ctx context.Context // done when the connection is
	end context.CancelFunc

	// pingWindow is how long the server may stay silent: its ping interval and ping timeout.
	pingWindow time.Duration

	wmu sync.Mutex // one writer at a time

	mu     sync.Mutex
	reason string // why the connection ended; "" while it lives
	nextID int
	acks   map[int]chan []json.RawMessage
}

// dialConn opens the WebSocket of the Socket.IO endpoint at origin and joins namespace ns with auth. It returns once
// the server accepted the namespace; a *refusal when it did not; another error when the connection could not be
// made before ctx ended.
func dialConn(ctx context.Context, origin, ns string, auth any) (*conn, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/socket.io/"
	u.RawQuery = "EIO=4&transport=websocket"
	dialer := &websocket.Dialer{NetDialContext: (&net.Dialer{}).DialContext}
	ws, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxMessageBytes)
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	c, err := join(ctx, ws, ns, auth)
	if !stop() && err == nil {
		err = ctx.Err() // the context ended as the handshake finished: the socket is closed
	}
	if err != nil {
		_ = ws.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return c, nil
}

// openPacket is the server's Engine.IO open.
type openPacket struct {
	PingInterval float64 `json:"pingInterval"`
	PingTimeout  float64 `json:"pingTimeout"`
}

// join runs the handshake on a fresh WebSocket: the Engine.IO open, then the Socket.IO CONNECT for ns, once.
func join(ctx context.Context, ws *websocket.Conn, ns string, auth any) (*conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = ws.SetReadDeadline(deadline)
		_ = ws.SetWriteDeadline(deadline)
	}
	first, err := readText(ws)
	if err != nil {
		return nil, err
	}
	if first == "" || first[0] != '0' {
		return nil, fmt.Errorf("the server did not open an Engine.IO session: %.80q", first)
	}
	var open openPacket
	if err := json.Unmarshal([]byte(first[1:]), &open); err != nil {
		return nil, fmt.Errorf("bad Engine.IO open packet: %w", err)
	}
	connect := "40"
	if ns != "/" {
		connect += ns + ","
	}
	body, err := json.Marshal(auth)
	if err != nil {
		return nil, err
	}
	if err := ws.WriteMessage(websocket.TextMessage, append([]byte(connect), body...)); err != nil {
		return nil, err
	}
	for {
		msg, err := readText(ws)
		if err != nil {
			return nil, err
		}
		if msg == "" {
			continue
		}
		switch msg[0] {
		case '2': // a ping before the namespace answered
			if err := ws.WriteMessage(websocket.TextMessage, []byte("3")); err != nil {
				return nil, err
			}
		case '1':
			return nil, errors.New("the server closed the Engine.IO session during the handshake")
		case '4':
			p, ok, err := parsePacket(msg[1:], ns)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			switch p.kind {
			case '0':
				return newConn(ws, ns, open), nil
			case '4':
				return nil, &refusal{message: refusalMessage(p.data)}
			}
		}
	}
}

// refusalMessage is the message of a CONNECT_ERROR packet's data ({"message":..,"data":..}), or the string it is.
func refusalMessage(data json.RawMessage) string {
	var v struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &v); err == nil && v.Message != "" {
		return v.Message
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s
	}
	return string(data)
}

func readText(ws *websocket.Conn) (string, error) {
	kind, msg, err := ws.ReadMessage()
	if err != nil {
		return "", err
	}
	if kind != websocket.TextMessage {
		return "", errors.New("the server sent a binary message")
	}
	return string(msg), nil
}

func newConn(ws *websocket.Conn, ns string, open openPacket) *conn {
	ctx, end := context.WithCancel(context.Background())
	window := time.Duration(open.PingInterval+open.PingTimeout) * time.Millisecond
	if window <= 0 {
		window = 45 * time.Second
	}
	_ = ws.SetWriteDeadline(time.Time{})
	return &conn{ws: ws, ns: ns, ctx: ctx, end: end, pingWindow: window, acks: map[int]chan []json.RawMessage{}}
}

// finish ends the connection for reason (the first reason stays) and closes the transport; the calls that wait for an
// answer fail. It returns the reason the connection ended for.
func (c *conn) finish(reason string) string {
	c.mu.Lock()
	if c.reason == "" {
		c.reason = reason
	}
	reason = c.reason
	c.mu.Unlock()
	c.end()
	_ = c.ws.Close()
	return reason
}

// leave says goodbye the way a client does (the namespace DISCONNECT, then the WebSocket close) and leaves the
// reading to serve, which ends when the server has closed its side: what was written before has been taken by
// then. The reason stays "io client disconnect" whatever ends the connection after this.
func (c *conn) leave() {
	c.mu.Lock()
	if c.reason == "" {
		c.reason = reasonClient
	}
	c.mu.Unlock()
	_ = c.write("41" + c.nsPrefix())
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

func (c *conn) nsPrefix() string {
	if c.ns == "/" {
		return ""
	}
	return c.ns + ","
}

// write sends one Engine.IO message.
func (c *conn) write(text string) error {
	if c.ctx.Err() != nil {
		return errDisconnected
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.ws.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		c.finish(reasonTransport)
		return fmt.Errorf("%w: %w", errDisconnected, err)
	}
	return nil
}

// emit sends the event name with args and does not wait for an answer.
func (c *conn) emit(name string, args ...any) error {
	text, err := encodePacket('2', c.ns, -1, append([]any{name}, args...))
	if err != nil {
		return err
	}
	return c.write(text)
}

// call sends the event name with args and returns the values the server acknowledged with. It ends with errTimedOut
// when ctx's deadline passes first, ctx.Err() when ctx is cancelled, errDisconnected when the connection ends.
func (c *conn) call(ctx context.Context, name string, args ...any) ([]json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	answer := make(chan []json.RawMessage, 1)
	c.acks[id] = answer
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.acks, id)
		c.mu.Unlock()
	}()
	text, err := encodePacket('2', c.ns, id, append([]any{name}, args...))
	if err != nil {
		return nil, err
	}
	if err := c.write(text); err != nil {
		return nil, err
	}
	select {
	case values := <-answer:
		return values, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errTimedOut
		}
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, errDisconnected
	}
}

// serve reads until the connection ends and returns why. An event from the server goes to onEvent with its arguments
// and, when the server wants an answer, the function that sends it; onEvent must not block.
func (c *conn) serve(onEvent func(name string, args []json.RawMessage, ack func(values ...any))) string {
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(c.pingWindow))
		kind, msg, err := c.ws.ReadMessage()
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				return c.finish(reasonPing)
			}
			return c.finish(reasonTransport)
		}
		if kind != websocket.TextMessage {
			return c.finish(reasonParse)
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case '1':
			return c.finish(reasonTransport)
		case '2':
			if c.write("3") != nil {
				return c.finish(reasonTransport)
			}
		case '4':
			if reason := c.handle(string(msg[1:]), onEvent); reason != "" {
				return c.finish(reason)
			}
		}
	}
}

// handle takes one Socket.IO packet; a non-empty result is the reason the connection must end.
func (c *conn) handle(text string, onEvent func(string, []json.RawMessage, func(...any))) string {
	p, ok, err := parsePacket(text, c.ns)
	if err != nil {
		return reasonParse
	}
	if !ok {
		return ""
	}
	switch p.kind {
	case '1':
		return reasonServer
	case '2':
		var parts []json.RawMessage
		var name string
		if json.Unmarshal(p.data, &parts) != nil || len(parts) == 0 || json.Unmarshal(parts[0], &name) != nil {
			return reasonParse
		}
		var ack func(...any)
		if p.id >= 0 {
			ack = func(values ...any) { c.acknowledge(p.id, values) }
		}
		onEvent(name, parts[1:], ack)
	case '3':
		var values []json.RawMessage
		if json.Unmarshal(p.data, &values) != nil {
			return reasonParse
		}
		c.mu.Lock()
		answer := c.acks[p.id]
		c.mu.Unlock()
		if answer != nil {
			select {
			case answer <- values:
			default: // answered twice: the first answer stands
			}
		}
	}
	return ""
}

func (c *conn) acknowledge(id int, values []any) {
	text, err := encodePacket('3', c.ns, id, values)
	if err != nil {
		return
	}
	_ = c.write(text)
}
