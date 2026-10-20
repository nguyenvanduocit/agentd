package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/gorilla/websocket"
)

// wsStream is what a client library needs from a WebSocket that carries ACP: every Write is one JSON-RPC message sent as
// one text frame, and Read hands back the frames, each ended by a newline, as the library's stdio framing expects.
type wsStream struct {
	ws      *websocket.Conn
	pending []byte
}

func (s *wsStream) Write(p []byte) (int, error) {
	for _, piece := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if piece == "" {
			continue
		}
		if err := s.ws.WriteMessage(websocket.TextMessage, []byte(piece)); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (s *wsStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		kind, data, err := s.ws.ReadMessage()
		if err != nil {
			return 0, io.EOF
		}
		if kind == websocket.TextMessage {
			s.pending = append(data, '\n')
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// nopClient is the app's side of ACP; initialize needs none of its methods.
type nopClient struct{ acp.Client }

func TestAcpTheOfficialClientLibraryWorksOverIt(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})
	c := r.mustDialDeaf("/acp/claude", "")
	stream := &wsStream{ws: c.ws}
	conn := acp.NewClientSideConnection(&nopClient{}, stream, stream)

	ctx, cancel := context.WithTimeout(context.Background(), acpWait)
	defer cancel()
	first, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if first.ProtocolVersion != 1 {
		t.Fatalf("protocol version %v, want 1", first.ProtocolVersion)
	}

	// A JSON-RPC error of the agent comes back to the library as an error of the call, not as a hang.
	_, err = conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	var failure *acp.RequestError
	if !errors.As(err, &failure) || !strings.Contains(failure.Message, "already initialized") {
		t.Fatalf("second Initialize: %v, want the agent's error", err)
	}
}

func TestAcpTheOfficialClientLibraryMeetsAPageRuleAsAnErrorNotAHang(t *testing.T) {
	work := shortDir(t)
	r, _, _ := newAcpRig(t, acpRigOptions{folders: []string{work}})
	c := r.mustDialDeaf("/acp/claude", page)
	stream := &wsStream{ws: c.ws}
	conn := acp.NewClientSideConnection(&nopClient{}, stream, stream)

	ctx, cancel := context.WithTimeout(context.Background(), acpWait)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/etc", McpServers: []acp.McpServer{}})
	var failure *acp.RequestError
	if !errors.As(err, &failure) || failure.Code != -32602 || !strings.Contains(failure.Message, "outside the folders") {
		t.Fatalf("NewSession outside the grant: %v, want a -32602 error", err)
	}
}
