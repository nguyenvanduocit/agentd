package goclient

import (
	"context"
	"errors"
	"sync"
)

// LocalClient is the calls to the daemon that a LocalDaemon makes. NewLocalDaemon uses this package's own (Detect,
// ConnectServer, ConnectBrowser and Disconnect, for LocalDeps.Origin); a test or an app replaces them.
type LocalClient interface {
	Detect(ctx context.Context) Detected
	ConnectServer(ctx context.Context, d Daemon, code string) ConnectResult
	ConnectBrowser(ctx context.Context, d Daemon) ConnectResult
	Disconnect(ctx context.Context, d Daemon) error
}

// Mode is how an app uses the daemon.
type Mode string

const (
	// ModeServer: the daemon connects to the app's server, which runs the agents (the default).
	ModeServer Mode = "server"
	// ModeBrowser: the app itself runs agents on this computer (OpenSession); no server takes part.
	ModeBrowser Mode = "browser"
)

// LocalDeps is what NewLocalDaemon needs.
type LocalDeps struct {
	// Origin is the app's origin, as the gateway reads it from the Origin header.
	Origin string
	// Mode is ModeServer (the zero value too) or ModeBrowser.
	Mode Mode
	// StartPairing (server mode) starts a pairing on the app's server and answers its code; "" when the server gave none.
	StartPairing func(ctx context.Context) (code string, err error)
	// CancelPairing (server mode) drops the pairing StartPairing started, after the daemon did not take it.
	CancelPairing func()
	// Client replaces the calls to the daemon (default: this package's own, with Options).
	Client LocalClient
	// Options are the options of the default Client's calls.
	Options []Option
}

// LocalState is what a LocalDaemon knows.
type LocalState struct {
	Found Detected
	// Connecting is whether a connection waits for the user's answer in the daemon's dialog.
	Connecting bool
	// Error is why the last connection did not happen, in plain words; "" when it did or none was tried.
	Error string
	// CanConnect is whether a daemon is here and has not been allowed to serve this app (browser mode: this origin) yet.
	CanConnect bool
	// Incompatible is a sentence when the daemon states a protocol this client does not speak (Detected.Incompatible):
	// Error repeats it and CanConnect is false. "" otherwise.
	Incompatible string
}

// remote is the default LocalClient: the package's calls for one origin.
type remote struct {
	origin string
	opts   []Option
}

func (r remote) Detect(ctx context.Context) Detected { return Detect(ctx, r.origin, r.opts...) }
func (r remote) ConnectServer(ctx context.Context, d Daemon, code string) ConnectResult {
	return ConnectServer(ctx, d, r.origin, code, r.opts...)
}
func (r remote) ConnectBrowser(ctx context.Context, d Daemon) ConnectResult {
	return ConnectBrowser(ctx, d, r.origin, r.opts...)
}
func (r remote) Disconnect(ctx context.Context, d Daemon) error {
	return Disconnect(ctx, d, r.origin, r.opts...)
}

// LocalDaemon is the daemon that may be running on this computer: whether it is there, and the connection the user can
// ask it for. The daemon shows its own dialog; this only asks. It is safe for concurrent use.
type LocalDaemon struct {
	deps   LocalDeps
	client LocalClient
	mode   Mode

	mu         sync.Mutex // guards the rest
	state      LocalState
	listeners  map[int]func(LocalState)
	nextID     int
	queue      []LocalState // states not yet told to the listeners
	delivering bool
}

// NewLocalDaemon is a LocalDaemon that has found nothing yet. It fails on deps that cannot work: no origin, or server
// mode without the pairing calls.
func NewLocalDaemon(deps LocalDeps) (*LocalDaemon, error) {
	mode := deps.Mode
	if mode == "" {
		mode = ModeServer
	}
	switch {
	case mode != ModeServer && mode != ModeBrowser:
		return nil, errors.New("goclient: Mode is ModeServer or ModeBrowser")
	case deps.Origin == "" && deps.Client == nil:
		return nil, errors.New("goclient: LocalDeps.Origin is required")
	case mode == ModeServer && (deps.StartPairing == nil || deps.CancelPairing == nil):
		return nil, errors.New("goclient: server mode cannot connect without StartPairing and CancelPairing")
	}
	client := deps.Client
	if client == nil {
		client = remote{origin: deps.Origin, opts: deps.Options}
	}
	l := &LocalDaemon{deps: deps, client: client, mode: mode, listeners: map[int]func(LocalState){}}
	l.state = l.stateOf(Detected{}, false, "")
	return l, nil
}

func (l *LocalDaemon) stateOf(found Detected, connecting bool, err string) LocalState {
	granted := found.Granted.Server
	if l.mode == ModeBrowser {
		granted = found.Granted.Browser
	}
	incompatible := found.Incompatible()
	if err == "" {
		err = incompatible
	}
	return LocalState{Found: found, Connecting: connecting, Error: err, CanConnect: found.Found && !granted && incompatible == "", Incompatible: incompatible}
}

// State is the state now.
func (l *LocalDaemon) State() LocalState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// Subscribe calls listener with the new state after every change, in order, on the goroutine that made the change
// (a listener that calls back into the LocalDaemon is not waited on by that call). A listener that panics does not
// stop the others. It returns what stops the listener.
func (l *LocalDaemon) Subscribe(listener func(LocalState)) (unsubscribe func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := l.nextID
	l.nextID++
	l.listeners[id] = listener
	return func() {
		l.mu.Lock()
		delete(l.listeners, id)
		l.mu.Unlock()
	}
}

// update replaces the state with what change makes of it, and tells the listeners; false (and no change) when change
// says no. The check and the replacement are one step.
func (l *LocalDaemon) update(change func(cur LocalState) (LocalState, bool)) bool {
	l.mu.Lock()
	next, ok := change(l.state)
	if !ok {
		l.mu.Unlock()
		return false
	}
	l.state = next
	l.queue = append(l.queue, next)
	if l.delivering { // the goroutine that is telling listeners tells this state too
		l.mu.Unlock()
		return true
	}
	l.delivering = true
	for len(l.queue) > 0 {
		state := l.queue[0]
		l.queue = l.queue[1:]
		listeners := make([]func(LocalState), 0, len(l.listeners))
		for _, fn := range l.listeners {
			listeners = append(listeners, fn)
		}
		l.mu.Unlock()
		for _, fn := range listeners {
			tell(fn, state)
		}
		l.mu.Lock()
	}
	l.delivering = false
	l.mu.Unlock()
	return true
}

// tell calls a listener; one listener's failure is not another's.
func tell(fn func(LocalState), state LocalState) {
	defer func() { _ = recover() }()
	fn(state)
}

// Detect looks for a daemon (the settings screen opened, or the app got focus). The dialog of a waiting connection
// returns the focus that triggers this, so a look while connecting is skipped; the connection looks again itself when
// it ends. The last refusal described an earlier attempt: a look clears it.
func (l *LocalDaemon) Detect(ctx context.Context) {
	if l.State().Connecting {
		return
	}
	found := l.client.Detect(ctx)
	// A connection that began while this look was in flight looks again itself when it ends.
	l.update(func(cur LocalState) (LocalState, bool) {
		if cur.Connecting {
			return cur, false
		}
		return l.stateOf(found, false, ""), true
	})
}

// askServer asks the daemon to connect to the app's server with a fresh pairing code: "" when it does, or why not.
func (l *LocalDaemon) askServer(ctx context.Context, d Daemon) string {
	code, err := l.deps.StartPairing(ctx)
	if err != nil {
		return err.Error()
	}
	if code == "" {
		return "The app could not start a connection. Try again."
	}
	result := l.client.ConnectServer(ctx, d, code)
	if result.Granted {
		return ""
	}
	l.deps.CancelPairing()
	return ReasonText(result.Reason)
}

// askBrowser asks the daemon to let this app run agents: "" when it does, or why not.
func (l *LocalDaemon) askBrowser(ctx context.Context, d Daemon) string {
	result := l.client.ConnectBrowser(ctx, d)
	if result.Granted {
		return ""
	}
	return ReasonText(result.Reason)
}

// Connect asks the user to let the daemon on this computer serve this app, and waits for the answer. It is true when
// the user allowed it and the daemon now does; otherwise State().Error says why, in plain words. Without a daemon, or
// while another Connect waits, it does nothing and is false.
func (l *LocalDaemon) Connect(ctx context.Context) bool {
	var daemon Detected
	began := l.update(func(cur LocalState) (LocalState, bool) {
		if !cur.Found.Found || cur.Connecting || cur.Found.Incompatible() != "" {
			return cur, false
		}
		daemon = cur.Found
		return l.stateOf(cur.Found, true, ""), true
	})
	if !began {
		return false
	}
	var problem string
	if l.mode == ModeBrowser {
		problem = l.askBrowser(ctx, Daemon{Port: daemon.Port})
	} else {
		problem = l.askServer(ctx, Daemon{Port: daemon.Port})
	}
	l.update(func(cur LocalState) (LocalState, bool) { return l.stateOf(cur.Found, false, problem), true })
	if problem != "" {
		return false
	}
	l.Detect(ctx)
	return true
}

// Disconnect takes back what this app was allowed (its browser grant and the app at its origin), then looks again. A
// daemon that did not confirm leaves State().Error saying so: a disconnect that failed must not look as if it worked.
func (l *LocalDaemon) Disconnect(ctx context.Context) {
	var problem string
	if daemon := l.State().Found; daemon.Found {
		if err := l.client.Disconnect(ctx, Daemon{Port: daemon.Port}); err != nil {
			problem = disconnectProblem(err)
		}
	}
	l.Detect(ctx) // shows what is left
	if problem != "" {
		l.update(func(cur LocalState) (LocalState, bool) { return l.stateOf(cur.Found, cur.Connecting, problem), true })
	}
}

// disconnectProblem is why a Disconnect was not confirmed, in plain words.
func disconnectProblem(err error) string {
	var refusal *DisconnectError
	if !errors.As(err, &refusal) {
		return ReasonText(ReasonUnreachable)
	}
	if refusal.Reason == "" {
		return ReasonText(ReasonMalformed)
	}
	return ReasonText(refusal.Reason)
}
