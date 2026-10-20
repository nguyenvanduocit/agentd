package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nguyenvanduocit/agentd/contract"
)

// The Go client of contract/define.acp.ts, written by hand: contract/server.generated.go is the Go server side of
// it and works on a server socket. The daemon implements ClientFunctions and calls ServerFunctions.
//
// Encoding: arguments are JSON values after the event name. A call that returns a value is acknowledged with that
// value, or with an RpcError ({__rpcError: true, code, message, method}) when the handler failed. A call that
// returns nothing is not acknowledged; its failure travels as a `__rpc:error__` event with the RpcError.

// ClientHandler serves the calls an app's server makes to the daemon (ClientFunctions). ctx ends with the
// connection the call came on. Calls about one session arrive in order; calls about different sessions may run
// together.
type ClientHandler interface {
	// HandleOpen starts the named agent for a session.
	HandleOpen(ctx context.Context, sessionID, agent string) (contract.Opened, error)
	// HandleWrite writes one line to a session's agent.
	HandleWrite(ctx context.Context, sessionID, line string) (bool, error)
	// HandleClose ends a session's agent.
	HandleClose(ctx context.Context, sessionID string) error
	// HandleRestart ends every agent and exits for a start again.
	HandleRestart(ctx context.Context) error
	// HandleStop ends every agent and exits for good.
	HandleStop(ctx context.Context) error
	// HandleMcpOut is a line for an MCP bridge; a daemon's socket is not one.
	HandleMcpOut(ctx context.Context, line string) error
}

// lanes runs functions one after another per key, and the keys side by side, so that a call that waits on one
// session (a write to an agent that does not read) holds up no other session. Queuing never blocks.
type lanes struct {
	mu    sync.Mutex
	queue map[string][]func()
}

func newLanes() *lanes { return &lanes{queue: map[string][]func(){}} }

func (l *lanes) run(key string, fn func()) {
	l.mu.Lock()
	_, draining := l.queue[key]
	l.queue[key] = append(l.queue[key], fn)
	l.mu.Unlock()
	if !draining {
		go l.drain(key)
	}
}

func (l *lanes) drain(key string) {
	for {
		l.mu.Lock()
		q := l.queue[key]
		if len(q) == 0 {
			delete(l.queue, key)
			l.mu.Unlock()
			return
		}
		next := q[0]
		l.queue[key] = q[1:]
		l.mu.Unlock()
		next()
	}
}

// dispatch hands an event of the server to its handler. The handler runs on a lane of the connection, so the read
// loop (and with it the answers to pings) never waits for it, and a call that arrives after its connection ended
// is dropped: the server forgot its sessions with the connection.
func (l *Link) dispatch(c *conn, ln *lanes, name string, args []json.RawMessage, ack func(...any)) {
	switch name {
	case "open":
		ln.run(sessionKey(args), func() {
			serveCall(c, name, ack, func() (contract.Opened, error) {
				var id, agent string
				if err := decodeArgs(args, &id, &agent); err != nil {
					return contract.Opened{}, err
				}
				return l.h.HandleOpen(c.ctx, id, agent)
			})
		})
	case "write":
		ln.run(sessionKey(args), func() {
			serveCall(c, name, ack, func() (bool, error) {
				var id, line string
				if err := decodeArgs(args, &id, &line); err != nil {
					return false, err
				}
				return l.h.HandleWrite(c.ctx, id, line)
			})
		})
	case "close":
		ln.run(sessionKey(args), func() {
			serveSignal(c, name, func() error {
				var id string
				if err := decodeArgs(args, &id); err != nil {
					return err
				}
				return l.h.HandleClose(c.ctx, id)
			})
		})
	case "restart":
		ln.run("", func() { serveSignal(c, name, func() error { return l.h.HandleRestart(c.ctx) }) })
	case "stop":
		ln.run("", func() { serveSignal(c, name, func() error { return l.h.HandleStop(c.ctx) }) })
	case "mcpOut":
		ln.run("", func() {
			serveSignal(c, name, func() error {
				var line string
				if err := decodeArgs(args, &line); err != nil {
					return err
				}
				return l.h.HandleMcpOut(c.ctx, line)
			})
		})
	default:
		refuseUnknown(c, name, ack)
	}
}

// refuseUnknown answers a call of the server that this daemon does not know (a newer server's), at once: its caller
// would wait out the acknowledgement timeout otherwise. A call that wants no answer is reported on the error event, as
// the failures of the known ones are. Events that begin with "__" are the library's (the server's own error event),
// not the contract's: answering one would be an answer to an answer.
func refuseUnknown(c *conn, name string, ack func(...any)) {
	if strings.HasPrefix(name, "__") || c.ctx.Err() != nil {
		return
	}
	failure := contract.NewRpcError(contract.CodeInvalidArgument, fmt.Sprintf("this agentd does not know %s: update agentd", name), name, nil)
	if ack != nil {
		ack(failure)
		return
	}
	_ = c.notify(contract.RPCErrorEvent, failure)
}

// sessionKey is the lane of a call: its session id (the first argument), or the shared lane when it has none.
func sessionKey(args []json.RawMessage) string {
	var id string
	if len(args) > 0 && json.Unmarshal(args[0], &id) == nil {
		return "session:" + id
	}
	return ""
}

func decodeArgs(args []json.RawMessage, into ...any) error {
	if len(args) < len(into) {
		return contract.NewRpcError(contract.CodeInvalidArgument, fmt.Sprintf("expected %d arguments, got %d", len(into), len(args)), "", nil)
	}
	for i, target := range into {
		if err := json.Unmarshal(args[i], target); err != nil {
			return contract.NewRpcError(contract.CodeInvalidArgument, fmt.Sprintf("invalid argument %d: %v", i+1, err), "", nil)
		}
	}
	return nil
}

// serveCall runs a handler that returns a value and acknowledges with the value, or with the RpcError of its failure.
func serveCall[T any](c *conn, method string, ack func(...any), call func() (T, error)) {
	if c.ctx.Err() != nil {
		return
	}
	value, err := guarded(call)
	if ack == nil {
		return
	}
	if err != nil {
		ack(rpcErrorOf(err, method))
		return
	}
	ack(value)
}

// serveSignal runs a handler that returns nothing; its failure is reported on the `__rpc:error__` event.
func serveSignal(c *conn, method string, call func() error) {
	if c.ctx.Err() != nil {
		return
	}
	if _, err := guarded(func() (struct{}, error) { return struct{}{}, call() }); err != nil {
		_ = c.notify(contract.RPCErrorEvent, rpcErrorOf(err, method))
	}
}

// guarded is call, with a panic turned into its error: a handler's failure answers the server, as a thrown exception
// does in the TypeScript daemon, and does not end the daemon.
func guarded[T any](call func() (T, error)) (value T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return call()
}

// rpcErrorOf is err as the RpcError the server receives: an RpcError stays (with its method named), anything else is
// INTERNAL_ERROR with its message.
func rpcErrorOf(err error, method string) *contract.RpcError {
	var re *contract.RpcError
	if errors.As(err, &re) && re != nil {
		clone := *re
		clone.RPCError = true
		if clone.Method == "" {
			clone.Method = method
		}
		return &clone
	}
	return contract.NewRpcError(contract.CodeInternalError, err.Error(), method, nil)
}

// Hello tells the server who this daemon is, on every (re)connect, and returns the protocol the server speaks. A
// failure is a *contract.RpcError: the server's own, or TIMEOUT, DISCONNECTED or ABORTED.
func (l *Link) Hello(ctx context.Context, info contract.Hello) (contract.HelloReply, error) {
	var reply contract.HelloReply
	err := l.request(ctx, "hello", &reply, info)
	return reply, err
}

// Agents tells the server which agents are ready now (fire-and-forget); it is dropped while the socket is down.
func (l *Link) Agents(list []contract.AgentInfo) {
	if list == nil {
		list = []contract.AgentInfo{}
	}
	if c := l.current(); c != nil {
		_ = c.notify("agents", list)
	}
}

// Mcp sends one MCP JSON-RPC line to the server (fire-and-forget), for the `agentd mcp` bridge. It fails while the
// socket is down: a *contract.RpcError with DISCONNECTED.
func (l *Link) Mcp(line string) error {
	c := l.current()
	if c == nil {
		return contract.NewRpcError(contract.CodeDisconnected, errDisconnected.Error(), "mcp", nil)
	}
	return c.emit("mcp", line)
}

// request is one acknowledged call to the server.
func (l *Link) request(ctx context.Context, method string, target any, args ...any) error {
	c := l.current()
	if c == nil {
		return contract.NewRpcError(contract.CodeDisconnected, errDisconnected.Error(), method, nil)
	}
	values, err := c.call(ctx, method, args...)
	if err != nil {
		return transportError(err, method)
	}
	if len(values) != 1 {
		return contract.NewRpcError(contract.CodeInvalidArgument, fmt.Sprintf("expected one acknowledgement value, got %d", len(values)), method, nil)
	}
	var failure contract.RpcError
	if json.Unmarshal(values[0], &failure) == nil && failure.RPCError {
		return &failure
	}
	if err := json.Unmarshal(values[0], target); err != nil {
		return contract.NewRpcError(contract.CodeInvalidArgument, fmt.Sprintf("invalid acknowledgement payload: %v", err), method, nil)
	}
	return nil
}

func transportError(err error, method string) *contract.RpcError {
	switch {
	case errors.Is(err, errTimedOut):
		return contract.NewRpcError(contract.CodeTimeout, err.Error(), method, nil)
	case errors.Is(err, errDisconnected):
		return contract.NewRpcError(contract.CodeDisconnected, err.Error(), method, nil)
	case errors.Is(err, context.Canceled):
		return contract.NewRpcError(contract.CodeAborted, err.Error(), method, nil)
	}
	return contract.NewRpcError(contract.CodeInternalError, err.Error(), method, nil)
}
