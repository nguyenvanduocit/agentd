package goclient

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// watchClient is a daemon that is whatever the test says, and counts the looks.
type watchClient struct {
	mu      sync.Mutex
	detects int
	next    Detected
}

func (c *watchClient) Detect(context.Context) Detected {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detects++
	return c.next
}

func (c *watchClient) set(d Detected) {
	c.mu.Lock()
	c.next = d
	c.mu.Unlock()
}

func (c *watchClient) looks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.detects
}

func (*watchClient) ConnectServer(context.Context, Daemon, string) ConnectResult {
	return ConnectResult{Granted: true}
}
func (*watchClient) ConnectBrowser(context.Context, Daemon) ConnectResult {
	return ConnectResult{Granted: true}
}
func (*watchClient) Disconnect(context.Context, Daemon) error { return nil }

// ticks is the clock of Watch, driven by the test: each send is one tick, and a send returns once Watch took it.
type ticks struct {
	ch      chan time.Time
	every   atomic.Int64
	stopped atomic.Bool
}

func newTicks() *ticks { return &ticks{ch: make(chan time.Time)} }

func (k *ticks) make(every time.Duration) (<-chan time.Time, func()) {
	k.every.Store(int64(every))
	return k.ch, func() { k.stopped.Store(true) }
}

func (k *ticks) tick(t *testing.T) {
	t.Helper()
	select {
	case k.ch <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch took no tick")
	}
}

// testClock is the time of the test, moved while Watch reads it.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type watched struct {
	local  *LocalDaemon
	client *watchClient
	ticks  *ticks
	clock  *testClock
	events *atomic.Int32
}

// settle returns once Watch has finished with every tick sent before: a send returns only when the loop is back at
// its select. It costs one more look, so a test that counts looks does not use it.
func (w watched) settle(t *testing.T) { w.ticks.tick(t) }

func watchedLocal(t *testing.T, found Detected) watched {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	client := &watchClient{next: found}
	local, err := NewLocalDaemon(LocalDeps{Origin: "https://app.example.com", Mode: ModeBrowser, Client: client, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	k := newTicks()
	local.ticker = k.make
	var events atomic.Int32
	local.Subscribe(func(LocalState) { events.Add(1) })
	return watched{local: local, client: client, ticks: k, clock: clock, events: &events}
}

var (
	here    = Detected{Found: true, Port: 47615, Version: "1.0.0"}
	allowed = Detected{Found: true, Port: 47615, Version: "1.0.0", Granted: Granted{Browser: true}}
)

func TestWatchLooksAtOnceAndAtEveryTickWhileThereIsNothingToConnect(t *testing.T) {
	w := watchedLocal(t, Detected{})
	stop := w.local.Watch(context.Background(), time.Second)
	w.ticks.tick(t)
	w.ticks.tick(t)
	stop()
	if got := w.client.looks(); got != 3 { // at once, and two ticks
		t.Fatalf("looks = %d, want 3", got)
	}
	if every := time.Duration(w.ticks.every.Load()); every != time.Second {
		t.Fatalf("ticks every %v", every)
	}
	if !w.ticks.stopped.Load() {
		t.Fatal("the clock was not stopped")
	}
}

func TestWatchTellsAChangeAndNothingWhenNothingChanged(t *testing.T) {
	w := watchedLocal(t, Detected{})
	stop := w.local.Watch(context.Background(), time.Second)
	w.ticks.tick(t)
	w.settle(t)
	if got := w.events.Load(); got != 1 { // the first look: detecting -> absent; the same answer again is no news
		t.Fatalf("events = %d after unchanged looks, want 1", got)
	}
	w.client.set(here) // the person installed agentd and came back
	w.ticks.tick(t)
	w.settle(t)
	stop()
	if got := w.local.State().Phase; got != PhaseAvailable {
		t.Fatalf("phase = %q", got)
	}
	if got := w.events.Load(); got != 2 {
		t.Fatalf("events = %d, want 2", got)
	}
}

func TestWatchKeepsLookingWhileConnectedSoAGoneDaemonShows(t *testing.T) {
	w := watchedLocal(t, allowed)
	stop := w.local.Watch(context.Background(), time.Second)
	w.ticks.tick(t) // the first look is over when Watch takes this tick
	w.settle(t)
	if got := w.local.State().Phase; got != PhaseConnected {
		t.Fatalf("phase = %q", got)
	}
	w.client.set(Detected{}) // the daemon ended, or the grant lapsed
	w.ticks.tick(t)
	w.settle(t)
	stop()
	if got := w.local.State().Phase; got != PhaseAbsent {
		t.Fatalf("phase = %q, want absent", got)
	}
}

func TestWatchDoesNotLookWhileADialogWaitsOrARefusalIsOnTheScreen(t *testing.T) {
	for name, set := range map[string]func(*LocalDaemon, *LocalState){
		"connecting": func(_ *LocalDaemon, s *LocalState) { s.Connecting, s.Phase = true, PhaseConnecting },
		"refused": func(l *LocalDaemon, s *LocalState) {
			l.refused = &refusal{reason: ReasonDenied, error: "no"} // the lock is held by update
			s.Reason, s.Error, s.Phase = ReasonDenied, "no", PhaseRefused
		},
	} {
		w := watchedLocal(t, here)
		w.local.update(func(cur LocalState) (LocalState, bool) { set(w.local, &cur); return cur, true })
		stop := w.local.Watch(context.Background(), time.Second)
		w.ticks.tick(t)
		w.ticks.tick(t)
		stop()
		if got := w.client.looks(); got != 0 {
			t.Errorf("%s: looked %d times", name, got)
		}
	}
}

func TestWatchLooksAgainOnceTheWaitOfARefusalIsOver(t *testing.T) {
	w := watchedLocal(t, here)
	retry := w.clock.Now().Add(30 * time.Second)
	w.local.update(func(cur LocalState) (LocalState, bool) {
		w.local.refused = &refusal{reason: ReasonCooldown, error: "wait", retryAt: retry}
		cur.Reason, cur.Error, cur.Phase, cur.RetryAt = ReasonCooldown, "wait", PhaseRefused, retry
		return cur, true
	})
	stop := w.local.Watch(context.Background(), time.Second)
	w.ticks.tick(t) // inside the wait
	if got := w.client.looks(); got != 0 {
		t.Fatalf("looked %d times inside the wait", got)
	}
	w.clock.set(retry.Add(time.Second))
	w.ticks.tick(t)
	w.settle(t)
	stop()
	if got := w.local.State(); got.Phase != PhaseAvailable || !got.CanConnect || got.Error != "" || !got.RetryAt.IsZero() {
		t.Fatalf("state = %+v", got)
	}
}

func TestWatchEndsWithItsContextAndItsStopFunctionIsSafeToCallTwice(t *testing.T) {
	w := watchedLocal(t, Detected{})
	ctx, cancel := context.WithCancel(context.Background())
	stop := w.local.Watch(ctx, time.Second)
	cancel()
	stop()
	stop()
	if !w.ticks.stopped.Load() {
		t.Fatal("the clock was not stopped")
	}
	looks := w.client.looks()
	select {
	case w.ticks.ch <- time.Time{}:
		t.Fatal("Watch still takes ticks")
	case <-time.After(50 * time.Millisecond):
	}
	if w.client.looks() != looks {
		t.Fatal("Watch looked after it ended")
	}
}

func TestWatchWithNoIntervalLooksEveryThreeSeconds(t *testing.T) {
	w := watchedLocal(t, Detected{})
	stop := w.local.Watch(context.Background(), 0)
	stop()
	if every := time.Duration(w.ticks.every.Load()); every != 3*time.Second {
		t.Fatalf("every = %v", every)
	}
}
