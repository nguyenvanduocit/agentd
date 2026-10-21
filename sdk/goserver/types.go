package goserver

import (
	"context"
	"errors"
	"io"
	"sync"
)

// Protocol is the version of the daemon contract this Server speaks. A daemon whose range of protocols (its hello)
// does not hold it is not online: Info.Incompatible says why.
const Protocol = 1

// Record is a paired daemon as the Tokens store keeps it. TokenHash is the SHA-256 of its token: the token itself is
// never stored.
type Record struct {
	// ID is the daemon's id, made when it paired: what an app stores and passes to Open.
	ID string
	// UserID is the user the daemon belongs to.
	UserID string
	// TokenHash is the hex SHA-256 of the daemon's token; a handshake is found by it (Tokens.ByHash).
	TokenHash string
	// Name is what the user calls the daemon; it starts as the machine's hostname.
	Name string
	// Hostname, OS and Arch are what the machine said about itself when it paired.
	Hostname, OS, Arch string
	// Created is when the daemon paired, RFC 3339.
	Created string
	// LastSeen is when the daemon last connected or disconnected, in Unix milliseconds; 0 when it never has.
	LastSeen int64
}

// ErrNotFound is what a Tokens store returns for a daemon it does not have. A Server never returns it: its methods
// answer ErrUnknownDaemon.
var ErrNotFound = errors.New("computer not found")

// ErrNameTaken is what a Tokens store returns when the user already has another daemon with that name. Rename passes
// it on (wrapped; test with errors.Is).
var ErrNameTaken = errors.New("another computer of yours already has that name")

// ErrNoDaemon is what OpenLatest returns when the user has no paired daemon: their agents run on their own computer,
// so none can start. The text names no place in an app; an app that wants to point at its pairing view says so itself
// (errors.Is).
var ErrNoDaemon = errors.New("No computer is connected: pair one first") //nolint:staticcheck // ST1005: the text is shown to the user as written

// ErrNoOnlineDaemon is what OpenLatest returns when the user has paired daemons and none of them is online. Since 0.7.0.
var ErrNoOnlineDaemon = errors.New("None of your computers is online: start one, or pair another") //nolint:staticcheck // ST1005: the text is shown to the user as written

// ErrUnknownDaemon means the user has no paired daemon with the id a call named: it never existed, it was
// disconnected, or it belongs to someone else. Every method of a Server answers it for such an id.
var ErrUnknownDaemon = errors.New("The computer chosen is no longer paired: pick another") //nolint:staticcheck // ST1005: the text is shown to the user as written

// ErrNoSuchDaemon is ErrUnknownDaemon.
//
// Deprecated: Revoke, Rename, Restart and Stop used to answer this error and Open another for the same case; they all
// answer ErrUnknownDaemon now, and this name is the same value.
var ErrNoSuchDaemon = ErrUnknownDaemon

// OfflineError means the daemon is paired but not connected. The text is a sentence for the user.
type OfflineError struct {
	// Name is the daemon's name.
	Name string
}

// Error is a sentence for the user: it names the daemon and says to start it or pick another.
func (e OfflineError) Error() string {
	return "Your daemon " + quote(e.Name) + " is offline: start it, or pick another daemon"
}

// offlineCall is the error of a Restart or Stop on a daemon that is offline: its text names the daemon, and errors.As
// finds the OfflineError underneath.
type offlineCall struct{ OfflineError }

func (e offlineCall) Error() string { return e.Name + " is offline" }
func (e offlineCall) Unwrap() error { return e.OfflineError }

func quote(s string) string { return `"` + s + `"` }

// IncompatibleError means the daemon is paired and connected, but it and this app speak protocols that do not meet:
// Reason says which of them to update, as a sentence for a person.
type IncompatibleError struct {
	// Name is the daemon's name.
	Name string
	// Reason is a sentence naming the side to update.
	Reason string
}

// Error is a sentence for the user: it names the daemon and gives Reason.
func (e IncompatibleError) Error() string {
	return "Your daemon " + quote(e.Name) + " cannot work with this app: " + e.Reason
}

// AgentNotReadyError means the daemon cannot start the agent now: it does not have it at all (Offered is false), or it
// has it and says it is not ready (Reason, when it gave one). Test with errors.As. Since 0.7.0.
type AgentNotReadyError struct {
	// Daemon is the daemon's name.
	Daemon string
	// Agent is the agent's name that was asked for ("claude").
	Agent string
	// Title is the agent as a person reads it ("Claude Code"); the name when the daemon does not offer it.
	Title string
	// Reason is why the daemon says the agent is not ready; empty when it does not offer the agent or gave none.
	Reason string
	// Offered is whether the daemon reported the agent at all.
	Offered bool
}

// Error says which agent is missing or not ready on which daemon, and why when the daemon said.
func (e *AgentNotReadyError) Error() string {
	switch {
	case !e.Offered:
		return e.Daemon + " does not offer " + e.Agent
	case e.Reason != "":
		return e.Title + " is not ready on " + e.Daemon + ": " + e.Reason
	default:
		return e.Title + " is not ready on " + e.Daemon
	}
}

// Conn is one agent process on the user's machine, seen from the app's server: the bytes of its stdin and stdout, and
// the process behind them. The server speaks ACP over it as it would over a local pipe.
type Conn interface {
	// Read is the agent's stdout; io.EOF once the process is gone and everything it wrote was read. Write is its
	// stdin, whole lines.
	io.ReadWriter
	// Cwd is the directory the agent runs in: the one the daemon started it in, or the last Chdir moved it to.
	Cwd() string
	// Chdir moves the agent to dir, an absolute path to a directory the user allowed this app. The daemon ends the
	// agent and starts the same program in dir, with this Conn's Read and Write carrying on to the new process; it is
	// a new agent that has not been initialized, so the app speaks ACP to it from the start (initialize, then
	// session/new or session/load). Lines written before Chdir reached the old agent. The agent stays where it is
	// when the error is the daemon's refusal (the text starts "your daemon:"). A daemon that predates Chdir refuses
	// it as an unknown call.
	Chdir(ctx context.Context, dir string) error
	// Stderr is the last of what the agent wrote on its standard error.
	Stderr() string
	// Close ends the process and everything it started. It does not block.
	Close()
	// Done is closed once the process is gone and its last words are in Stderr.
	Done() <-chan struct{}
}

// Ref names one daemon of a user.
type Ref struct {
	// ID is what an app stores and passes to Open.
	ID string
	// Name is what the user sees.
	Name string
}

// TailBuffer keeps the last Max bytes written to it. It holds an agent's standard error.
type TailBuffer struct {
	// Max is how many bytes it keeps.
	Max int
	mu  sync.Mutex
	buf []byte
}

// Write appends p and drops the oldest bytes beyond Max. It never fails.
func (b *TailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.Max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

// String is what the buffer holds.
func (b *TailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// List is the user's daemons as an app's UI shows them, with the pending pairing code, if any. Its JSON is what the
// page of an app gets (sdk/web has the matching TypeScript types).
type List struct {
	// Daemons is every daemon the user has paired, online or not, the one paired first first.
	Daemons []Info `json:"daemons"`
	// Pairing is the pending pairing code; its Code is empty when there is none.
	Pairing Pairing `json:"pairing"`
}

// Info is one paired daemon.
type Info struct {
	// ID is the daemon's id, what Open takes.
	ID string `json:"id"`
	// Name is what the user calls the daemon.
	Name string `json:"name"`
	// Hostname is the machine's; while the daemon is offline, the one it had when it paired.
	Hostname string `json:"hostname"`
	// State is StateOnline or StateOffline.
	State string `json:"state"`
	// Version is the daemon's version; empty while it is offline.
	Version string `json:"version"`
	// OS is the machine's operating system ("darwin", "linux").
	OS string `json:"os"`
	// Agents are the agents the daemon reports, in its order; empty while it is offline.
	Agents []Agent `json:"agents"`
	// LastSeen is when the daemon last connected or disconnected, in Unix milliseconds.
	LastSeen int64 `json:"lastSeen"`
	// Sessions is how many agent sessions it runs for this app now.
	Sessions int `json:"sessions"`
	// Managed is whether a service restarts the daemon when it exits: only then does Restart work.
	Managed bool `json:"managed"`
	// Incompatible says why a daemon that connected is not online: it and this app speak protocols that do not meet
	// (a sentence for a person, naming the side to update). Empty for every other daemon, and then absent from the JSON.
	// Such a daemon is StateOffline.
	Incompatible string `json:"incompatible,omitempty"`
}

// Agent is an agent a daemon can start. Ready is false when its machine lacks what it needs; Reason says what.
type Agent struct {
	// Name is the agent's id ("claude"), what Open takes.
	Name string `json:"name"`
	// Title is the agent as a person reads it ("Claude Code").
	Title string `json:"title"`
	// Ready is whether the daemon can start it now.
	Ready bool `json:"ready"`
	// Reason is why it cannot, as a sentence; empty when Ready.
	Reason string `json:"reason"`
}

// AgentInfo is Agent under the name the other SDKs (sdk/goclient, sdk/web) give it. Since 0.7.0.
type AgentInfo = Agent

// Pairing is the one pending pairing code of a user: Code is empty when there is none, ExpiresAt is Unix milliseconds.
type Pairing struct {
	// Code is the code to show the user, XXXXX-XXXXX; they give it to their daemon with `agentd pair`.
	Code string `json:"code"`
	// ExpiresAt is when the code stops working, in Unix milliseconds.
	ExpiresAt int64 `json:"expiresAt"`
}
