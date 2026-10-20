package goclient_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

var (
	notFound  = goclient.Detected{}
	foundOne  = goclient.Detected{Found: true, Port: 47615, Version: "1.0.0"}
	connected = goclient.Detected{Found: true, Port: 47615, Version: "1.0.0", Granted: goclient.Granted{Server: true}, Agents: []goclient.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}}}
	browser   = goclient.Detected{Found: true, Port: 47615, Version: "1.0.0", Granted: goclient.Granted{Browser: true}}
)

// fakeClient is the calls to the daemon, recorded.
type fakeClient struct {
	mu       sync.Mutex
	calls    []string
	detected []goclient.Detected
	connect  goclient.ConnectResult
	// disconnectErr is what Disconnect answers; nil is a daemon that confirmed.
	disconnectErr error
}

func (c *fakeClient) record(call string) {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
}

func (c *fakeClient) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (c *fakeClient) count(call string) int {
	n := 0
	for _, got := range c.Calls() {
		if got == call {
			n++
		}
	}
	return n
}

func (c *fakeClient) Detect(context.Context) goclient.Detected {
	c.record("detect")
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.detected) > 1 {
		next := c.detected[0]
		c.detected = c.detected[1:]
		return next
	}
	if len(c.detected) == 1 {
		return c.detected[0]
	}
	return notFound
}

func (c *fakeClient) ConnectServer(_ context.Context, d goclient.Daemon, code string) goclient.ConnectResult {
	c.record(fmt.Sprintf("connectServer %d %s", d.Port, code))
	return c.connect
}

func (c *fakeClient) ConnectBrowser(_ context.Context, d goclient.Daemon) goclient.ConnectResult {
	c.record(fmt.Sprintf("connectBrowser %d", d.Port))
	return c.connect
}

func (c *fakeClient) Disconnect(_ context.Context, d goclient.Daemon) error {
	c.record(fmt.Sprintf("disconnect %d", d.Port))
	return c.disconnectErr
}

type setupOptions struct {
	detected []goclient.Detected
	connect  goclient.ConnectResult
	code     string
	noCode   bool
	mode     goclient.Mode
}

func setup(t *testing.T, o setupOptions) (*goclient.LocalDaemon, *fakeClient) {
	t.Helper()
	if o.detected == nil {
		o.detected = []goclient.Detected{foundOne}
	}
	if o.connect == (goclient.ConnectResult{}) {
		o.connect = goclient.ConnectResult{Granted: true}
	}
	client := &fakeClient{detected: o.detected, connect: o.connect}
	deps := goclient.LocalDeps{Origin: "https://app.example.com", Mode: o.mode, Client: client}
	if o.mode != goclient.ModeBrowser {
		code := o.code
		if code == "" && !o.noCode {
			code = "CODE"
		}
		deps.StartPairing = func(context.Context) (string, error) {
			client.record("startPairing")
			return code, nil
		}
		deps.CancelPairing = func() { client.record("cancelPairing") }
	}
	local, err := goclient.NewLocalDaemon(deps)
	if err != nil {
		t.Fatal(err)
	}
	return local, client
}

var bg = context.Background()

func TestStartsWithNothingFound(t *testing.T) {
	local, _ := setup(t, setupOptions{})
	if got := local.State(); !reflect.DeepEqual(got, goclient.LocalState{Phase: goclient.PhaseDetecting}) {
		t.Fatalf("state = %+v", got)
	}
}

func TestDetectKeepsWhatTheClientFound(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne}})
	local.Detect(bg)
	if got := local.State().Found; got.Port != 47615 || !got.Found {
		t.Fatalf("found = %+v", got)
	}
}

func TestCanConnectOnlyForADaemonThatIsHereAndNotYetConnectedToThisApp(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne}})
	if local.State().CanConnect {
		t.Fatal("canConnect before a look")
	}
	local.Detect(bg)
	if !local.State().CanConnect {
		t.Fatal("no canConnect for a daemon that is here")
	}
	done, _ := setup(t, setupOptions{detected: []goclient.Detected{connected}})
	done.Detect(bg)
	if done.State().CanConnect {
		t.Fatal("canConnect for a daemon already connected")
	}
}

func TestConnectAsksTheServerForACodeHandsItToTheDaemonAndLooksAgain(t *testing.T) {
	local, c := setup(t, setupOptions{detected: []goclient.Detected{foundOne, connected}})
	local.Detect(bg)
	if !local.Connect(bg) {
		t.Fatal("Connect = false")
	}
	if want := []string{"detect", "startPairing", "connectServer 47615 CODE", "detect"}; !slices.Equal(c.Calls(), want) {
		t.Fatalf("calls = %v", c.Calls())
	}
	if got := local.State(); !got.Found.Granted.Server || got.Error != "" || got.CanConnect {
		t.Fatalf("state = %+v", got)
	}
}

func TestARefusalOnTheMachineCancelsThePairingAndSaysWhyInPlainWords(t *testing.T) {
	local, c := setup(t, setupOptions{connect: goclient.ConnectResult{Reason: goclient.ReasonDenied}})
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	if c.count("cancelPairing") != 1 {
		t.Fatalf("calls = %v", c.Calls())
	}
	if got := local.State(); !strings.Contains(got.Error, "declined") || got.Connecting {
		t.Fatalf("state = %+v", got)
	}
}

func TestLookingAgainClearsTheLastRefusalItDescribedAnEarlierAttempt(t *testing.T) {
	local, _ := setup(t, setupOptions{connect: goclient.ConnectResult{Reason: goclient.ReasonDenied}})
	local.Detect(bg)
	local.Connect(bg)
	if local.State().Error == "" {
		t.Fatal("no error after a refusal")
	}
	local.Detect(bg)
	if got := local.State(); got.Error != "" || !got.Found.Found {
		t.Fatalf("state = %+v", got)
	}
}

func TestALookWhileAConnectionWaitsForTheDialogChangesNothing(t *testing.T) {
	// The dialog returns the focus that triggers a look.
	release := make(chan struct{})
	client := &blockingClient{fakeClient: fakeClient{detected: []goclient.Detected{foundOne}, connect: goclient.ConnectResult{Granted: true}}, release: release, asked: make(chan struct{})}
	local := mustLocal(t, client)
	local.Detect(bg)
	done := make(chan bool)
	go func() { done <- local.Connect(bg) }()
	<-client.asked
	if !local.State().Connecting {
		t.Fatal("not connecting while the dialog is open")
	}
	local.Detect(bg)
	close(release)
	if !<-done {
		t.Fatal("Connect = false")
	}
	if n := client.count("detect"); n != 2 { // the first look, and the one after the connection
		t.Fatalf("detects = %d, calls %v", n, client.Calls())
	}
}

// blockingClient holds ConnectServer until release is closed.
type blockingClient struct {
	fakeClient
	release chan struct{}
	asked   chan struct{}
}

func (c *blockingClient) ConnectServer(ctx context.Context, d goclient.Daemon, code string) goclient.ConnectResult {
	close(c.asked)
	<-c.release
	return c.fakeClient.ConnectServer(ctx, d, code)
}

func mustLocal(t *testing.T, client goclient.LocalClient) *goclient.LocalDaemon {
	t.Helper()
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
		Origin:        "https://app.example.com",
		Client:        client,
		StartPairing:  func(context.Context) (string, error) { return "CODE", nil },
		CancelPairing: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return local
}

// stepClient lets the test answer each look and the connection when it likes.
type stepClient struct {
	looks   chan chan goclient.Detected
	connect chan goclient.ConnectResult
}

func (c *stepClient) Detect(context.Context) goclient.Detected {
	answer := make(chan goclient.Detected, 1)
	c.looks <- answer
	return <-answer
}
func (c *stepClient) ConnectServer(context.Context, goclient.Daemon, string) goclient.ConnectResult {
	return <-c.connect
}
func (c *stepClient) ConnectBrowser(context.Context, goclient.Daemon) goclient.ConnectResult {
	panic("server mode")
}
func (c *stepClient) Disconnect(context.Context, goclient.Daemon) error { return nil }

func TestALookThatBeganBeforeTheConnectionAndEndsDuringItDoesNotEndTheConnectingState(t *testing.T) {
	client := &stepClient{looks: make(chan chan goclient.Detected, 4), connect: make(chan goclient.ConnectResult)}
	local := mustLocal(t, client)

	first := make(chan struct{})
	go func() { local.Detect(bg); close(first) }() // the focus event: a look is in flight, the daemon not yet known
	(<-client.looks) <- foundOne
	<-first

	second := make(chan struct{})
	go func() { local.Detect(bg); close(second) }() // another focus: in flight again while the user presses Connect
	secondAnswer := <-client.looks
	connecting := make(chan bool)
	go func() { connecting <- local.Connect(bg) }()
	waitUntil(t, "connecting", func() bool { return local.State().Connecting })
	secondAnswer <- foundOne // the older look answers while the dialog is still open
	<-second
	if !local.State().Connecting {
		t.Fatal("the older look ended the connecting state")
	}
	client.connect <- goclient.ConnectResult{Granted: true}
	(<-client.looks) <- connected // connect has now asked for its own look
	if !<-connecting {
		t.Fatal("Connect = false")
	}
	if got := local.State(); got.Connecting || !got.Found.Granted.Server {
		t.Fatalf("state = %+v", got)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAServerThatGivesNoCodeNeverReachesTheDaemon(t *testing.T) {
	local, c := setup(t, setupOptions{noCode: true})
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	for _, call := range c.Calls() {
		if strings.HasPrefix(call, "connectServer") {
			t.Fatalf("calls = %v", c.Calls())
		}
	}
	if local.State().Error == "" {
		t.Fatal("no error")
	}
}

func TestAPairingThatFailsPutsItsErrorInState(t *testing.T) {
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
		Origin:        "https://app.example.com",
		Client:        &fakeClient{detected: []goclient.Detected{foundOne}},
		StartPairing:  func(context.Context) (string, error) { return "", errors.New("the app is down") },
		CancelPairing: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	if got := local.State(); got.Error != "the app is down" || got.Connecting {
		t.Fatalf("state = %+v", got)
	}
}

func TestNothingHappensWithoutADaemonAndASecondPressWhileOneRunsIsIgnored(t *testing.T) {
	none, c := setup(t, setupOptions{detected: []goclient.Detected{notFound}})
	none.Detect(bg)
	if none.Connect(bg) {
		t.Fatal("Connect = true without a daemon")
	}
	if !slices.Equal(c.Calls(), []string{"detect"}) {
		t.Fatalf("calls = %v", c.Calls())
	}

	release := make(chan struct{})
	client := &blockingClient{fakeClient: fakeClient{detected: []goclient.Detected{foundOne}, connect: goclient.ConnectResult{Granted: true}}, release: release, asked: make(chan struct{})}
	local := mustLocal(t, client)
	local.Detect(bg)
	first := make(chan bool)
	go func() { first <- local.Connect(bg) }()
	<-client.asked
	if local.Connect(bg) {
		t.Fatal("a second Connect ran")
	}
	close(release)
	<-first
	if n := client.count("connectServer 47615 CODE"); n != 1 {
		t.Fatalf("connectServer = %d, calls %v", n, client.Calls())
	}
}

// ---- browser mode ----

func TestInBrowserModeCanConnectWhileThisAppHasNoBrowserGrantWhateverTheServerGrant(t *testing.T) {
	local, _ := setup(t, setupOptions{mode: goclient.ModeBrowser, detected: []goclient.Detected{connected}}) // connected has the server grant only
	local.Detect(bg)
	if !local.State().CanConnect {
		t.Fatal("no canConnect without a browser grant")
	}
	done, _ := setup(t, setupOptions{mode: goclient.ModeBrowser, detected: []goclient.Detected{browser}})
	done.Detect(bg)
	if done.State().CanConnect {
		t.Fatal("canConnect with a browser grant")
	}
}

func TestInBrowserModeConnectAsksForTheBrowserGrantWithoutAPairingAndLooksAgain(t *testing.T) {
	local, c := setup(t, setupOptions{mode: goclient.ModeBrowser, detected: []goclient.Detected{foundOne, browser}})
	local.Detect(bg)
	if !local.Connect(bg) {
		t.Fatal("Connect = false")
	}
	if want := []string{"detect", "connectBrowser 47615", "detect"}; !slices.Equal(c.Calls(), want) {
		t.Fatalf("calls = %v", c.Calls())
	}
	if got := local.State(); !got.Found.Granted.Browser || got.Connecting || got.Error != "" || got.CanConnect {
		t.Fatalf("state = %+v", got)
	}
}

func TestInBrowserModeARefusalSaysWhyInPlainWords(t *testing.T) {
	local, c := setup(t, setupOptions{mode: goclient.ModeBrowser, connect: goclient.ConnectResult{Reason: goclient.ReasonDenied}})
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	if got := local.State(); !strings.Contains(got.Error, "declined") || got.Connecting {
		t.Fatalf("state = %+v", got)
	}
	for _, call := range c.Calls() {
		if strings.HasPrefix(call, "connectServer") || call == "cancelPairing" {
			t.Fatalf("calls = %v", c.Calls())
		}
	}
}

// ---- disconnect ----

func TestDisconnectTakesBackWhatThisAppWasAllowedAndLooksAgain(t *testing.T) {
	for _, c := range []struct {
		mode    goclient.Mode
		granted goclient.Detected
	}{{goclient.ModeServer, connected}, {goclient.ModeBrowser, browser}} {
		local, client := setup(t, setupOptions{mode: c.mode, detected: []goclient.Detected{c.granted, foundOne}})
		local.Detect(bg)
		if local.State().CanConnect {
			t.Fatalf("%s: canConnect while granted", c.mode)
		}
		local.Disconnect(bg)
		if want := []string{"detect", "disconnect 47615", "detect"}; !slices.Equal(client.Calls(), want) {
			t.Fatalf("%s: calls = %v", c.mode, client.Calls())
		}
		if !local.State().CanConnect {
			t.Fatalf("%s: no canConnect after the grant was taken back", c.mode)
		}
	}
}

func TestDisconnectWithoutADaemonOnlyLooks(t *testing.T) {
	local, c := setup(t, setupOptions{detected: []goclient.Detected{notFound}})
	local.Disconnect(bg)
	if !slices.Equal(c.Calls(), []string{"detect"}) {
		t.Fatalf("calls = %v", c.Calls())
	}
}

// ---- the deps ----

func TestDepsThatCannotWorkAreAnError(t *testing.T) {
	pairing := func(context.Context) (string, error) { return "CODE", nil }
	for name, deps := range map[string]goclient.LocalDeps{
		"no origin":                  {StartPairing: pairing, CancelPairing: func() {}},
		"server mode, no pairing":    {Origin: "https://a.example.com", Mode: goclient.ModeServer},
		"server mode, no cancel":     {Origin: "https://a.example.com", StartPairing: pairing},
		"a mode that does not exist": {Origin: "https://a.example.com", Mode: "both"},
		"browser mode, no origin":    {Mode: goclient.ModeBrowser},
	} {
		if _, err := goclient.NewLocalDaemon(deps); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: "https://a.example.com", Mode: goclient.ModeBrowser}); err != nil {
		t.Errorf("browser mode needs no pairing: %v", err)
	}
}

// ---- subscribe ----

func TestSubscribeTellsEveryChangeWithTheStateAsItIsAndStopsAfterTheUnsubscribe(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne, connected}})
	var mu sync.Mutex
	var seen []goclient.LocalState
	off := local.Subscribe(func(s goclient.LocalState) {
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
	})
	local.Detect(bg)
	mu.Lock()
	last := seen[len(seen)-1]
	count := len(seen)
	mu.Unlock()
	if !last.Found.Found || last.Found.Granted.Server {
		t.Fatalf("last = %+v", last)
	}
	off()
	local.Connect(bg)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != count {
		t.Fatalf("%d states after the unsubscribe, %d before", len(seen), count)
	}
}

func TestListenersAreToldEachStateInOrder(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne, connected}})
	var seen []string
	local.Subscribe(func(s goclient.LocalState) {
		seen = append(seen, fmt.Sprintf("connecting=%t found=%t", s.Connecting, s.Found.Found))
	})
	local.Detect(bg)
	local.Connect(bg)
	want := []string{
		"connecting=false found=true", // the look
		"connecting=true found=true",  // the dialog is open
		"connecting=false found=true", // the answer and the look after it, in one step: no state offers Connect again
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("seen = %v", seen)
	}
}

func TestAListenerThatThrowsDoesNotStopTheOthersAndOneThatCallsBackIsNotWaitedOn(t *testing.T) {
	local, _ := setup(t, setupOptions{})
	var seen []int
	local.Subscribe(func(goclient.LocalState) { panic("a bad listener") })
	local.Subscribe(func(goclient.LocalState) {
		seen = append(seen, 1)
		_ = local.State() // reading is allowed in a listener
	})
	local.Detect(bg)
	if len(seen) == 0 {
		t.Fatal("the second listener was not told")
	}
}

func TestEachStateIsAValueSoAFrameworkCanTellAChange(t *testing.T) {
	local, _ := setup(t, setupOptions{})
	before := local.State()
	local.Detect(bg)
	if local.State().Found.Found == before.Found.Found {
		t.Fatal("the state did not change")
	}
	if before.Found.Found {
		t.Fatal("an earlier state changed under its reader")
	}
}

func TestLocalDaemonIsSafeForConcurrentUse(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{foundOne}})
	local.Subscribe(func(goclient.LocalState) {})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local.Detect(bg)
			local.Connect(bg)
			_ = local.State()
			local.Disconnect(bg)
		}()
	}
	wg.Wait()
}
