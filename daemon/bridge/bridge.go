// Package bridge is `agentd mcp`: the stdio MCP servers an agent starts through the agentd-mcp wrapper. `--session
// <id>` carries the agent's JSON-RPC lines to an app's server over a socket of its own; `--page` carries them to the
// page that started the agent, through the daemon's Unix socket. Neither opens a port. Each returns the exit code.
package bridge

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// CloseWait is the longest a bridge waits to hand over the last lines once stdin has ended, before it exits anyway.
const CloseWait = 10 * time.Second

const maxLineMiB = proc.MaxLineBytes / (1024 * 1024)

// Deps is what a bridge runs against; a test passes its own.
type Deps struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Host is the machine: Host.Env is the environment `--page` reads, and Host locates the registry for `--session`.
	Host home.Host
	// Connect makes the link to an app's server. RunBridge needs it; RunPageBridge does not.
	Connect Connect
	// CloseWait is how long the bridge waits for its last lines to go out after stdin ended; zero is CloseWait.
	CloseWait time.Duration
}

func (d Deps) closeWait() time.Duration {
	if d.CloseWait > 0 {
		return d.CloseWait
	}
	return CloseWait
}

// outcome settles a bridge once: the first code is its exit code, and its message, if any, goes to stderr.
type outcome struct {
	log  *proc.Logger
	once sync.Once
	done chan struct{}
	code int
}

func newOutcome(log *proc.Logger) *outcome { return &outcome{log: log, done: make(chan struct{})} }

func (o *outcome) finish(code int) { o.finishWith(code, "") }

func (o *outcome) finishWith(code int, message string) {
	o.once.Do(func() {
		if message != "" {
			o.log.Warn(message)
		}
		o.code = code
		close(o.done)
	})
}

// afterEnd settles the bridge with 0 once wait has passed, unless it was settled before.
func (o *outcome) afterEnd(wait time.Duration) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		o.finish(0)
	case <-o.done:
	}
}

// lineWriter writes whole lines to w, one at a time.
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) line(text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := io.WriteString(l.w, text+"\n")
	return err
}

// readLines hands each line r yields to handle, until handle returns false (it returns false, nil), r ends (io.EOF
// or its error) or a line passes the cap (true: the stream is over, and the lines before it were handled).
func readLines(r io.Reader, handle func(line string) bool) (tooLong bool, err error) {
	splitter := proc.NewLineSplitter(proc.MaxLineBytes)
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, line := range splitter.Push(buf[:n]) {
				if !handle(line) {
					return false, nil
				}
			}
			if splitter.TooLong() {
				return true, nil
			}
		}
		if err != nil {
			return false, err
		}
	}
}

// blank reports a line with nothing but whitespace (what String.prototype.trim removes), which is not forwarded.
func blank(line string) bool {
	return strings.TrimFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }) == ""
}

// closeInput ends the read of stdin when it can be ended, so that the goroutine that reads it does not outlive the bridge.
func closeInput(in io.Reader) {
	if c, ok := in.(io.Closer); ok {
		_ = c.Close()
	}
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }

func tooLongMessage(who string) string {
	return fmt.Sprintf("agentd-mcp: the %s sent a line longer than %d MiB", who, maxLineMiB)
}
