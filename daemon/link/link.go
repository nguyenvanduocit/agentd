// Package link is the daemon's side of its connection to an app's server: one Socket.IO socket to the server's
// /agentd namespace per app (the contract of contract/define.acp.ts), and the AppHub that gives each app its own
// agents.Fleet over that socket.
package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// Refused is what the hub answers a token it will never accept (unknown or revoked). Any other refusal, such as
// "unavailable" (the server could not check the token now), is retried.
const Refused = "unauthorized"

// DefaultConnectTimeout bounds one attempt to connect: the WebSocket, the Engine.IO open and the namespace answer.
const DefaultConnectTimeout = 20 * time.Second

// ReconnectBackoff is the wait between attempts to connect: 500 ms doubling to 15 s, jittered by up to half.
func ReconnectBackoff() home.BackoffOptions {
	return home.BackoffOptions{Base: 500 * time.Millisecond, Max: 15 * time.Second, Jitter: 0.5}
}

// Options say where a Link connects and how it retries.
type Options struct {
	// Server is the server origin (http or https), as home.ServerOrigin returns it.
	Server string
	// Auth is the handshake data: {role: "daemon", token}.
	Auth map[string]string
	// MaxRetries gives up after this many failed reconnects in a row; 0 retries for as long as the daemon runs.
	MaxRetries int
	// Backoff is the wait between attempts; the zero value is ReconnectBackoff().
	Backoff home.BackoffOptions
	// ConnectTimeout bounds one attempt; the zero value is DefaultConnectTimeout.
	ConnectTimeout time.Duration
}

// Events is what a Link reports. Each is optional. They run on the goroutine that connects, so none may wait for
// the Link: start that work on a goroutine of its own.
type Events struct {
	// OnConnect runs on every (re)connect, after the handshake was accepted.
	OnConnect func()
	// OnDisconnect runs when a connection ended; reason is socket.io's ("io server disconnect", "transport close",
	// "ping timeout", "io client disconnect", ...).
	OnDisconnect func(reason string)
	// OnTrouble is network trouble, or a server that could not take the handshake now; the Link retries by itself.
	OnTrouble func(message string)
	// OnRejected is the server refusing the token. Retrying cannot help: it is wrong or revoked. The Link stopped.
	OnRejected func(message string)
	// OnGiveUp is the Link giving up after Options.MaxRetries failed reconnects. Never, when those are unlimited.
	OnGiveUp func()
}

func (e Events) connect() {
	if e.OnConnect != nil {
		e.OnConnect()
	}
}

func (e Events) disconnect(reason string) {
	if e.OnDisconnect != nil {
		e.OnDisconnect(reason)
	}
}

func (e Events) trouble(message string) {
	if e.OnTrouble != nil {
		e.OnTrouble(message)
	}
}

func (e Events) rejected(message string) {
	if e.OnRejected != nil {
		e.OnRejected(message)
	}
}

func (e Events) giveUp() {
	if e.OnGiveUp != nil {
		e.OnGiveUp()
	}
}

// Link is the daemon's connection to one app's server: the calls the server makes are answered by the Handler, the
// calls to the server are methods, and Sink is a frame sink tied to the connection that is current.
type Link struct {
	o         Options
	h         ClientHandler
	ev        Events
	origin    string
	namespace string

	ctx       context.Context //nolint:containedctx // the life of the Link, from Start to Close
	cancel    context.CancelFunc
	startOnce sync.Once
	done      chan struct{} // closed when the connection loop has ended

	mu    sync.Mutex
	c     *conn  // the connected socket, nil between connections
	epoch uint64 // changes on every connect and disconnect
}

// New makes a Link that is not connected yet: it connects once Start is called, so that whoever holds the Link is
// complete before the first event arrives.
func New(o Options, h ClientHandler, ev Events) (*Link, error) {
	u, err := url.Parse(home.NamespaceURL(o.Server))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("the server %q is not an http(s) origin", o.Server)
	}
	if o.Backoff == (home.BackoffOptions{}) {
		o.Backoff = ReconnectBackoff()
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	l := &Link{o: o, h: h, ev: ev, origin: u.Scheme + "://" + u.Host, namespace: u.Path, done: make(chan struct{})}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	return l, nil
}

// Start connects, and keeps the Link connected until Close, a refused token or Options.MaxRetries.
func (l *Link) Start() {
	l.startOnce.Do(func() { go l.run() })
}

// run connects, serves the connection, and connects again after a backoff, until the Link is closed or cannot go on.
func (l *Link) run() {
	defer close(l.done)
	failures := 0 // reconnects that failed since the last connection
	for {
		c, err := l.dial()
		switch {
		case l.ctx.Err() != nil:
			return
		case err != nil:
			var refused *refusal
			if errors.As(err, &refused) && refused.message == Refused {
				l.ev.rejected(refused.message)
				return
			}
			l.ev.trouble(err.Error())
		default:
			failures = 0
			l.serve(c)
			if l.ctx.Err() != nil {
				return
			}
		}
		if l.o.MaxRetries > 0 && failures >= l.o.MaxRetries {
			l.ev.giveUp()
			return
		}
		timer := time.NewTimer(home.BackoffDelay(failures, l.o.Backoff, nil))
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		failures++
	}
}

func (l *Link) dial() (*conn, error) {
	ctx, cancel := context.WithTimeout(l.ctx, l.o.ConnectTimeout)
	defer cancel()
	return dialConn(ctx, l.origin, l.namespace, l.o.Auth)
}

// serve runs one connection from its handshake to its end.
func (l *Link) serve(c *conn) {
	lanes := newLanes()
	l.mu.Lock()
	l.c = c
	l.epoch++
	l.mu.Unlock()
	stop := context.AfterFunc(l.ctx, c.leave)
	l.ev.connect()
	reason := c.serve(func(name string, args []json.RawMessage, ack func(...any)) {
		l.dispatch(c, lanes, name, args, ack)
	})
	stop()
	l.mu.Lock()
	l.c = nil
	l.epoch++
	l.mu.Unlock()
	l.ev.disconnect(reason)
}

// current is the connected socket, or nil.
func (l *Link) current() *conn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c
}

// Connected reports whether the socket is connected now.
func (l *Link) Connected() bool { return l.current() != nil }

// Sink is a frame sink for the connection that is current now. Frames go out only while that same connection is up:
// after a drop the server has forgotten the sessions, so a frame sent after the reconnect would be stale.
func (l *Link) Sink() agents.FrameSink {
	l.mu.Lock()
	defer l.mu.Unlock()
	return sink{l: l, epoch: l.epoch}
}

type sink struct {
	l     *Link
	epoch uint64
}

func (s sink) Send(sessionID string, kind agents.FrameKind, data string, code int) {
	s.l.mu.Lock()
	c := s.l.c
	if s.l.epoch != s.epoch {
		c = nil
	}
	s.l.mu.Unlock()
	if c != nil {
		_ = c.emit("frame", sessionID, string(kind), data, code)
	}
}

// Close ends the connection and the retries. It returns once the connection is closed (what was queued before has
// gone out), or after grace.
func (l *Link) Close(grace time.Duration) {
	l.cancel()
	l.startOnce.Do(func() { close(l.done) }) // never started: nothing to wait for
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-l.done:
		return
	case <-timer.C:
	}
	if c := l.current(); c != nil {
		c.finish(reasonClient)
	}
	select {
	case <-l.done:
	case <-time.After(time.Second):
	}
}
