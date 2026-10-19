package hub

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
)

// Namespace is the Socket.IO namespace daemons and their MCP bridges connect to.
const Namespace = "/agentd"

// Handshake roles.
const (
	roleDaemon = "daemon"
	roleBridge = "mcp"
)

// Mount puts the hub on the web server: the /agentd Socket.IO namespace with its own handshake check (the default
// namespace keeps its cookie check), and the pairing and install endpoints.
func (h *Hub) Mount(srv *socket.Server, mux *http.ServeMux) {
	ns := srv.Of(Namespace, nil)
	ns.Use(h.authenticate)
	if err := ns.On("connection", func(clients ...any) { h.onConnection(clients) }); err != nil {
		log.Printf("daemon: connection handler: %v", err)
	}
	mux.HandleFunc("POST /agentd/pair", h.handlePair)
}

// text is the string under key in the handshake's auth data, empty when it is missing or not a string.
func text(auth map[string]any, key string) string {
	v, ok := auth[key].(string)
	if !ok {
		return ""
	}
	return v
}

// errUnauthorized is all a refused handshake says, whatever was wrong with it: as the message and as data.code, so
// that a daemon can tell a token the server does not accept (revoked, unknown) from a network error.
const errUnauthorized = "unauthorized"

// authenticate checks the handshake: {role:"daemon", token} or {role:"mcp", token, session}. The token must be one
// the server issued and not revoked; a bridge's session must be a tool session of the token's user. A socket that passes gets its
// call handlers here, before the server tells it that it is connected: the Go server sends that answer first and
// emits `connection` after it, so a daemon that says hello at once would otherwise be heard by nobody.
func (h *Hub) authenticate(s *socket.Socket, next func(*socket.ExtendedError)) {
	auth := s.Handshake().Auth
	role, token, session := text(auth, "role"), text(auth, "token"), text(auth, "session")
	rec, err := h.opts.Tokens.ByHash(hashToken(token))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("daemon: look up a token: %v", err)
		}
		next(socket.NewExtendedError(errUnauthorized, map[string]any{"code": errUnauthorized}))
		return
	}
	switch {
	case role == roleDaemon:
		err = h.prepareDaemon(s, rec)
	case role == roleBridge && h.opts.Tools != nil && h.opts.Tools.Owns(rec.UserID, rec.ID, session):
		err = h.prepareBridge(s, rec, session)
	default:
		err = errors.New("unknown role, or a session that is not the user's")
	}
	if err != nil {
		log.Printf("daemon: refuse a %q handshake of user %s: %v", role, rec.UserID, err)
		next(socket.NewExtendedError(errUnauthorized, map[string]any{"code": errUnauthorized}))
		return
	}
	next(nil)
}

// onConnection finishes what authenticate prepared, once the socket is connected.
func (h *Hub) onConnection(clients []any) {
	if len(clients) == 0 {
		log.Print("daemon: connection event without a socket")
		return
	}
	raw, ok := clients[0].(*socket.Socket)
	if !ok {
		log.Printf("daemon: connection event carries %T, want *socket.Socket", clients[0])
		return
	}
	var err error
	switch v := raw.Data().(type) {
	case *peer:
		err = h.attachDaemon(raw, v)
	case *bridge:
		err = h.attachBridge(raw, v)
	default:
		err = fmt.Errorf("no handshake result on the socket: %T", v)
	}
	if err != nil {
		log.Printf("daemon: connect a socket: %v", err)
		raw.Disconnect(true)
	}
}

// prepareDaemon makes a handshaken daemon the connection of its record and binds its calls.
func (h *Hub) prepareDaemon(s *socket.Socket, rec Record) error {
	p := &peer{id: rec.ID, userID: rec.UserID, name: rec.Name, raw: s, attached: make(chan struct{})}
	if err := s.On("disconnect", func(...any) { h.disconnected(p) }); err != nil {
		return fmt.Errorf("disconnect handler: %w", err)
	}
	if _, err := contract.BindServer(s, daemonHandler{hub: h, p: p}); err != nil {
		return fmt.Errorf("rpc bind: %w", err)
	}
	s.SetData(p)
	return h.connect(p, rec)
}

// attachDaemon gives the daemon the means to be called, which needs a connected socket.
func (h *Hub) attachDaemon(raw *socket.Socket, p *peer) error {
	client, err := contract.NewClient(raw, &contract.ClientOptions{Timeout: ackTimeout})
	if err != nil {
		return fmt.Errorf("rpc client: %w", err)
	}
	p.client = client
	close(p.attached)
	return nil
}

// daemonHandler serves what a daemon calls: frame and hello.
type daemonHandler struct {
	hub *Hub
	p   *peer
}

var errNotBridge = errors.New("this socket is not an MCP bridge")

func (d daemonHandler) HandleFrame(_ context.Context, sessionID string, kind contract.FrameKind, data string, code float64) error {
	s := d.hub.session(d.p, sessionID)
	if s == nil {
		return nil // a frame after the session ended, or for a session this daemon does not run
	}
	switch kind {
	case contract.FrameKindOut:
		s.push(data)
	case contract.FrameKindErr:
		fmt.Fprint(&s.stderr, data)
	case contract.FrameKindExit:
		note := ""
		if code != 0 {
			note = fmt.Sprintf("the agent exited with code %d", int(code))
		}
		s.finish(d.hub.noteOf(d.p, note))
	}
	return nil
}

// HandleHello answers with the build the server serves, so that the daemon can update itself to it.
func (d daemonHandler) HandleHello(ctx context.Context, info contract.Hello) (contract.HelloReply, error) {
	select {
	case <-d.p.attached: // the socket is connected and can be called
	case <-ctx.Done():
		return contract.HelloReply{}, fmt.Errorf("the daemon left before its hello was handled: %w", ctx.Err())
	}
	d.hub.hello(d.p, info)
	return contract.HelloReply{Protocol: Protocol}, nil
}

// HandleAgents takes the agents the daemon is ready to run now, after an install finished or failed.
func (d daemonHandler) HandleAgents(_ context.Context, agents []contract.AgentInfo) error {
	d.hub.agents(d.p, agents)
	return nil
}

func (d daemonHandler) HandleMcp(context.Context, string) error { return errNotBridge }

// bridge is an MCP bridge between handshake and connection.
type bridge struct {
	userID, daemonID, session string
	raw                       *socket.Socket
	in                        *io.PipeWriter // the bridge's messages, read by the session's MCP server
	inR                       *io.PipeReader
	outR                      *io.PipeReader // what the MCP server writes, to go to the bridge
	outW                      *io.PipeWriter
	cancel                    context.CancelFunc
	ctx                       context.Context //nolint:containedctx // the life of one bridge connection, from handshake to disconnect
}

// prepareBridge binds a handshaken bridge's calls: its messages feed the session's MCP server.
func (h *Hub) prepareBridge(s *socket.Socket, rec Record, session string) error {
	b := &bridge{userID: rec.UserID, daemonID: rec.ID, session: session, raw: s}
	var inW, outW *io.PipeWriter
	b.inR, inW = io.Pipe()
	b.outR, outW = io.Pipe()
	b.in, b.outW = inW, outW
	b.ctx, b.cancel = context.WithCancel(context.Background())
	if err := s.On("disconnect", func(...any) { h.removeBridge(b); b.cancel(); _ = inW.Close() }); err != nil {
		b.cancel()
		return fmt.Errorf("disconnect handler: %w", err)
	}
	if _, err := contract.BindServer(s, bridgeHandler{in: inW}); err != nil {
		b.cancel()
		return fmt.Errorf("rpc bind: %w", err)
	}
	s.SetData(b)
	if err := h.addBridge(rec, b); err != nil {
		b.cancel()
		return err
	}
	return nil
}

// attachBridge starts serving the session's tools to a connected bridge and sends the server's answers back, one
// line per call.
func (h *Hub) attachBridge(raw *socket.Socket, b *bridge) error {
	client, err := contract.NewClient(raw, &contract.ClientOptions{Timeout: ackTimeout})
	if err != nil {
		b.cancel()
		return fmt.Errorf("rpc client: %w", err)
	}
	go func() {
		defer raw.Disconnect(true) // a session that ended, or a grant that was revoked, ends its bridge
		defer b.outW.Close()
		defer b.inR.Close()
		if err := h.opts.Tools.Serve(b.ctx, b.userID, b.session, &mcp.IOTransport{Reader: b.inR, Writer: b.outW}); err != nil {
			log.Printf("daemon: tools of session %s: %v", b.session, err)
		}
	}()
	go relayLines(b.ctx, b.outR, client)
	return nil
}

// relayLines sends each line the MCP server writes to the bridge. It ends when the server's side closes.
func relayLines(ctx context.Context, r io.Reader, client *contract.Client) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if line != "" && line[len(line)-1] == '\n' {
			if cerr := client.CallMcpOut(ctx, line[:len(line)-1]); cerr != nil && ctx.Err() == nil {
				log.Printf("daemon: send to the bridge: %v", cerr)
			}
		}
		if err != nil {
			return
		}
	}
}

// bridgeHandler serves what an MCP bridge calls: mcp.
type bridgeHandler struct{ in io.Writer }

var errNotDaemon = errors.New("this socket is an MCP bridge, not a daemon")

func (bridgeHandler) HandleFrame(context.Context, string, contract.FrameKind, string, float64) error {
	return errNotDaemon
}

func (bridgeHandler) HandleHello(context.Context, contract.Hello) (contract.HelloReply, error) {
	return contract.HelloReply{}, errNotDaemon
}

func (bridgeHandler) HandleAgents(context.Context, []contract.AgentInfo) error { return errNotDaemon }

func (b bridgeHandler) HandleMcp(_ context.Context, line string) error {
	if _, err := io.WriteString(b.in, line+"\n"); err != nil {
		return fmt.Errorf("the tool session has ended: %w", err)
	}
	return nil
}
