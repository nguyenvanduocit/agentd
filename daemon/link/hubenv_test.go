package link

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

const user = "alice"

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// store is the hub's token store in a test: it counts the handshakes that present each token, can fail its lookups
// as a database does while it is down, and with acceptAny knows every token.
type store struct {
	goserver.Tokens
	acceptAny bool
	down      atomic.Bool

	mu     sync.Mutex
	counts map[string]int // token hash -> handshakes
}

func (s *store) ByHash(hash string) (goserver.Record, error) {
	s.mu.Lock()
	s.counts[hash]++
	s.mu.Unlock()
	if s.down.Load() {
		return goserver.Record{}, errors.New("connection refused: the database is down")
	}
	if s.acceptAny {
		return goserver.Record{ID: hash[:32], UserID: user, TokenHash: hash, Name: "any"}, nil
	}
	return s.Tokens.ByHash(hash)
}

func (s *store) ByID(userID, id string) (goserver.Record, error) {
	if s.acceptAny {
		return goserver.Record{ID: id, UserID: userID}, nil
	}
	return s.Tokens.ByID(userID, id)
}

func (s *store) Seen(id string, at int64) error {
	if s.acceptAny {
		return nil
	}
	return s.Tokens.Seen(id, at)
}

// handshakesOf is how many handshakes presented token.
func (s *store) handshakesOf(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[sha256hex(token)]
}

// hubEnv is the real hub on an httptest server.
type hubEnv struct {
	hub    *goserver.Server
	store  *store
	tokens *goserver.MemoryTokens
	srv    *httptest.Server
}

func newHubEnv(t *testing.T, acceptAny bool) *hubEnv {
	t.Helper()
	e := &hubEnv{tokens: goserver.NewMemoryTokens()}
	e.store = &store{Tokens: e.tokens, acceptAny: acceptAny, counts: map[string]int{}}
	e.hub = goserver.New(goserver.Options{Tokens: e.store})
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(goserver.MaxMessageBytes)
	sio := socket.NewServer(nil, opts)
	mux := http.NewServeMux()
	e.hub.Mount(sio, mux)
	mux.Handle("/socket.io/", sio.ServeHandler(nil))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() { sio.Close(nil); e.srv.Close() })
	return e
}

// pair is what `agentd pair` leaves in daemon.json: a paired daemon of user, and the token it holds.
func (e *hubEnv) pair(t *testing.T, token string) goserver.Record {
	t.Helper()
	rec, err := e.tokens.Add(goserver.Record{ID: sha256hex(token)[:32], UserID: user, TokenHash: sha256hex(token), Name: "laptop", Hostname: "laptop", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func (e *hubEnv) waitState(t *testing.T, id, state string) goserver.Info {
	t.Helper()
	var info goserver.Info
	waitFor(t, "the daemon to be "+state, func() bool {
		list, err := e.hub.List(user)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range list.Daemons {
			if d.ID == id && d.State == state {
				info = d
				return true
			}
		}
		return false
	})
	return info
}

// open starts the agent on the user's daemon id.
func (e *hubEnv) open(t *testing.T, id string) goserver.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	c, err := e.hub.Open(ctx, user, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// readLine reads one line of c's stdout.
func readLine(t *testing.T, c goserver.Conn) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		var b strings.Builder
		one := make([]byte, 1)
		for {
			n, err := c.Read(one)
			if n > 0 {
				if one[0] == '\n' {
					got <- result{b.String(), nil}
					return
				}
				b.WriteByte(one[0])
			}
			if err != nil {
				got <- result{b.String(), err}
				return
			}
		}
	}()
	select {
	case r := <-got:
		if r.err != nil && !errors.Is(r.err, io.EOF) {
			t.Fatal(r.err)
		}
		return r.line
	case <-time.After(waitLimit):
		t.Fatal("no line from the agent")
		return ""
	}
}

// proxy is a TCP relay between a daemon and a server that a test can cut, as a network does.
type proxy struct {
	l     net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{l: l}
	t.Cleanup(func() { _ = l.Close(); p.sever() })
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				_ = in.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, in, out)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(out, in); _ = out.Close() }()
			go func() { _, _ = io.Copy(in, out); _ = in.Close() }()
		}
	}()
	return p
}

func (p *proxy) url() string { return "http://" + p.l.Addr().String() }

// sever cuts every connection that goes through the proxy.
func (p *proxy) sever() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}
