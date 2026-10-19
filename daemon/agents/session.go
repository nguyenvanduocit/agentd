package agents

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	termGrace = 2 * time.Second
	pipeGrace = 3 * time.Second
	readChunk = 64 * 1024
	// exitBySignal is the code of the exit frame of an agent a signal ended.
	exitBySignal = 128
)

// FrameKind is what a frame carries: the agent's stdout line, its stderr text, or its end.
type FrameKind string

const (
	FrameOut  FrameKind = "out"
	FrameErr  FrameKind = "err"
	FrameExit FrameKind = "exit"
)

// FrameSink is where an agent's frames go: one ordered stream per connection. A sink that has been replaced drops
// frames. Send may be called from several goroutines.
type FrameSink interface {
	Send(sessionID string, kind FrameKind, data string, code int)
}

// BackloggedSink is a FrameSink that can fall behind its reader. Backlog is nil while the sink has room, else a
// channel that is closed once it has room again. The agent's output is not read meanwhile, so the agent waits on its
// full pipe. A sink without Backlog takes every frame (the server bounds what it holds itself).
type BackloggedSink interface {
	FrameSink
	Backlog() <-chan struct{}
}

// Session is one agent process: its stdout becomes `out` frames, its stderr `err` frames, its end one `exit` frame.
type Session struct {
	id    string
	cmd   *exec.Cmd
	sink  FrameSink
	room  func() <-chan struct{}
	onEnd func()
	// pgid is the process group the agent leads (its pid).
	pgid int

	stdin io.WriteCloser
	wmu   sync.Mutex // one line at a time into stdin

	readers sync.WaitGroup
	cut     chan struct{} // closed when the pipes are cut after the leader is gone

	mu        sync.Mutex
	killTimer *time.Timer
	finished  bool
}

// StartSession starts cmd as a session's agent and pumps its output to sink. cmd has its Path, Args, Dir, Env and
// process group set; the session owns its stdio. onEnd runs once the agent is gone and its output drained, before
// the exit frame. A cmd that cannot start is an error, and no frame is sent.
func StartSession(id string, cmd *exec.Cmd, sink FrameSink, onEnd func()) (*Session, error) {
	return startSession(id, cmd, sink, onEnd, proc.MaxLineBytes)
}

func startSession(id string, cmd *exec.Cmd, sink FrameSink, onEnd func(), maxLine int) (*Session, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(outR, outW)
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	closeAll(outW, errW) // the child holds its own copies; readers see EOF when it and its children are gone
	if err != nil {
		closeAll(outR, errR)
		_ = stdin.Close()
		return nil, err
	}
	s := &Session{id: id, cmd: cmd, sink: sink, onEnd: onEnd, pgid: cmd.Process.Pid, stdin: stdin, cut: make(chan struct{})}
	if b, ok := sink.(BackloggedSink); ok {
		s.room = b.Backlog
	}
	s.readers.Add(2)
	go s.pumpOut(outR, maxLine)
	go s.pumpErr(errR)
	go s.wait(outR, errR)
	return s, nil
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// PGID is the process group of the agent: its pid.
func (s *Session) PGID() int { return s.pgid }

// wait ends the session. The leader is gone: end whatever it left in the group. A process that left the group may
// still hold a pipe, so the pipes get a grace period before they are cut and the session can finish.
func (s *Session) wait(outR, errR *os.File) {
	_ = s.cmd.Wait() // the exit status is read from ProcessState
	proc.SignalGroup(s.pgid, true)
	cutter := time.AfterFunc(pipeGrace, func() {
		close(s.cut)
		closeAll(outR, errR)
	})
	s.readers.Wait()
	cutter.Stop()

	s.mu.Lock()
	s.finished = true
	if s.killTimer != nil {
		s.killTimer.Stop()
	}
	s.mu.Unlock()

	s.onEnd()
	code := s.cmd.ProcessState.ExitCode()
	if code < 0 {
		code = exitBySignal
	}
	s.sink.Send(s.id, FrameExit, "", code)
}

func (s *Session) pumpOut(r *os.File, maxLine int) {
	defer s.readers.Done()
	splitter := proc.NewLineSplitter(maxLine)
	buf := make([]byte, readChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, line := range splitter.Push(buf[:n]) {
				s.sink.Send(s.id, FrameOut, validText(line), 0)
			}
			if splitter.TooLong() {
				s.lineTooLong(r)
				return
			}
			if !s.waitForRoom() {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) pumpErr(r *os.File) {
	defer s.readers.Done()
	buf := make([]byte, readChunk)
	var carry []byte // the start of a character that continues in the next chunk
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			whole, rest := splitIncomplete(chunk)
			carry = append([]byte(nil), rest...)
			if len(whole) > 0 {
				s.sink.Send(s.id, FrameErr, validText(string(whole)), 0)
			}
			if !s.waitForRoom() {
				return
			}
		}
		if err != nil {
			if len(carry) > 0 {
				s.sink.Send(s.id, FrameErr, validText(string(carry)), 0)
			}
			return
		}
	}
}

// splitIncomplete cuts b before a UTF-8 character that b ends in the middle of.
func splitIncomplete(b []byte) (whole, rest []byte) {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i], b[len(b)-i:]
			}
			break
		}
	}
	return b, nil
}

// validText is text with every invalid byte sequence replaced by U+FFFD, as the sink takes JSON strings.
func validText(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "�")
}

// waitForRoom stops reading while the sink is full. It is false when the pipes were cut meanwhile.
func (s *Session) waitForRoom() bool {
	if s.room == nil {
		return true
	}
	ch := s.room()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-s.cut:
		return false
	}
}

// lineTooLong: the agent wrote a line no reader would take: the session ends, and its stderr says why.
func (s *Session) lineTooLong(stdout *os.File) {
	s.sink.Send(s.id, FrameErr, fmt.Sprintf("agentd: the agent wrote a line longer than %d MiB; the session is ended\n", proc.MaxLineBytes/(1024*1024)), 0)
	_ = stdout.Close()
	s.End()
}

// Write writes one line to the agent's stdin; false when the agent can no longer read it. It returns once the agent
// has taken the line, so a caller that must not wait on a slow agent runs it on its own goroutine.
func (s *Session) Write(line string) bool {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := io.WriteString(s.stdin, line+"\n")
	return err == nil
}

// End sends SIGTERM to the group, and SIGKILL after a grace period: children may outlive a leader that ignores SIGTERM.
func (s *Session) End() {
	proc.SignalGroup(s.pgid, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.killTimer != nil {
		return
	}
	s.killTimer = time.AfterFunc(termGrace, func() { proc.SignalGroup(s.pgid, true) })
}
