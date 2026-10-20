package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A server that stops reading while the daemon sends frames: the frames fill the socket's buffers and the write of
// the one in flight stops. What the daemon owes the server then is a read loop that goes on (the server's events are
// not held up by a write) and answers that go before the frames that wait.

// stalledPeer is the server side of one connection, by hand: it opens the Engine.IO session, accepts the namespace,
// and reads nothing until resume is closed. After that it records the start of every message it reads.
type stalledPeer struct {
	srv    *httptest.Server
	ws     chan *websocket.Conn
	resume chan struct{}
	once   sync.Once

	mu  sync.Mutex
	got []string
}

func newStalledPeer(t *testing.T) *stalledPeer {
	t.Helper()
	p := &stalledPeer{ws: make(chan *websocket.Conn, 1), resume: make(chan struct{})}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`0{"sid":"x","pingInterval":60000,"pingTimeout":60000}`))
		if _, _, err := ws.ReadMessage(); err != nil { // the namespace CONNECT
			return
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`40/agentd,{"sid":"ns"}`))
		p.ws <- ws
		<-p.resume
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.got = append(p.got, string(msg[:min(len(msg), 48)]))
			p.mu.Unlock()
		}
	}))
	t.Cleanup(func() {
		p.read()
		select {
		case ws := <-p.ws:
			_ = ws.Close()
		default:
		}
		p.srv.Close()
	})
	return p
}

// read lets the server read what the daemon sent.
func (p *stalledPeer) read() { p.once.Do(func() { close(p.resume) }) }

func (p *stalledPeer) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.got)
}

// indexOf is the place in what the server read of the first message that starts with prefix; -1 before it came.
func (p *stalledPeer) indexOf(prefix string) int {
	return slices.IndexFunc(p.seen(), func(m string) bool { return strings.HasPrefix(m, prefix) })
}

// stalledConn dials the peer and sends 1 MiB frames until one of them does not return: its write is stuck.
func stalledConn(t *testing.T) (*stalledPeer, *conn, *websocket.Conn) {
	t.Helper()
	p := newStalledPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := dialConn(ctx, p.srv.URL, "/agentd", map[string]string{"role": "daemon", "token": "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.finish(reasonClient) })
	var ws *websocket.Conn
	select {
	case ws = <-p.ws:
	case <-time.After(3 * time.Second):
		t.Fatal("the peer never accepted the namespace")
	}
	megabyte := strings.Repeat("x", 1<<20)
	for i := 0; i < 64; i++ {
		done := make(chan struct{})
		go func() {
			_ = c.emit("frame", "big", "out", megabyte, 0)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
			return p, c, ws
		}
	}
	t.Fatal("the socket took 64 MiB that nobody read: this machine cannot stall a write for the test")
	return nil, nil, nil
}

func sendText(t *testing.T, ws *websocket.Conn, text string) {
	t.Helper()
	if err := ws.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		t.Fatal(err)
	}
}

func TestAStuckWriteDoesNotStopTheReadLoop(t *testing.T) {
	_, c, ws := stalledConn(t)
	events := make(chan string, 4)
	go c.serve(func(name string, _ []json.RawMessage, _ func(...any)) { events <- name })

	sendText(t, ws, "2") // a ping: the answer is a write, and the write is stuck
	sendText(t, ws, `42/agentd,["probe"]`)
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("an event of the server waited behind the answer to a ping, which waited behind a stuck write")
	}
}

func TestPongsAndAcknowledgementsGoBeforeTheFramesThatWait(t *testing.T) {
	p, c, ws := stalledConn(t)
	type call struct {
		ack func(...any)
	}
	calls := make(chan call, 1)
	go c.serve(func(name string, _ []json.RawMessage, ack func(...any)) {
		if name == "probe" {
			calls <- call{ack}
		}
	})
	for _, id := range []string{"q1", "q2", "q3"} { // three frames wait for the one that is stuck
		go func() { _ = c.emit("frame", id, "out", "x", 0) }()
	}
	time.Sleep(200 * time.Millisecond) // let them queue

	sendText(t, ws, "2")
	sendText(t, ws, `42/agentd,7["probe"]`)
	var probe call
	select {
	case probe = <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe never reached the handler: the read loop is stuck")
	}
	answered := make(chan struct{})
	go func() {
		probe.ack(true)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("an acknowledgement waited behind the frames")
	}

	p.read()
	waitFor(t, "the pong, the acknowledgement and the three frames", func() bool {
		return p.indexOf("3") >= 0 && p.indexOf(`43/agentd,7[true]`) >= 0 &&
			p.indexOf(`42/agentd,["frame","q1"`) >= 0 && p.indexOf(`42/agentd,["frame","q2"`) >= 0 && p.indexOf(`42/agentd,["frame","q3"`) >= 0
	})
	firstFrame := min(p.indexOf(`42/agentd,["frame","q1"`), p.indexOf(`42/agentd,["frame","q2"`), p.indexOf(`42/agentd,["frame","q3"`))
	if pong := p.indexOf("3"); pong > firstFrame {
		t.Errorf("the pong went out at %d, after a frame that waited (%d): %q", pong, firstFrame, p.seen())
	}
	if ack := p.indexOf(`43/agentd,7[true]`); ack > firstFrame {
		t.Errorf("the acknowledgement went out at %d, after a frame that waited (%d): %q", ack, firstFrame, p.seen())
	}
}

func TestTheWriterEndsWithTheConnectionAndReleasesWhoWaits(t *testing.T) {
	_, c, _ := stalledConn(t)
	results := make(chan error, 3)
	for _, id := range []string{"q1", "q2", "q3"} {
		go func() { results <- c.emit("frame", id, "out", "x", 0) }()
	}
	time.Sleep(200 * time.Millisecond) // let them queue behind the stuck write

	c.finish(reasonClient)
	for range 3 {
		select {
		case err := <-results:
			if !errors.Is(err, errDisconnected) {
				t.Errorf("a frame that waited when the connection ended got %v, want errDisconnected", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a frame that waited for the writer was never released")
		}
	}
	select {
	case <-c.writerEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer goroutine outlived its connection")
	}
	if err := c.signal("x"); !errors.Is(err, errDisconnected) {
		t.Errorf("a control message after the end got %v, want errDisconnected", err)
	}
}

func TestAFloodOfControlMessagesCannotStarveAFrame(t *testing.T) {
	p, c, _ := stalledConn(t)
	go func() { _ = c.emit("frame", "fair", "out", "x", 0) }()
	time.Sleep(200 * time.Millisecond) // the frame waits behind the stuck write
	const flood = 40
	for i := range flood {
		if err := c.signal(fmt.Sprintf("flood%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	p.read()
	waitFor(t, "the frame and the whole flood", func() bool {
		return p.indexOf(`42/agentd,["frame","fair"`) >= 0 && p.indexOf(fmt.Sprintf("flood%d", flood-1)) >= 0
	})
	frame, first, last := p.indexOf(`42/agentd,["frame","fair"`), p.indexOf("flood0"), p.indexOf(fmt.Sprintf("flood%d", flood-1))
	if frame > last {
		t.Errorf("the frame went out after all %d control messages", flood)
	}
	if frame-first > maxControlRun {
		t.Errorf("the frame went out after %d control messages, want at most %d", frame-first, maxControlRun)
	}
}
