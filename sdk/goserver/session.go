package goserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
)

// stderrTailBytes is how much of an agent's standard error a session keeps.
const stderrTailBytes = 8 << 10

// closeLinger is how long a closed session waits for the daemon's exit report before it gives up on it.
const closeLinger = 10 * time.Second

// writeTimeout bounds one line sent to the daemon.
const writeTimeout = 90 * time.Second

// defaultMaxUnreadBytes is Options.MaxUnreadBytes when unset. Frames do not wait for the app, so a reader that stopped
// would otherwise let the daemon fill the server's memory. One message of the largest size the Server accepts still fits
// whole; an ACP client reads as lines arrive, so one that is this far behind is stuck, not slow.
const defaultMaxUnreadBytes = MaxMessageBytes

var errSessionEnded = errors.New("the agent on your machine has ended")

// session is one agent process on a user's daemon, seen from the server as stdio: Read gives the lines the daemon
// relayed from the agent's stdout, Write sends lines to its stdin. It implements Conn.
type session struct {
	server *Server
	id     string
	p      *peer
	stderr TailBuffer

	mu       sync.Mutex
	cwd      string
	out      bytes.Buffer
	finished bool
	wake     chan struct{} // a Read waits here for output or the end
	done     chan struct{} // closed once the session ended and its stderr is complete

	wmu  sync.Mutex // one writer at a time: lines reach the daemon in order
	wbuf []byte     // the start of a line the writer has not finished

	closeOnce sync.Once
	opened    atomic.Bool // the daemon answered `open`: the session counts as one the daemon runs
}

var _ Conn = (*session)(nil)

func newSession(h *Server, id string, p *peer) *session {
	return &session{server: h, id: id, p: p, stderr: TailBuffer{Max: stderrTailBytes}, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// push queues one stdout line the daemon relayed. A line that would take the unread output past MaxUnreadBytes ends
// the session instead: its output is dropped (a stream with a hole would be read as a wrong one) and the daemon is told
// to end the agent.
func (s *session) push(line string) {
	limit := s.server.opts.MaxUnreadBytes
	s.mu.Lock()
	over := !s.finished && s.out.Len()+len(line)+1 > limit
	switch {
	case over:
		s.out = bytes.Buffer{}
	case !s.finished:
		s.out.WriteString(line)
		s.out.WriteByte('\n')
	}
	s.mu.Unlock()
	if over {
		s.finish(fmt.Sprintf("the agent's output was not read: the server ended the session past %d bytes unread", limit))
		go s.Close() // not on this goroutine: it serves the daemon's socket, which Close waits on for the ack
	}
	s.signal()
}

// finish ends the session: the daemon reported the exit, or the daemon is gone. note, when set, joins the agent's
// stderr so the error a turn ends with says why.
func (s *session) finish(note string) {
	s.mu.Lock()
	already := s.finished
	s.finished = true
	s.mu.Unlock()
	if already {
		return
	}
	if note != "" {
		fmt.Fprintln(&s.stderr, note)
	}
	s.server.forget(s.id)
	close(s.done)
	s.signal()
}

// Read returns the bytes of the lines the daemon relayed, then io.EOF once the agent is gone and the queue is empty.
func (s *session) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if s.out.Len() > 0 {
			n := copy(p, s.out.Next(len(p)))
			s.mu.Unlock()
			return n, nil
		}
		finished := s.finished
		s.mu.Unlock()
		if finished {
			return 0, io.EOF
		}
		<-s.wake
	}
}

// Write splits what the ACP SDK writes into lines and sends each line to the daemon, which acknowledges it.
func (s *session) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.wbuf = append(s.wbuf, p...)
	for {
		i := bytes.IndexByte(s.wbuf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(s.wbuf[:i])
		s.wbuf = s.wbuf[i+1:]
		if err := s.sendLine(line); err != nil {
			return 0, err
		}
	}
}

func (s *session) sendLine(line string) error {
	select {
	case <-s.done:
		return errSessionEnded
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	ok, err := s.p.client.CallWrite(ctx, s.id, line)
	if err != nil {
		return fmt.Errorf("write to the daemon: %w", err)
	}
	if !ok {
		return errSessionEnded
	}
	return nil
}

func (s *session) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *session) setCwd(cwd string) {
	s.mu.Lock()
	s.cwd = cwd
	s.mu.Unlock()
	s.opened.Store(true)
}

func (s *session) Stderr() string { return s.stderr.String() }

func (s *session) Done() <-chan struct{} { return s.done }

// Close asks the daemon to end the agent and everything it started. It does not block; a daemon that never
// reports the exit is given up on after closeLinger.
func (s *session) Close() {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		if err := s.p.client.CallClose(ctx, s.id); err != nil && !isDisconnected(err) {
			log.Printf("daemon: close session %s: %v", s.id, err)
		}
		time.AfterFunc(closeLinger, func() { s.finish("") })
	})
}

// isDisconnected reports whether err says the daemon's socket is gone: nothing to close then.
func isDisconnected(err error) bool {
	var re *contract.RpcError
	return errors.As(err, &re) && re.Code == contract.CodeDisconnected
}
