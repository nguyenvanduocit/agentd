package tray

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// Link shows the daemon's state and hands back the user's choices: the tray program the daemon starts (Tray), or the
// app that started it (AppLink).
type Link interface {
	Start()
	Update(State)
	Stop()
}

// FDConn is the channel on the inherited descriptor fd (see UIFd). It takes the descriptor over: it is closed with
// the connection.
func FDConn(fd int) (net.Conn, error) {
	file := os.NewFile(uintptr(fd), "agentd-ui")
	if file == nil {
		return nil, errors.New("invalid descriptor")
	}
	defer func() { _ = file.Close() }() // net.FileConn duplicated it
	return net.FileConn(file)
}

// AppLink is the app that started the daemon (agentd.app), on the channel it handed over: the tray's protocol in both
// directions, JSON lines, states from the daemon and the user's choices from the app. The app owns the daemon: when
// its end closes (the app quit, or died), onGone is called, and it ends the daemon.
type AppLink struct {
	conn     net.Conn
	onAction func(Action)
	onGone   func()
	log      *proc.Logger

	// stopped: Stop was called. down: the channel is closed, by Stop or by the app.
	stopped, down atomic.Bool

	mu      sync.Mutex // guards started, last and the writes
	started bool
	last    string
}

// NewAppLink makes the link on conn (FDConn of the app's descriptor).
func NewAppLink(conn net.Conn, onAction func(Action), onGone func(), log *proc.Logger) *AppLink {
	return &AppLink{conn: conn, onAction: onAction, onGone: onGone, log: log}
}

// Start reads the app's choices and sends the state that is known.
func (l *AppLink) Start() {
	go l.read()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = true
	l.send()
}

func (l *AppLink) read() {
	lines := proc.NewLineSplitter(proc.MaxLineBytes)
	buf := make([]byte, 32*1024)
	for {
		n, err := l.conn.Read(buf)
		for _, line := range lines.Push(buf[:n]) {
			if action, ok := ParseLine(line); ok {
				l.onAction(action)
			}
		}
		if err != nil {
			l.down.Store(true)
			if l.stopped.Load() {
				return
			}
			if !errors.Is(err, io.EOF) {
				l.log.Warn("app: " + err.Error())
			}
			l.onGone()
			return
		}
	}
}

// Update is the state to show now; nothing is sent when it did not change.
func (l *AppLink) Update(state State) {
	text := state.line()
	l.mu.Lock()
	defer l.mu.Unlock()
	if text == l.last {
		return
	}
	l.last = text
	l.send()
}

// Stop closes the channel; onGone is not called for it. A write in flight ends with the close.
func (l *AppLink) Stop() {
	l.stopped.Store(true)
	l.down.Store(true)
	_ = l.conn.Close()
}

// send writes the last state; the caller holds mu.
func (l *AppLink) send() {
	if !l.started || l.last == "" || l.down.Load() {
		return
	}
	if _, err := io.WriteString(l.conn, l.last+"\n"); err != nil && !l.down.Load() {
		l.log.Warn("app: " + err.Error())
	}
}
