//go:build daemontest

// Command server is the test double of the app's server for the daemon's integration tests. It serves the
// generated contract code on the /agentd namespace, exactly as the real server does, and exposes a loopback HTTP
// control API under /ctl so a test can drive it (open, write, close, read the frames, drop the socket).
//
//	go run -tags daemontest ./daemon/test/server -token T [-addr 127.0.0.1:0] [-code TESTCODE]
//
// It prints "LISTEN <host:port>" on stdout once it serves. One MCP tool, ping, answers "pong" through the bridge.
// POST /agentd/pair trades the code -code (default TESTCODE) for the token. hello answers with protocol 1. Installs and
// updates are not here: the real release server (cmd/agentd-server) serves them.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
)

const (
	maxBuffer   = 64 << 20 // the production maxHttpBufferSize
	callTimeout = 90 * time.Second
	inlineLimit = 64 << 10 // longer frame data is reported as length + sha256 only
)

// frameRec is one frame the daemon sent, in arrival order.
type frameRec struct {
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Data    string `json:"data,omitempty"`
	Len     int    `json:"len"`
	SHA     string `json:"sha256"`
	Code    int    `json:"code"`
}

type peer struct {
	raw    *socket.Socket
	client *contract.Client
}

type server struct {
	token string
	io    *socket.Server
	mu    sync.Mutex
	// daemon is the connected daemon socket; hellos are the Hello each (re)connect sent.
	daemon *peer
	hellos []contract.Hello
	// agentLists are the lists the daemon pushed with `agents`, in arrival order.
	agentLists [][]contract.AgentInfo
	frames     []frameRec
	// bridges are the connected MCP bridge sockets.
	bridges []*socket.Socket
	// connects counts accepted /agentd sockets by role.
	connects map[string]int
	rejects  int
	revoked  bool
	// unavailable is how many handshakes are still refused as the hub refuses them while its token store is down.
	unavailable int
	// mcpLines counts the MCP messages that reached the server through bridges.
	mcpLines int
	code     string
	origin   string
}

func main() {
	token := flag.String("token", "test-token", "the token the handshake must carry")
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	code := flag.String("code", "TESTCODE", "the pairing code POST /agentd/pair accepts")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	s := &server{token: *token, connects: map[string]int{}, code: *code}
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(maxBuffer)
	s.io = socket.NewServer(nil, opts)
	ns := s.io.Of("/agentd", nil)
	ns.Use(s.authenticate)
	if err := ns.On("connection", func(c ...any) { s.onConnection(c) }); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", s.io.ServeHandler(nil))
	s.control(mux)
	s.pairRoutes(mux)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	s.origin = "http://" + ln.Addr().String()
	fmt.Println("LISTEN", ln.Addr().String())
	log.Fatal((&http.Server{Handler: mux}).Serve(ln))
}

func (s *server) authenticate(sock *socket.Socket, next func(*socket.ExtendedError)) {
	auth := sock.Handshake().Auth
	role, _ := auth["role"].(string)
	token, _ := auth["token"].(string)
	s.mu.Lock()
	revoked, down := s.revoked, s.unavailable > 0
	if down {
		s.unavailable--
		s.rejects++
	}
	s.mu.Unlock()
	if down {
		next(socket.NewExtendedError("unavailable", map[string]any{"code": "unavailable"}))
		return
	}
	if token != s.token || revoked || (role != "daemon" && role != "mcp") {
		s.mu.Lock()
		s.rejects++
		s.mu.Unlock()
		next(socket.NewExtendedError("unauthorized", nil))
		return
	}
	next(nil)
}

func (s *server) onConnection(clients []any) {
	raw, ok := clients[0].(*socket.Socket)
	if !ok {
		log.Printf("connection event carries %T", clients[0])
		return
	}
	auth := raw.Handshake().Auth
	role, _ := auth["role"].(string)
	client, err := contract.NewClient(raw, &contract.ClientOptions{Timeout: callTimeout})
	if err != nil {
		log.Printf("rpc client: %v", err)
		raw.Disconnect(true)
		return
	}
	p := &peer{raw: raw, client: client}
	h := &handler{s: s}
	if role == "mcp" {
		session, _ := auth["session"].(string)
		b, err := newBridge(client)
		if err != nil {
			log.Printf("bridge: %v", err)
			raw.Disconnect(true)
			return
		}
		h.bridge = b
		log.Printf("mcp bridge connected for session %q", session)
		s.mu.Lock()
		s.bridges = append(s.bridges, raw)
		s.mu.Unlock()
		_ = raw.On("disconnect", func(...any) { b.in.Close() })
	} else {
		_ = raw.On("disconnect", func(...any) {
			s.mu.Lock()
			if s.daemon == p {
				s.daemon = nil
			}
			s.mu.Unlock()
			log.Print("daemon disconnected")
		})
	}
	// The daemon is current before its first call can arrive: it says hello the moment it connects.
	s.mu.Lock()
	s.connects[role]++
	if role == "daemon" {
		s.daemon = p
	}
	s.mu.Unlock()
	if _, err := contract.BindServer(raw, h); err != nil {
		log.Printf("bind: %v", err)
		raw.Disconnect(true)
		return
	}
	log.Printf("%s connected (%s)", role, raw.Id())
}

// handler implements contract.ServerHandler for one socket.
type handler struct {
	s      *server
	bridge *bridge
}

func (h *handler) HandleFrame(_ context.Context, sid string, kind contract.FrameKind, data string, code float64) error {
	sum := sha256.Sum256([]byte(data))
	rec := frameRec{Session: sid, Kind: string(kind), Len: len(data), SHA: hex.EncodeToString(sum[:]), Code: int(code)}
	if len(data) <= inlineLimit {
		rec.Data = data
	}
	h.s.mu.Lock()
	h.s.frames = append(h.s.frames, rec)
	h.s.mu.Unlock()
	return nil
}

func (h *handler) HandleHello(_ context.Context, info contract.Hello) (contract.HelloReply, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.s.hellos = append(h.s.hellos, info)
	return contract.HelloReply{Protocol: 1}, nil
}

func (h *handler) HandleAgents(_ context.Context, agents []contract.AgentInfo) error {
	h.s.mu.Lock()
	h.s.agentLists = append(h.s.agentLists, agents)
	h.s.mu.Unlock()
	return nil
}

func (h *handler) HandleMcp(_ context.Context, line string) error {
	if h.bridge == nil {
		return errors.New("not an mcp bridge")
	}
	h.s.mu.Lock()
	h.s.mcpLines++
	h.s.mu.Unlock()
	_, err := h.bridge.in.Write([]byte(line + "\n"))
	return err
}

// ---- MCP: an in-memory MCP server per bridge socket, its lines relayed through mcp / mcpOut ----

type bridge struct{ in *io.PipeWriter }

func newBridge(client *contract.Client) (*bridge, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := mcp.NewServer(&mcp.Implementation{Name: "app", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping", Description: "Returns the text pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	if _, err := srv.Connect(context.Background(), &mcp.IOTransport{Reader: inR, Writer: outW}, nil); err != nil {
		return nil, err
	}
	go func() {
		var pending []byte
		tmp := make([]byte, 64<<10)
		for {
			n, err := outR.Read(tmp)
			pending = append(pending, tmp[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				if e := client.CallMcpOut(context.Background(), string(pending[:i])); e != nil {
					log.Printf("mcpOut: %v", e)
				}
				pending = pending[i+1:]
			}
			if err != nil {
				return
			}
		}
	}()
	return &bridge{in: inW}, nil
}

// ---- control API (test only) ----

func (s *server) control(mux *http.ServeMux) {
	mux.HandleFunc("GET /ctl/status", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		reply(w, map[string]any{
			"connected": s.daemon != nil, "hellos": s.hellos, "agentLists": s.agentLists, "connects": s.connects,
			"rejects": s.rejects, "frames": len(s.frames), "mcpLines": s.mcpLines,
		})
	})
	mux.HandleFunc("POST /ctl/restart", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		p := s.daemon
		s.mu.Unlock()
		if p == nil {
			http.Error(w, "no daemon", http.StatusConflict)
			return
		}
		replyResult(w, true, p.client.CallRestart(r.Context()))
	})
	mux.HandleFunc("POST /ctl/stop", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		p := s.daemon
		s.mu.Unlock()
		if p == nil {
			http.Error(w, "no daemon", http.StatusConflict)
			return
		}
		replyResult(w, true, p.client.CallStop(r.Context()))
	})
	// revoke refuses every later handshake and drops the daemon that is connected.
	mux.HandleFunc("POST /ctl/revoke", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.revoked = true
		p := s.daemon
		s.mu.Unlock()
		if p != nil {
			p.raw.Disconnect(false)
		}
		reply(w, true)
	})
	// unavailable refuses the next n handshakes the way the hub does while it cannot look tokens up.
	mux.HandleFunc("POST /ctl/unavailable", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ N int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.unavailable = in.N
		s.mu.Unlock()
		reply(w, true)
	})
	mux.HandleFunc("GET /ctl/frames", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		reply(w, s.frames)
	})
	mux.HandleFunc("POST /ctl/open", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Session, Agent string }
		p := s.decode(w, r, &in)
		if p == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		defer cancel()
		op, err := p.client.CallOpen(ctx, in.Session, in.Agent)
		replyResult(w, op, err)
	})
	mux.HandleFunc("POST /ctl/write", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Session, Line string }
		p := s.decode(w, r, &in)
		if p == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		defer cancel()
		ok, err := p.client.CallWrite(ctx, in.Session, in.Line)
		replyResult(w, ok, err)
	})
	mux.HandleFunc("POST /ctl/close", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Session string }
		p := s.decode(w, r, &in)
		if p == nil {
			return
		}
		replyResult(w, true, p.client.CallClose(r.Context(), in.Session))
	})
	// kick drops the daemon's socket: transport=1 closes the connection (the client sees "transport close"),
	// otherwise only the namespace socket is disconnected ("io server disconnect").
	mux.HandleFunc("POST /ctl/kick", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("role") == "mcp" {
			s.mu.Lock()
			bridges := s.bridges
			s.bridges = nil
			s.mu.Unlock()
			for _, b := range bridges {
				b.Disconnect(true)
			}
			reply(w, len(bridges))
			return
		}
		s.mu.Lock()
		p := s.daemon
		s.mu.Unlock()
		if p == nil {
			http.Error(w, "no daemon", http.StatusConflict)
			return
		}
		p.raw.Disconnect(r.URL.Query().Get("transport") == "1")
		reply(w, true)
	})
}

// decode reads the JSON body into in and returns the connected daemon, or answers 4xx and returns nil.
func (s *server) decode(w http.ResponseWriter, r *http.Request, in any) *peer {
	if err := json.NewDecoder(r.Body).Decode(in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil
	}
	s.mu.Lock()
	p := s.daemon
	s.mu.Unlock()
	if p == nil {
		http.Error(w, "no daemon", http.StatusConflict)
	}
	return p
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// replyResult answers {"ok":<value>} or {"error":"<message>"} (an RpcError from the daemon arrives as err).
func replyResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		reply(w, map[string]any{"error": err.Error()})
		return
	}
	reply(w, map[string]any{"ok": v})
}

// ---- pairing (test only) ----

// pairRoutes serves the pairing endpoint of an app: the code is traded for the daemon's token.
func (s *server) pairRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /agentd/pair", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Code != s.code {
			http.Error(w, "unknown or used code", http.StatusForbidden)
			return
		}
		reply(w, map[string]string{"token": s.token})
	})
}
