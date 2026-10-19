package hub

import (
	"errors"
	"io"
	"sync"
)

// Protocol is the version of the daemon contract this hub speaks. A daemon that is told another one leaves.
const Protocol = 1

// Record is a paired daemon as the token store keeps it. TokenHash is the SHA-256 of its token: the token itself is
// never stored.
type Record struct {
	ID, UserID, TokenHash, Name, Hostname, OS, Arch, Created string
	LastSeen                                                 int64
}

// ErrNotFound is what a TokenStore returns for a daemon it does not have.
var ErrNotFound = errors.New("daemon not found")

// ErrNameTaken is what a TokenStore returns when the user already has another daemon with that name.
var ErrNameTaken = errors.New("another daemon of yours already has that name")

// ErrNoDaemon means the user has no daemon online: their agents run on their own machine, so none can start.
var ErrNoDaemon = errors.New("No daemon is connected: pair one in your account settings") //nolint:staticcheck // ST1005: the text is shown to the user as written

// ErrUnknownDaemon means the user has no paired daemon with the id a call named: it was disconnected.
var ErrUnknownDaemon = errors.New("The daemon chosen is no longer paired: pick another") //nolint:staticcheck // ST1005: the text is shown to the user as written

// OfflineError means the daemon is paired but not connected.
type OfflineError struct{ Name string }

func (e OfflineError) Error() string {
	return "Your daemon " + quote(e.Name) + " is offline: start it, or pick another daemon"
}

func quote(s string) string { return `"` + s + `"` }

// Conn is one agent process on the user's machine, seen from the app's server: the bytes of its stdin and stdout, and
// the process behind them. The server speaks ACP over it as it would over a local pipe.
type Conn interface {
	// Read is the agent's stdout; io.EOF once the process is gone and everything it wrote was read. Write is its
	// stdin, whole lines.
	io.ReadWriter
	// Cwd is the directory the daemon started the agent in.
	Cwd() string
	// Stderr is the last of what the agent wrote on its standard error.
	Stderr() string
	// Close ends the process and everything it started. It does not block.
	Close()
	// Done is closed once the process is gone and its last words are in Stderr.
	Done() <-chan struct{}
}

// Ref names one daemon of a user: ID is what an app stores, Name is what the user sees.
type Ref struct{ ID, Name string }

// TailBuffer keeps the last Max bytes written to it. It holds an agent's standard error.
type TailBuffer struct {
	Max int
	mu  sync.Mutex
	buf []byte
}

func (b *TailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.Max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *TailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// List is the user's daemons as an app's UI shows them, with the pending pairing code, if any.
type List struct {
	Daemons []Info  `json:"daemons"`
	Pairing Pairing `json:"pairing"`
	// LatestVersion is the newest daemon release the app knows of (Options.LatestVersion): empty when it does not say.
	LatestVersion string `json:"latestVersion"`
}

// Info is one paired daemon.
type Info struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Hostname string  `json:"hostname"`
	State    string  `json:"state"`
	Version  string  `json:"version"`
	OS       string  `json:"os"`
	Agents   []Agent `json:"agents"`
	LastSeen int64   `json:"lastSeen"`
	Sessions int     `json:"sessions"`
	Managed  bool    `json:"managed"`
}

// Agent is an agent a daemon can start. Ready is false when its machine lacks what it needs; Reason says what.
type Agent struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// Pairing is the one pending pairing code of a user: Code is empty when there is none, ExpiresAt is Unix milliseconds.
type Pairing struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
}
