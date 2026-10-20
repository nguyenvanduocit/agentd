package mcpbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/link"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// reconnectAttempts is how many times a bridge retries a connection that failed before it gives up.
const reconnectAttempts = 3

// abortGrace is how long a bridge that is ending for a failure waits for its socket to close.
const abortGrace = time.Second

// reasonClient is socket.io's name for a disconnect this side asked for.
const reasonClient = "io client disconnect"

// Session is the bridge's connection to its app's server: a link.Link, or a stand-in.
type Session interface {
	// Start connects, and keeps trying within the retries of the link's Options.
	Start()
	// Close ends the connection once what was sent has gone out, or after grace.
	Close(grace time.Duration)
	// Mcp sends one MCP JSON-RPC line as the contract's `mcp` call (fire-and-forget); it fails while the socket is down.
	Mcp(line string) error
}

// Connect makes a Session that is not connected yet, as link.New makes a Link.
type Connect func(o link.Options, h link.ClientHandler, ev link.Events) (Session, error)

// Dial is the Connect of a real run: it makes a link.Link.
func Dial(o link.Options, h link.ClientHandler, ev link.Events) (Session, error) {
	l, err := link.New(o, h, ev)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// appBridge is one `mcp --session` run.
type appBridge struct {
	app       registry.App
	stdout    *lineWriter
	log       *proc.Logger
	out       *outcome
	connectAt chan struct{} // closed on the first connect
	closeWait time.Duration
}

var errNotDaemon = errors.New("this socket is an MCP bridge: it serves mcpOut only")

func (*appBridge) HandleOpen(context.Context, string, string) (contract.Opened, error) {
	return contract.Opened{}, errNotDaemon
}

func (*appBridge) HandleWrite(context.Context, string, string) (bool, error) {
	return false, errNotDaemon
}
func (*appBridge) HandleClose(context.Context, string) error { return errNotDaemon }
func (*appBridge) HandleRestart(context.Context) error       { return errNotDaemon }
func (*appBridge) HandleStop(context.Context) error          { return errNotDaemon }

// HandleMcpOut prints a line of the server's MCP session on stdout.
func (b *appBridge) HandleMcpOut(_ context.Context, line string) error {
	return b.stdout.line(line)
}

func (b *appBridge) events() link.Events {
	return link.Events{
		OnConnect: func() {
			select {
			case <-b.connectAt:
			default:
				close(b.connectAt)
			}
		},
		// The MCP session lives on the server's side of this socket: a new socket would be a new, empty session.
		OnDisconnect: func(reason string) {
			if reason == reasonClient { // our own close, after stdin ended
				return
			}
			b.log.Warn(fmt.Sprintf("agentd-mcp: the server connection ended (%s)", reason))
			b.out.finish(1)
		},
		OnTrouble: func(message string) {
			b.log.Warn(fmt.Sprintf("agentd-mcp: cannot reach %s (%s)", b.app.Server, message))
		},
		OnRejected: func(message string) {
			b.log.Warn(fmt.Sprintf("agentd-mcp: %s refused the bridge (%s)", b.app.Server, message))
			b.out.finish(1)
		},
		OnGiveUp: func() { b.out.finish(1) },
	}
}

// readInput queues the lines of stdin for the server. At the end of stdin it closes lines.
func (b *appBridge) readInput(in io.Reader, lines chan<- string) {
	defer close(lines)
	tooLong, err := readLines(in, func(line string) bool {
		if blank(line) {
			return true
		}
		select {
		case lines <- line:
			return true
		case <-b.out.done:
			return false
		}
	})
	switch {
	case tooLong:
		// The server would refuse the message; a bridge that dropped it would leave the client waiting for an answer.
		b.log.Warn(tooLongMessage("client"))
		b.out.finish(1)
	case err != nil && !isEOF(err):
		b.log.Warn(fmt.Sprintf("agentd-mcp: cannot read stdin (%v)", err))
		b.out.finish(1)
	case err != nil:
		go b.out.afterEnd(b.closeWait)
	}
}

// send hands the queued lines to the server, in order, once the socket is up. Once stdin has ended and its lines are
// out, it closes the connection: the client is done.
func (b *appBridge) send(s Session, lines <-chan string) {
	select {
	case <-b.connectAt:
	case <-b.out.done:
		return
	}
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				s.Close(b.closeWait)
				b.out.finish(0)
				return
			}
			if err := s.Mcp(line); err != nil {
				b.log.Warn(fmt.Sprintf("agentd-mcp: cannot send to %s (%v)", b.app.Server, err))
				b.out.finish(1)
				return
			}
		case <-b.out.done:
			return
		}
	}
}

// RunBridge is `agentd mcp --session <id>`: it forwards the JSON-RPC lines of stdin, one per message, to the app's
// server over its own socket, and prints the server's answers. It returns the exit code: 0 when stdin ended and its
// lines were handed over, 1 when the server refused it, could not be reached, or ended the connection.
func RunBridge(cmd cli.MCP, d Deps) int {
	log := proc.NewLogger(io.Discard, d.Stderr)
	reg, err := registry.Load(registry.File(cmd.Config, d.Host))
	if err != nil {
		log.Warn(err.Error())
		return 1
	}
	app, err := reg.SelectApp(cmd.App)
	if err != nil {
		log.Warn(err.Error())
		return 1
	}
	b := &appBridge{
		app:       app,
		stdout:    &lineWriter{w: d.Stdout},
		log:       log,
		out:       newOutcome(log),
		connectAt: make(chan struct{}),
		closeWait: d.closeWait(),
	}
	opts := link.Options{
		Server:     app.Server,
		Auth:       map[string]string{"role": "mcp", "token": app.Token, "session": cmd.Session},
		MaxRetries: reconnectAttempts,
	}
	s, err := d.Connect(opts, b, b.events())
	if err != nil {
		log.Warn(err.Error())
		return 1
	}
	lines := make(chan string, 256)
	go b.readInput(d.Stdin, lines)
	go b.send(s, lines)
	s.Start()
	<-b.out.done
	closeInput(d.Stdin)
	s.Close(abortGrace)
	return b.out.code
}
