package goclient

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
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
	// Origin is the app's origin, as the gateway reads it from the Origin header (see ValidateOrigin).
	Origin string
	// Mode is ModeServer (the zero value too) or ModeBrowser.
	Mode Mode
	// StartPairing (server mode) starts a pairing on the app's server and answers its code; "" when the server gave none.
	StartPairing func(ctx context.Context) (code string, err error)
	// CancelPairing (server mode) drops the pairing StartPairing started, after the daemon did not take it.
	CancelPairing func()
	// Client replaces the calls to the daemon (default: this package's own, with Options).
	Client LocalClient
	// Options are the options of the default Client's calls, and of OpenSession.
	Options []Option
	// Now is the clock that RetryAt is read against (default time.Now); a test moves it.
	Now func() time.Time
}

// Phase is where the user is on the way to a connected daemon: the one thing a screen needs to choose what to show.
// The phases mean what @agentd/client's do. Since 0.7.0.
type Phase string

const (
	// PhaseDetecting: nothing has been looked for yet, or the first look has not ended.
	PhaseDetecting Phase = "detecting"
	// PhaseAbsent: no daemon answered. agentd is not installed, not running, or something between blocked the call:
	// this package cannot tell which.
	PhaseAbsent Phase = "absent"
	// PhaseAvailable: a daemon is here and has not been allowed to serve this app (browser mode: this origin) yet.
	PhaseAvailable Phase = "available"
	// PhaseConnecting: a connection waits for the user's answer in the daemon's dialog.
	PhaseConnecting Phase = "connecting"
	// PhaseConnected: the daemon serves this app (browser mode: this origin).
	PhaseConnected Phase = "connected"
	// PhaseRefused: the last connection did not happen (Reason, Message, Error), a gateway turned the call down
	// (Found.Refused), or the daemon speaks a protocol this client does not (Incompatible).
	PhaseRefused Phase = "refused"
)

// LocalState is what a LocalDaemon knows.
type LocalState struct {
	Found Detected
	// Connecting is whether a connection waits for the user's answer in the daemon's dialog.
	Connecting bool
	// Error is why the last connection did not happen, in plain words; "" when it did or none was tried.
	Error string
	// CanConnect is whether a daemon is here and has not been allowed to serve this app (browser mode: this origin) yet,
	// and no wait is asked for (RetryAt).
	CanConnect bool
	// Incompatible is a sentence when the daemon states a protocol this client does not speak (Detected.Incompatible):
	// Error repeats it and CanConnect is false. "" otherwise.
	Incompatible string

	// Phase is where the user is: see Phase. Since 0.7.0.
	Phase Phase
	// Reason is why the last connection did not happen, or why a gateway turned the call down; "" when neither. Error
	// has its words, ReasonText(Reason). Since 0.7.0.
	Reason Reason
	// Message is the daemon's own words about the refusal, when it gave any. Since 0.7.0.
	Message string
	// RetryAt is when the daemon asks to ask again, after a cooldown or a lock: the zero time when it says none. While
	// it is in the future CanConnect is false and Connect does nothing. Since 0.7.0.
	RetryAt time.Time
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

// refusal is why the user's last attempt did not happen: what the state shows until the next look.
type refusal struct {
	error   string
	reason  Reason
	message string
	retryAt time.Time
}

// LocalDaemon is the daemon that may be running on this computer: whether it is there, and the connection the user can
// ask it for. The daemon shows its own dialog; this only asks. It is safe for concurrent use.
type LocalDaemon struct {
	deps   LocalDeps
	client LocalClient
	mode   Mode
	now    func() time.Time
	// ticker is the clock of Watch: ticks every `every`, and what stops them. A test replaces it.
	ticker func(every time.Duration) (<-chan time.Time, func())

	mu         sync.Mutex // guards the rest
	state      LocalState
	looked     bool     // a look has ended
	refused    *refusal // the last attempt's refusal, while the state shows it
	listeners  map[int]func(LocalState)
	nextID     int
	queue      []LocalState // states not yet told to the listeners
	delivering bool
}

func realTicker(every time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(every)
	return t.C, t.Stop
}

// NewLocalDaemon is a LocalDaemon that has found nothing yet. It fails on deps that cannot work: no origin, an origin
// the gateway would refuse (an *OriginError), or server mode without the pairing calls.
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
	if deps.Origin != "" {
		if err := ValidateOrigin(deps.Origin); err != nil {
			return nil, err
		}
	}
	client := deps.Client
	if client == nil {
		client = remote{origin: deps.Origin, opts: deps.Options}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	l := &LocalDaemon{deps: deps, client: client, mode: mode, now: now, ticker: realTicker, listeners: map[int]func(LocalState){}}
	l.state = l.commit(Detected{}, false, false, nil)
	return l, nil
}

// commit makes the state from what is known, and keeps what the state shows of it (the look that ended, the refusal).
// The caller holds l.mu, or owns l.
func (l *LocalDaemon) commit(found Detected, connecting, looked bool, r *refusal) LocalState {
	l.looked = looked
	l.refused = r
	granted := found.Granted.Server
	if l.mode == ModeBrowser {
		granted = found.Granted.Browser
	}
	incompatible := found.Incompatible()
	gatewayRefused := !found.Found && found.Refused != ""
	s := LocalState{Found: found, Connecting: connecting, Incompatible: incompatible}
	switch {
	case r != nil:
		s.Error, s.Reason, s.Message, s.RetryAt = r.error, r.reason, r.message, r.retryAt
	case incompatible != "":
		s.Error = incompatible
	case gatewayRefused:
		s.Error, s.Reason = ReasonText(found.Refused), found.Refused
	}
	waiting := !s.RetryAt.IsZero() && l.now().Before(s.RetryAt)
	s.CanConnect = found.Found && !granted && incompatible == "" && !waiting
	switch {
	case connecting:
		s.Phase = PhaseConnecting
	case r != nil || incompatible != "" || gatewayRefused:
		s.Phase = PhaseRefused
	case !looked:
		s.Phase = PhaseDetecting
	case !found.Found:
		s.Phase = PhaseAbsent
	case granted:
		s.Phase = PhaseConnected
	default:
		s.Phase = PhaseAvailable
	}
	return s
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
func (l *LocalDaemon) Detect(ctx context.Context) { l.look(ctx, false) }

// look is Detect; quiet makes it tell the listeners of a change only, and drop the answer of a look that the caller
// cancelled (Watch).
func (l *LocalDaemon) look(ctx context.Context, quiet bool) {
	if l.State().Connecting {
		return
	}
	found := l.client.Detect(ctx)
	if quiet && ctx.Err() != nil {
		return
	}
	// A connection that began while this look was in flight looks again itself when it ends.
	l.update(func(cur LocalState) (LocalState, bool) {
		if cur.Connecting {
			return cur, false
		}
		next := l.commit(found, false, true, nil)
		if quiet && reflect.DeepEqual(cur, next) {
			return cur, false
		}
		return next, true
	})
}

// Watch looks now, and then every `every` (default 3 s), for as long as ctx lives or the returned function is not
// called: so that a person who installs agentd and comes back, or whose grant lapsed or whose daemon ended, finds the
// state true without a call of the app's. It looks while the phase is detecting, absent, available, connected or
// refused by a gateway or a protocol; it does not look while a connection waits for the dialog, nor while the refusal
// of the user's last attempt is on the screen (a look clears it; once the RetryAt of a cooldown has passed it looks
// again). The listeners hear of a change only. The returned function ends the watch and waits for it to end; it is
// safe to call twice. Start it when the user shows interest (a settings screen), not at start-up. Since 0.7.0.
func (l *LocalDaemon) Watch(ctx context.Context, every time.Duration) (stop func()) {
	if every <= 0 {
		every = 3 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	ticks, stopTicks := l.ticker(every)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicks()
		l.watchOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				l.watchOnce(ctx)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(cancel)
		<-done
	}
}

func (l *LocalDaemon) watchOnce(ctx context.Context) {
	l.mu.Lock()
	// The refusal of the user's last attempt stays on the screen until they act, or until the wait it asks for is over.
	hold := l.refused != nil && (l.refused.retryAt.IsZero() || l.now().Before(l.refused.retryAt))
	l.mu.Unlock()
	if hold {
		return
	}
	l.look(ctx, true)
}

// askServer asks the daemon to connect to the app's server with a fresh pairing code: nil when it does, or why not.
func (l *LocalDaemon) askServer(ctx context.Context, d Daemon) *refusal {
	code, err := l.deps.StartPairing(ctx)
	if err != nil {
		return &refusal{error: err.Error()}
	}
	if code == "" {
		return &refusal{error: "The app could not start a connection. Try again."}
	}
	result := l.client.ConnectServer(ctx, d, code)
	if result.Granted {
		return nil
	}
	l.deps.CancelPairing()
	return l.refusalOf(result)
}

// askBrowser asks the daemon to let this app run agents: nil when it does, or why not.
func (l *LocalDaemon) askBrowser(ctx context.Context, d Daemon) *refusal {
	result := l.client.ConnectBrowser(ctx, d)
	if result.Granted {
		return nil
	}
	return l.refusalOf(result)
}

func (l *LocalDaemon) refusalOf(result ConnectResult) *refusal {
	r := &refusal{error: ReasonText(result.Reason), reason: result.Reason, message: result.Message}
	if result.RetryAfter > 0 {
		r.retryAt = l.now().Add(result.RetryAfter)
	}
	return r
}

// Connect asks the user to let the daemon on this computer serve this app, and waits for the answer. It is true when
// the user allowed it and the daemon now does; otherwise State().Error says why, in plain words, and Reason says why
// in code. Without a daemon, while another Connect waits, or while the daemon asks to wait (RetryAt), it does nothing
// and is false. If ctx ends while the dialog is open the reason is ReasonCancelled: the dialog may still be open, and
// a later Detect finds the grant if the user allows it there.
func (l *LocalDaemon) Connect(ctx context.Context) bool {
	var daemon Detected
	began := l.update(func(cur LocalState) (LocalState, bool) {
		if !cur.Found.Found || cur.Connecting || cur.Found.Incompatible() != "" || (l.refused != nil && !l.refused.retryAt.IsZero() && l.now().Before(l.refused.retryAt)) {
			return cur, false
		}
		daemon = cur.Found
		return l.commit(cur.Found, true, true, nil), true
	})
	if !began {
		return false
	}
	var problem *refusal
	if l.mode == ModeBrowser {
		problem = l.askBrowser(ctx, daemon.Daemon())
	} else {
		problem = l.askServer(ctx, daemon.Daemon())
	}
	if problem != nil {
		l.update(func(cur LocalState) (LocalState, bool) { return l.commit(cur.Found, false, true, problem), true })
		return false
	}
	// The grant is told with the look that shows it, in one step: a state in between would offer Connect again.
	found := l.client.Detect(ctx)
	l.update(func(LocalState) (LocalState, bool) { return l.commit(found, false, true, nil), true })
	return true
}

// Disconnect takes back what this app was allowed (its browser grant and the app at its origin), then looks again. A
// daemon that did not confirm leaves State().Error saying so: a disconnect that failed must not look as if it worked.
func (l *LocalDaemon) Disconnect(ctx context.Context) {
	var problem *refusal
	if daemon := l.State().Found; daemon.Found {
		if err := l.client.Disconnect(ctx, daemon.Daemon()); err != nil {
			problem = disconnectRefusal(err)
		}
	}
	l.Detect(ctx) // shows what is left
	if problem != nil {
		l.update(func(cur LocalState) (LocalState, bool) {
			return l.commit(cur.Found, cur.Connecting, true, problem), true
		})
	}
}

// disconnectRefusal is why a Disconnect was not confirmed, in plain words.
func disconnectRefusal(err error) *refusal {
	var refused *DisconnectError
	switch {
	case !errors.As(err, &refused):
		return &refusal{error: ReasonText(ReasonUnreachable), reason: ReasonUnreachable}
	case refused.Reason == "":
		return &refusal{error: ReasonText(ReasonMalformed), reason: ReasonMalformed, message: refused.Message}
	default:
		return &refusal{error: ReasonText(refused.Reason), reason: refused.Reason, message: refused.Message}
	}
}

// OpenSession opens agent on the daemon the last look found, for the origin of this LocalDaemon (browser mode only).
// It is the one call after Connect: no Daemon to build from a port, no origin to repeat. If the last look found no
// allowed daemon it looks once more, because the grant may have come since; then ErrNoDaemon says that none answers
// and ErrNotAllowed that the user has not allowed this app (the fix is Connect). A daemon's refusal of the agent is a
// *SessionError. opts add to LocalDeps.Options. Since 0.7.0.
func (l *LocalDaemon) OpenSession(ctx context.Context, agent string, opts ...Option) (*Session, error) {
	if l.mode != ModeBrowser {
		return nil, errors.New("goclient: OpenSession needs ModeBrowser: in server mode the app's server runs the agents")
	}
	if l.deps.Origin == "" {
		return nil, errors.New("goclient: OpenSession needs LocalDeps.Origin")
	}
	state := l.State()
	if !state.Found.Found || !state.Found.Granted.Browser {
		l.Detect(ctx) // the grant may have come since the last look
		state = l.State()
	}
	switch {
	case !state.Found.Found:
		return nil, ErrNoDaemon
	case !state.Found.Granted.Browser:
		return nil, ErrNotAllowed
	}
	all := append(append([]Option{}, l.deps.Options...), opts...)
	return OpenSession(ctx, state.Found.Daemon(), l.deps.Origin, agent, all...)
}
