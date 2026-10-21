package goclient_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// clock is a time the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)} }

func localWith(t *testing.T, mode goclient.Mode, client goclient.LocalClient, now func() time.Time) *goclient.LocalDaemon {
	t.Helper()
	deps := goclient.LocalDeps{Name: "Test app", Origin: "https://app.example.com", Mode: mode, Client: client, Now: now}
	if mode != goclient.ModeBrowser {
		deps.StartPairing = func(context.Context) (string, error) { return "CODE", nil }
		deps.CancelPairing = func() {}
	}
	local, err := goclient.NewLocalDaemon(deps)
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func TestThePhaseSaysWhereTheUserIs(t *testing.T) {
	granted := goclient.Detected{Found: true, Port: 47615, Version: "1", Protocol: 1, Granted: goclient.Granted{Server: true}}
	tooNew := goclient.Detected{Found: true, Port: 47615, Version: "9", Protocol: 9}
	for _, c := range []struct {
		name  string
		found goclient.Detected
		want  goclient.Phase
	}{
		{"no daemon answered", notFound, goclient.PhaseAbsent},
		{"a daemon that has not been allowed", foundOne, goclient.PhaseAvailable},
		{"a daemon that serves this app", granted, goclient.PhaseConnected},
		{"a daemon of another protocol", tooNew, goclient.PhaseRefused},
		{"a gateway that refused the origin", goclient.Detected{Refused: goclient.ReasonOrigin}, goclient.PhaseRefused},
	} {
		local, _ := setup(t, setupOptions{detected: []goclient.Detected{c.found}})
		if got := local.State().Phase; got != goclient.PhaseDetecting {
			t.Errorf("%s: before a look the phase is %q", c.name, got)
		}
		local.Detect(bg)
		if got := local.State().Phase; got != c.want {
			t.Errorf("%s: phase = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestARefusedGatewayIsSaidInPlainWordsWithItsReason(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{{Refused: goclient.ReasonOrigin}}})
	local.Detect(bg)
	got := local.State()
	if got.Phase != goclient.PhaseRefused || got.Reason != goclient.ReasonOrigin || got.Error != goclient.ReasonText(goclient.ReasonOrigin) || got.CanConnect || got.Found.Found {
		t.Fatalf("state = %+v", got)
	}
}

func TestARefusalKeepsItsReasonAndTheDaemonsOwnWords(t *testing.T) {
	local, _ := setup(t, setupOptions{connect: goclient.ConnectResult{Reason: goclient.ReasonDenied, Message: "the user said no"}})
	local.Detect(bg)
	local.Connect(bg)
	got := local.State()
	if got.Phase != goclient.PhaseRefused || got.Reason != goclient.ReasonDenied || got.Message != "the user said no" || got.Error != goclient.ReasonText(goclient.ReasonDenied) {
		t.Fatalf("state = %+v", got)
	}
	local.Detect(bg)
	if got := local.State(); got.Phase != goclient.PhaseAvailable || got.Reason != "" || got.Message != "" || got.Error != "" {
		t.Fatalf("a look clears the refusal: %+v", got)
	}
}

func TestThePhaseIsConnectingWhileTheDialogIsOpen(t *testing.T) {
	release := make(chan struct{})
	client := &blockingClient{fakeClient: fakeClient{detected: []goclient.Detected{foundOne, connected}, connect: goclient.ConnectResult{Granted: true}}, release: release, asked: make(chan struct{})}
	local := mustLocal(t, client)
	local.Detect(bg)
	done := make(chan bool)
	go func() { done <- local.Connect(bg) }()
	<-client.asked
	if got := local.State(); got.Phase != goclient.PhaseConnecting || !got.Connecting {
		t.Fatalf("state = %+v", got)
	}
	close(release)
	<-done
	if got := local.State().Phase; got != goclient.PhaseConnected {
		t.Fatalf("phase = %q", got)
	}
}

// After Allow the state never goes back to "not allowed yet" on the way to "connected": a screen that draws each state
// would flash the Connect button.
func TestAGrantGoesFromConnectingStraightToConnected(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne, connected}})
	local.Detect(bg)
	var mu sync.Mutex
	var seen []goclient.LocalState
	local.Subscribe(func(s goclient.LocalState) {
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
	})
	if !local.Connect(bg) {
		t.Fatal("Connect = false")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].Phase != goclient.PhaseConnecting || seen[1].Phase != goclient.PhaseConnected {
		phases := make([]goclient.Phase, len(seen))
		for i, s := range seen {
			phases[i] = s.Phase
		}
		t.Fatalf("phases = %v, want [connecting connected]", phases)
	}
	for _, s := range seen {
		if s.Phase == goclient.PhaseAvailable || (!s.Connecting && s.CanConnect) {
			t.Errorf("a state offers Connect after Allow: %+v", s)
		}
	}
}

func TestACooldownSaysWhenToTryAgainAndStopsAskingUntilThen(t *testing.T) {
	clk := newClock()
	client := &fakeClient{detected: []goclient.Detected{foundOne}, connect: goclient.ConnectResult{Reason: goclient.ReasonCooldown, RetryAfter: 30 * time.Second}}
	local := localWith(t, goclient.ModeBrowser, client, clk.Now)
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	got := local.State()
	if want := clk.Now().Add(30 * time.Second); !got.RetryAt.Equal(want) || got.CanConnect || got.Reason != goclient.ReasonCooldown {
		t.Fatalf("state = %+v, want RetryAt %v and no CanConnect", got, want)
	}
	asked := client.count("connectBrowser 47615")
	if local.Connect(bg) || client.count("connectBrowser 47615") != asked {
		t.Fatal("asked the daemon again before the cooldown ended")
	}
	clk.Advance(31 * time.Second)
	local.Detect(bg) // a look clears the refusal; the wait is over
	if got := local.State(); !got.CanConnect || !got.RetryAt.IsZero() || got.Phase != goclient.PhaseAvailable {
		t.Fatalf("after the wait: %+v", got)
	}
}

func TestAReasonWithNoWaitLeavesRetryAtZero(t *testing.T) {
	local, _ := setup(t, setupOptions{connect: goclient.ConnectResult{Reason: goclient.ReasonCooldown}})
	local.Detect(bg)
	local.Connect(bg)
	if got := local.State(); !got.RetryAt.IsZero() || !got.CanConnect {
		t.Fatalf("state = %+v", got)
	}
}
