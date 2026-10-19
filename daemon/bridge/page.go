package bridge

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"github.com/nguyenvanduocit/agentd/daemon/pagemcp"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// pageBridge is one `mcp --page` run.
type pageBridge struct {
	conn       net.Conn
	path       string
	stdout     *lineWriter
	out        *outcome
	closeWait  func()
	stdinEnded atomic.Bool
}

func (p *pageBridge) unreachable(err error) string {
	return fmt.Sprintf("agentd-mcp: cannot reach the daemon at %s (%v)", p.path, err)
}

// readDaemon relays the daemon's lines: its answer to the hello first, then the page's lines, which go to stdout.
func (p *pageBridge) readDaemon() {
	accepted := false
	tooLong, err := readLines(p.conn, func(line string) bool {
		if accepted {
			_ = p.stdout.line(line)
			return true
		}
		answer := pagemcp.ParseBridgeAnswer(line)
		if !answer.OK {
			p.out.finishWith(1, fmt.Sprintf("agentd-mcp: the daemon refused the bridge (%s)", answer.Message))
			return false
		}
		accepted = true
		return true
	})
	switch {
	case tooLong:
		p.out.finishWith(1, tooLongMessage("page"))
	case err == nil: // the daemon refused the bridge
	case !isEOF(err):
		p.out.finishWith(1, p.unreachable(err))
	case p.stdinEnded.Load():
		// The client is done: the daemon closes its end once it has read the last lines, and that close is a clean exit.
		p.out.finish(0)
	default:
		p.out.finishWith(1, "agentd-mcp: the daemon ended the bridge: the page or its session closed, or the agent started a newer bridge")
	}
}

// readClient sends the client's lines on stdin to the daemon. The end of stdin ends the bridge with 0 once its lines are out.
func (p *pageBridge) readClient(in io.Reader) {
	tooLong, err := readLines(in, func(line string) bool {
		if blank(line) {
			return true
		}
		if _, werr := io.WriteString(p.conn, line+"\n"); werr != nil {
			p.out.finishWith(1, p.unreachable(werr))
			return false
		}
		return true
	})
	switch {
	case tooLong:
		// The page would never get the message; a bridge that dropped it would leave the client waiting for an answer.
		p.out.finishWith(1, tooLongMessage("client"))
	case err == nil: // a write failed: the bridge is settled
	case !isEOF(err):
		p.out.finishWith(1, fmt.Sprintf("agentd-mcp: cannot read stdin (%v)", err))
	default:
		p.stdinEnded.Store(true)
		if u, ok := p.conn.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
		p.closeWait()
	}
}

// RunPageBridge is `agentd mcp --page`: it forwards the JSON-RPC lines of stdin, one per message, to the page whose
// session started the agent, and prints the page's answers. It reaches the page through the daemon's socket
// (AGENTD_MCP_SOCKET, in d.Host.Env) with the session's token (AGENTD_MCP_TOKEN), both from the `opened.mcp` the page
// passed to session/new. It returns the exit code: 0 when stdin ended and the daemon closed its end, 1 for a refused
// token, a missing variable, or a connection that ended first.
func RunPageBridge(d Deps) int {
	log := proc.NewLogger(io.Discard, d.Stderr)
	path, token := d.Host.Env[pagemcp.McpSocketEnv], d.Host.Env[pagemcp.McpTokenEnv]
	if path == "" || token == "" {
		log.Warn(fmt.Sprintf("agentd-mcp --page needs %s and %s: the env of the MCP server in a page session's opened.mcp",
			pagemcp.McpSocketEnv, pagemcp.McpTokenEnv))
		return 1
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		log.Warn(fmt.Sprintf("agentd-mcp: cannot reach the daemon at %s (%v)", path, err))
		return 1
	}
	p := &pageBridge{conn: conn, path: path, stdout: &lineWriter{w: d.Stdout}, out: newOutcome(log)}
	p.closeWait = func() { go p.out.afterEnd(d.closeWait()) }
	daemonRead := make(chan struct{})
	go func() {
		defer close(daemonRead)
		p.readDaemon()
	}()
	if _, err := io.WriteString(conn, pagemcp.BridgeHello(token)+"\n"); err != nil {
		p.out.finishWith(1, p.unreachable(err))
	} else {
		go p.readClient(d.Stdin)
	}
	<-p.out.done
	closeInput(d.Stdin)
	_ = conn.Close()
	<-daemonRead
	return p.out.code
}
