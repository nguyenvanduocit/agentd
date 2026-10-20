package goclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ErrClosed is the error of a write to a session that was closed, or whose socket ended.
var ErrClosed = errors.New("goclient: the session is closed")

// ErrNotAllowed is the error of opening a session for an origin the user has not allowed (ConnectBrowser): the daemon
// turns the socket down. errors.Is finds it in what OpenSession returns; the fix is Connect. Since 0.7.0.
var ErrNotAllowed = errors.New("goclient: the user has not allowed this app to run agents on this computer: ask with Connect")

// ErrNoDaemon is the error of opening a session when no agentd answers on the port given, or none was found. Since
// 0.7.0.
var ErrNoDaemon = errors.New("goclient: no agentd answers on this computer")

// SessionError is the daemon's refusal to open an agent: the agent is unknown, is not installed or still setting up, or
// the folder is not one the user allowed. Message is the daemon's own sentence, which is also Error(). Since 0.7.0.
type SessionError struct {
	// Agent is the agent asked for.
	Agent string
	// Reason is ReasonError: the daemon states no finer reason for a refused open; Message has the details.
	Reason  Reason
	Message string
}

func (e *SessionError) Error() string { return e.Message }

// classified is an error that is also one of this package's sentinels, with the message it had before they existed.
type classified struct {
	err  error
	kind error
}

func (e *classified) Error() string   { return e.err.Error() }
func (e *classified) Unwrap() []error { return []error{e.err, e.kind} }

// dialRefused is whether a failed dial reached a daemon that turned the socket down (an answer that is not an
// upgrade, a hang-up, a reset) as against nothing answering at all.
func dialRefused(err error, res *http.Response) bool {
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Op != "dial"
	}
	return res != nil || errors.Is(err, websocket.ErrBadHandshake) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// FrameKind says what a Frame carries.
type FrameKind string

const (
	FrameOut  FrameKind = "out"  // a line of the agent's stdout
	FrameErr  FrameKind = "err"  // stderr text
	FrameExit FrameKind = "exit" // the agent ended; Code is its exit code
)

// Frame is one thing the agent did: a stdout line, stderr text or its exit.
type Frame struct {
	Kind FrameKind
	Data string
	Code int
}

// EnvVar is one environment variable of an McpServer.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// McpServer is a stdio MCP server as ACP's session/new takes it in `mcpServers`.
type McpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []EnvVar `json:"env"`
}

// wireMcp is the `mcp` of `opened`: the stdio server through which the agent reaches the app's MCP server.
type wireMcp struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []EnvVar `json:"env"`
}

// incoming is a message of the daemon: hello, opened, error, frame or mcp (daemon/pagetools/localproto.go).
type incoming struct {
	T       string    `json:"t"`
	ID      string    `json:"id"`
	Pid     *int      `json:"pid"`
	Cwd     string    `json:"cwd"`
	Message string    `json:"message"`
	Kind    FrameKind `json:"kind"`
	Data    string    `json:"data"`
	Code    int       `json:"code"`
	Version string    `json:"version"`
	Line    string    `json:"line"`
	Mcp     *wireMcp  `json:"mcp"`
}

// lineQueue is lines as bytes, each ending in "\n", for a reader. It starts the first time it is taken and keeps no
// line from before: a session that never takes it keeps none, and one that takes it after the agent is gone gets EOF.
type lineQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	buf   []byte
	taken bool
	ended bool
}

func newLineQueue() *lineQueue {
	q := &lineQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *lineQueue) take() {
	q.mu.Lock()
	q.taken = true
	q.mu.Unlock()
}

func (q *lineQueue) line(data string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.taken || q.ended {
		return
	}
	q.buf = append(q.buf, data...)
	q.buf = append(q.buf, '\n')
	q.cond.Broadcast()
}

func (q *lineQueue) end() {
	q.mu.Lock()
	q.ended = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Read blocks until a line is there, and answers EOF once the queue has ended and what it holds is read.
func (q *lineQueue) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.buf) == 0 && !q.ended {
		q.cond.Wait()
	}
	if len(q.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, q.buf)
	q.buf = q.buf[n:]
	if len(q.buf) == 0 {
		q.buf = nil
	}
	return n, nil
}

// lineWriter sends bytes on as lines: each complete line is one send, empty lines are dropped, and a partial line
// waits for its newline. Close sends the partial line left.
type lineWriter struct {
	mu     sync.Mutex
	rest   []byte
	closed bool
	send   func(line string) error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.rest = append(w.rest, p...)
	for {
		i := bytes.IndexByte(w.rest, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.rest[:i])
		w.rest = w.rest[i+1:]
		if line == "" {
			continue
		}
		if err := w.send(line); err != nil {
			return 0, err
		}
	}
}

// Close sends the line still waiting for its newline (the last line has none left to wait for).
func (w *lineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	line := string(w.rest)
	w.rest = nil
	if line == "" {
		return nil
	}
	return w.send(line)
}

// McpStream is the app's end of the bridge between the agent and the app's own MCP server, as an MCP transport takes
// it: Read gives the lines of the agent's MCP client, each ending in "\n" (it ends with the session); Write takes the
// lines of the app's MCP server, and each complete line goes to the agent's MCP client. Close sends the partial line
// left and ends the writes; it does not end the session.
type McpStream struct {
	in  *lineQueue
	out *lineWriter
}

func (m *McpStream) Read(p []byte) (int, error)  { return m.in.Read(p) }
func (m *McpStream) Write(p []byte) (int, error) { return m.out.Write(p) }
func (m *McpStream) Close() error                { return m.out.Close() }

// Session is one agent running on the daemon, with a socket of its own; the app is the ACP client of its lines.
type Session struct {
	// Pid is the agent's process (group) id on the daemon's computer; -1 when the daemon told none.
	Pid int
	// Cwd is the agent's working directory.
	Cwd string

	id      string
	version string
	mcp     *wireMcp
	conn    *websocket.Conn

	wmu    sync.Mutex // one write at a time on the socket
	closed atomic.Bool
	done   chan struct{}

	exited   chan struct{} // closed once the agent's exit frame has been told to the callbacks
	exitOnce sync.Once
	exitCode int // the agent's exit code, set before exited is closed

	stdout, mcpIn *lineQueue
	stdin         *lineWriter
	mcpOut        *lineWriter

	mu     sync.Mutex // guards frames
	frames []func(Frame)
}

func (s *Session) send(m any) error {
	if s.closed.Load() {
		return ErrClosed
	}
	return s.sendRaw(m)
}

func (s *Session) sendRaw(m any) error {
	text := stringify(m)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, []byte(text))
}

type toDaemon struct {
	T     string `json:"t"`
	ID    string `json:"id"`
	Agent string `json:"agent,omitempty"`
	Line  string `json:"line,omitempty"`
}

// Write sends one line to the agent's stdin (without its newline).
func (s *Session) Write(line string) error {
	return s.send(toDaemon{T: "write", ID: s.id, Line: line})
}

// WriteLine is Write under the name a reader of the call expects: one line, without its newline, to the agent's stdin.
// Since 0.7.0.
func (s *Session) WriteLine(line string) error { return s.Write(line) }

// Stdout is the agent's stdout, each line ending in "\n"; it ends (EOF) when the agent exits or the socket closes. It
// starts the first time it is called and keeps no line from before: call it right after the session opens (an ACP agent
// speaks only when asked). Every call answers the same reader.
func (s *Session) Stdout() io.Reader {
	s.stdout.take()
	return s.stdout
}

// Stdin is the agent's stdin: each complete line written is one Write, empty lines are dropped, and a partial line
// waits for its newline. Closing it sends the line still waiting; it does not end the session.
func (s *Session) Stdin() io.WriteCloser { return s.stdin }

// OnFrame calls cb with every frame the agent sends from now on: its stdout lines, its stderr text and its exit. The
// callbacks run in order on the session's reader goroutine, so a slow one holds the session up.
func (s *Session) OnFrame(cb func(Frame)) {
	s.mu.Lock()
	s.frames = append(s.frames, cb)
	s.mu.Unlock()
}

// Done is closed when the socket ended: the agent is gone with it. It does not close when the agent exits and the
// socket stays: use Wait, or the exit Frame, to know that the agent ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// Wait blocks until the agent exits and answers its exit code, as exec.Cmd.Wait does for a process. When the socket
// ends first (Close, the daemon going away) the code is -1 and the error wraps ErrClosed. Every call answers the same.
// The OnFrame callbacks have seen the exit frame by the time Wait returns. Since 0.7.0.
func (s *Session) Wait() (code int, err error) {
	select {
	case <-s.exited:
		return s.exitCode, nil
	case <-s.done:
		select {
		case <-s.exited: // the exit frame was the last thing on the socket
			return s.exitCode, nil
		default:
		}
		return -1, fmt.Errorf("goclient: the socket ended before the agent exited: %w", ErrClosed)
	}
}

// Mcp is the app's own MCP server's end of the bridge to the agent. Like Stdout it starts the first time it is
// called and keeps no line from before: call it before session/new lists McpServer(name).
func (s *Session) Mcp() *McpStream {
	s.mcpIn.take()
	return &McpStream{in: s.mcpIn, out: s.mcpOut}
}

// McpServer is the MCP server, named name, to list in session/new's `mcpServers`: the agent then reaches the app's
// server through Mcp. It fails on a daemon that does not bridge an app's MCP server (agentd 0.3 and older). Each call
// answers a copy.
func (s *Session) McpServer(name string) (McpServer, error) {
	if s.mcp == nil {
		return McpServer{}, fmt.Errorf("agentd %s does not bridge a page's MCP server: update agentd on this computer", s.version)
	}
	server := McpServer{Name: name, Command: s.mcp.Command, Args: append([]string{}, s.mcp.Args...), Env: append([]EnvVar{}, s.mcp.Env...)}
	return server, nil
}

// Close ends the session: the daemon ends the agent with the socket. It is safe to call more than once.
func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = s.sendRaw(toDaemon{T: "close", ID: s.id})
	s.wmu.Lock()
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	s.wmu.Unlock()
	err := s.conn.Close()
	s.stdout.end()
	s.mcpIn.end()
	return err
}

// run reads the socket until it ends, and hands each message of this session on.
func (s *Session) run() {
	defer func() {
		s.closed.Store(true)
		s.stdout.end()
		s.mcpIn.end()
		_ = s.conn.Close()
		close(s.done)
	}()
	for {
		m, err := readMessage(s.conn)
		if err != nil {
			return
		}
		if m.ID != s.id {
			continue
		}
		switch m.T {
		case "mcp":
			s.mcpIn.line(m.Line)
		case "frame":
			f := Frame{Kind: m.Kind, Data: m.Data, Code: m.Code}
			if f.Kind == "" {
				f.Kind = FrameOut
			}
			switch f.Kind {
			case FrameOut:
				s.stdout.line(f.Data)
			case FrameExit:
				s.stdout.end()
				s.mcpIn.end()
			}
			s.mu.Lock()
			cbs := append([]func(Frame){}, s.frames...)
			s.mu.Unlock()
			for _, cb := range cbs {
				cb(f)
			}
			if f.Kind == FrameExit {
				s.exitOnce.Do(func() {
					s.exitCode = f.Code
					close(s.exited)
				})
			}
		}
	}
}

// readMessage reads the next JSON text message; a message that is not one is skipped, a socket that ended is an error.
func readMessage(conn *websocket.Conn) (incoming, error) {
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return incoming{}, err
		}
		var m incoming
		if kind == websocket.TextMessage && json.Unmarshal(data, &m) == nil {
			return m, nil
		}
	}
}

// newID is a random UUID (version 4) for a session.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("goclient: no randomness: " + err.Error()) // crypto/rand does not fail on a working system
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// OpenSession opens agent on the daemon over a socket of its own, for the app at origin (which the user must have
// allowed with ConnectBrowser); the app is the ACP client of the lines. ctx covers opening only: the session lives
// until Close, the agent's exit or the socket's end.
//
// The errors tell what to do: errors.Is(err, ErrNotAllowed) is a daemon that turned the socket down (ask with
// ConnectBrowser), errors.Is(err, ErrNoDaemon) nothing answering on the port, a *OriginError an origin the gateway
// would refuse, and a *SessionError the daemon's refusal of the agent.
func OpenSession(ctx context.Context, d Daemon, origin, agent string, opts ...Option) (*Session, error) {
	if err := ValidateOrigin(origin); err != nil {
		return nil, err
	}
	o := newOptions(opts)
	header := http.Header{}
	header.Set("Origin", origin)
	conn, res, err := o.dialer.DialContext(ctx, "ws://127.0.0.1:"+strconv.Itoa(d.Port)+"/v1/session", header)
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		if dialRefused(err, res) {
			return nil, &classified{fmt.Errorf("cannot open a session: the daemon refused the connection (is this origin allowed?): %w", err), ErrNotAllowed}
		}
		return nil, &classified{fmt.Errorf("cannot open a session: no agentd answers on port %d (is it running?): %w", d.Port, err), ErrNoDaemon}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	s, err := handshake(conn, newID(), agent)
	if !stop() { // ctx ended first: its watcher closed the socket
		_ = conn.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	go s.run()
	return s, nil
}

// handshake waits for `hello`, asks to open the agent, and waits for `opened` or the daemon's refusal.
func handshake(conn *websocket.Conn, id, agent string) (*Session, error) {
	s := &Session{id: id, conn: conn, version: "(unknown version)", done: make(chan struct{}), exited: make(chan struct{}), stdout: newLineQueue(), mcpIn: newLineQueue()}
	s.stdin = &lineWriter{send: s.Write}
	s.mcpOut = &lineWriter{send: func(line string) error { return s.send(toDaemon{T: "mcp", ID: id, Line: line}) }}
	hello := false
	for {
		m, err := readMessage(conn)
		if err != nil {
			return nil, fmt.Errorf("the daemon closed the session: %w", err)
		}
		switch {
		case m.T == "hello" && !hello:
			hello = true
			if m.Version != "" {
				s.version = m.Version
			}
			if err := s.sendRaw(toDaemon{T: "open", ID: id, Agent: agent}); err != nil {
				return nil, err
			}
		case m.ID != id:
		case m.T == "error":
			return nil, &SessionError{Agent: agent, Reason: ReasonError, Message: m.Message}
		case m.T == "opened":
			s.Pid = -1
			if m.Pid != nil {
				s.Pid = *m.Pid
			}
			s.Cwd = m.Cwd
			s.mcp = m.Mcp
			return s, nil
		}
	}
}
