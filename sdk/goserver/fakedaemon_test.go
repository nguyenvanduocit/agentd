package goserver_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	csocket "github.com/zishang520/socket.io/clients/socket/v3"
	sio "github.com/zishang520/socket.io/servers/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"

	"github.com/nguyenvanduocit/agentd/contract"
)

const waitFor = 5 * time.Second

// The Go Socket.IO client is not reliable under load: now and then it sends the namespace CONNECT twice (the server
// then sees two handshakes, and its second socket replaces the first, as one daemon per user requires) or never
// (the server sees none). Both show only in these tests, whose client is not the real daemon's. dial therefore
// gives each handshake dialWait and tries again on a new socket, up to dialAttempts times, and a daemon that was
// replaced by its own duplicate within settle is dialled again.
const (
	dialAttempts = 5
	dialWait     = 2 * time.Second
	settle       = 60 * time.Millisecond
)

// dial opens a Socket.IO client on the /daemon namespace with the given handshake auth. It waits until the server
// accepted or refused it; refused reports the refusal.
func dial(t *testing.T, base string, auth map[string]any) (sock *csocket.Socket, refused any) {
	t.Helper()
	for range dialAttempts {
		sock, refused, finished := dialOnce(t, base, auth)
		if !finished {
			continue
		}
		if refused == nil {
			time.Sleep(settle)
			if !sock.Connected() {
				continue
			}
		}
		t.Cleanup(func() { sock.Close() })
		return sock, refused
	}
	t.Fatal("the handshake neither succeeded nor failed")
	return nil, nil
}

func dialOnce(t *testing.T, base string, auth map[string]any) (sock *csocket.Socket, refused any, finished bool) {
	t.Helper()
	opts := csocket.DefaultOptions()
	opts.SetTransports(types.NewSet(csocket.WebSocket))
	opts.SetAuth(auth)
	opts.SetReconnection(false)
	opts.SetAutoConnect(false) // the listeners below must exist before the connection can finish
	opts.SetForceNew(true)     // every dial is its own connection, never one shared through the client's cache
	sock, err := csocket.Connect(base+"/agentd", opts)
	if err != nil {
		t.Fatal(err)
	}
	connected, failed := make(chan struct{}, 1), make(chan any, 1)
	_ = sock.On("connect", func(...any) { connected <- struct{}{} })
	_ = sock.On("connect_error", func(a ...any) { failed <- a })
	sock.Connect()
	select {
	case <-connected:
		return sock, nil, true
	case r := <-failed:
		return sock, r, true
	case <-time.After(dialWait):
		sock.Close()
		return nil, nil, false
	}
}

// fakeDaemon is the daemon's side of the contract, as a test double: it answers open, write and close the way a
// real daemon does, and lets the test send frames and hello.
type fakeDaemon struct {
	id, token string // the daemon's record (set by env.online) and its token
	base      string // the server's address
	t         *testing.T
	sock      *csocket.Socket
	emit      sync.Mutex // the ack timeout of a call is a flag on the socket: calls are paired under a lock

	mu      sync.Mutex
	opened  []string // "<sid>:<agent>"
	written map[string][]string
	closed  []string
	onOpen  func(sid, agent string) (contract.Opened, error)
	onWrite func(sid, line string) bool
	onClose func(sid string)

	controls  []string // "restart" and "stop", as the daemon was told
	onControl func(what string)
}

// connectDaemon dials as a daemon with token and returns once the server accepted it.
func connectDaemon(t *testing.T, base, token string) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{t: t, written: map[string][]string{}}
	d.onOpen = func(string, string) (contract.Opened, error) { return contract.Opened{Pid: 1, Cwd: "/work"}, nil }
	d.onWrite = func(string, string) bool { return true }
	d.base, d.token = base, token
	d.redial()
	return d
}

// redial opens a new connection with the daemon's token and binds the daemon's calls to it.
func (d *fakeDaemon) redial() {
	d.t.Helper()
	sock, refused := dial(d.t, d.base, map[string]any{"role": "daemon", "token": d.token})
	if refused != nil {
		d.t.Fatalf("the server refused the daemon: %v", refused)
	}
	d.emit.Lock()
	d.sock = sock
	d.emit.Unlock()
	d.bind()
}

// bind answers the server's calls on the daemon's current socket.
func (d *fakeDaemon) bind() {
	sock := d.sock
	d.handle("open", func(args []any) any {
		sid, _ := args[0].(string)
		name, _ := args[1].(string)
		d.mu.Lock()
		d.opened = append(d.opened, sid+":"+name)
		fn := d.onOpen
		d.mu.Unlock()
		opened, err := fn(sid, name)
		if err != nil {
			return contract.NewRpcError(contract.CodeInternalError, err.Error(), "open", nil)
		}
		return opened
	})
	d.handle("write", func(args []any) any {
		sid, _ := args[0].(string)
		line, _ := args[1].(string)
		d.mu.Lock()
		d.written[sid] = append(d.written[sid], line)
		fn := d.onWrite
		d.mu.Unlock()
		return fn(sid, line)
	})
	_ = sock.On("close", func(args ...any) {
		sid, _ := args[0].(string)
		d.mu.Lock()
		d.closed = append(d.closed, sid)
		fn := d.onClose
		d.mu.Unlock()
		if fn != nil {
			fn(sid)
		}
	})
	for _, what := range []string{"restart", "stop"} {
		_ = sock.On(types.EventName(what), func(...any) {
			d.mu.Lock()
			d.controls = append(d.controls, what)
			fn := d.onControl
			d.mu.Unlock()
			if fn != nil {
				fn(what)
			}
		})
	}
}

// told is what the server told the daemon to do, in order.
func (d *fakeDaemon) told() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.controls...)
}

// handle answers an acknowledged call from the server with fn's value.
func (d *fakeDaemon) handle(event string, fn func(args []any) any) {
	_ = d.sock.On(types.EventName(event), func(args ...any) {
		ack, ok := args[len(args)-1].(sio.Ack)
		if !ok {
			d.t.Errorf("%s: no acknowledgement callback", event)
			return
		}
		ack([]any{fn(args[:len(args)-1])}, nil)
	})
}

// The Go Socket.IO client now and then sends the namespace CONNECT twice (Connect() sends one when the transport is
// already open and the transport's own open event sends the other). The server then sees two handshakes on one
// transport; the second replaces the first, and ending the first closes the transport they share. A real daemon, whose
// client sends one, never meets this, so it is handled here: hello is the first thing a daemon says, and it is repeated
// on a new connection, up to helloAttempts times, until it is answered and the connection is still up a moment later.
const (
	helloAttempts = 4
	helloWait     = 2 * time.Second
)

// hello sends the daemon's hello and returns the server's reply.
func (d *fakeDaemon) hello(info contract.Hello) contract.HelloReply {
	d.t.Helper()
	var failures []string
	for range helloAttempts {
		if !d.sock.Connected() {
			d.redial()
		}
		reply, err := d.tryHello(info)
		if err == nil {
			time.Sleep(2 * settle) // a duplicate CONNECT still on its way closes the connection about now
			if d.sock.Connected() {
				return reply
			}
			err = errors.New("the connection closed right after the answer")
		}
		failures = append(failures, err.Error())
		d.sock.Close()
	}
	d.t.Fatalf("hello was not answered on a lasting connection in %d attempts: %v", helloAttempts, failures)
	return contract.HelloReply{}
}

func (d *fakeDaemon) tryHello(info contract.Hello) (contract.HelloReply, error) {
	type answer struct {
		vals []any
		err  error
	}
	res := make(chan answer, 1)
	d.emit.Lock()
	d.sock.Timeout(helloWait).EmitWithAck("hello", info)(func(vals []any, err error) { res <- answer{vals, err} })
	d.emit.Unlock()
	select {
	case a := <-res:
		if a.err != nil {
			return contract.HelloReply{}, a.err
		}
		if len(a.vals) != 1 {
			return contract.HelloReply{}, fmt.Errorf("hello answered %v", a.vals)
		}
		raw, err := json.Marshal(a.vals[0])
		if err != nil {
			return contract.HelloReply{}, err
		}
		var reply contract.HelloReply
		if err := json.Unmarshal(raw, &reply); err != nil {
			return contract.HelloReply{}, fmt.Errorf("hello answered %s: %w", raw, err)
		}
		return reply, nil
	case <-time.After(helloWait + time.Second):
		return contract.HelloReply{}, errors.New("no answer")
	}
}

// agents tells the server which agents the daemon is ready to run now.
func (d *fakeDaemon) agents(list []contract.AgentInfo) {
	d.emit.Lock()
	defer d.emit.Unlock()
	if err := d.sock.Emit("agents", list); err != nil {
		d.t.Errorf("agents: %v", err)
	}
}

func (d *fakeDaemon) frame(sid string, kind contract.FrameKind, data string, code int) {
	d.emit.Lock()
	defer d.emit.Unlock()
	if !d.sock.Connected() {
		return // a daemon whose socket dropped has nothing to say
	}
	if err := d.sock.Emit("frame", sid, string(kind), data, code); err != nil {
		d.t.Errorf("frame: %v", err)
	}
}

func (d *fakeDaemon) disconnect() { d.sock.Disconnect() }

func (d *fakeDaemon) openedSessions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.opened...)
}

func (d *fakeDaemon) writtenTo(sid string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.written[sid]...)
}

func (d *fakeDaemon) closedSessions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.closed...)
}

func allAgents() []contract.AgentInfo {
	return []contract.AgentInfo{
		{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Ready: false, Reason: "Node is not installed"}, {Name: "grok", Title: "Grok", Ready: true},
	}
}

// bridge is what `agentd-mcp` does: it carries MCP lines between an agent and the server over its own socket.
// The returned transport is the agent's end.
func bridge(t *testing.T, base, token, session string) (transport mcp.Transport, refused any) {
	t.Helper()
	transport, _, refused = bridgeSocket(t, base, token, session)
	return transport, refused
}

// bridgeSocket is bridge with the bridge's socket too.
func bridgeSocket(t *testing.T, base, token, session string) (transport mcp.Transport, sock *csocket.Socket, refused any) {
	t.Helper()
	sock, refused = dial(t, base, map[string]any{"role": "mcp", "token": token, "session": session})
	if refused != nil {
		return nil, nil, refused
	}
	fromServerR, fromServerW := io.Pipe()
	toServerR, toServerW := io.Pipe()
	t.Cleanup(func() { fromServerW.Close(); toServerW.Close() })
	_ = sock.On("mcpOut", func(args ...any) {
		line, _ := args[0].(string)
		_, _ = io.WriteString(fromServerW, line+"\n")
	})
	_ = sock.On("disconnect", func(...any) { fromServerW.Close() })
	go func() {
		r := bufio.NewReader(toServerR)
		for {
			line, err := r.ReadString('\n')
			if len(line) > 1 {
				_ = sock.Emit("mcp", line[:len(line)-1])
			}
			if err != nil {
				return
			}
		}
	}()
	return &mcp.IOTransport{Reader: fromServerR, Writer: toServerW}, sock, nil
}
