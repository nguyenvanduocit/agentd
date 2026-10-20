package goserver_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
	"github.com/nguyenvanduocit/agentd/sdk/goserver/pairingtest"
)

const proxyHeader = "X-Test-Client-Ip"

// twoInstances are two Servers of one app sharing a pairing store: what an app does when it runs more than one
// process. Each has its own Tokens, as a test cannot share them, so the daemons of a pairing land in the instance that
// served it; everything these tests ask about the codes is what the shared store carries.
func twoInstances(t *testing.T, store goserver.PairingStore) (a, b env) {
	t.Helper()
	opts := goserver.Options{Pairing: store, ClientIPHeader: proxyHeader}
	return newEnvWith(t, opts), newEnvWith(t, opts)
}

func TestACodeIssuedOnOneInstanceIsRedeemedOnAnotherExactlyOnce(t *testing.T) {
	a, b := twoInstances(t, pairingtest.NewJSONStore())
	code := a.startCode(t, alice)
	if shown := b.list(t, alice).Pairing.Code; shown != code {
		t.Fatalf("the other instance shows the code %q, want %q", shown, code)
	}

	token, status := b.postPair(t, code)
	if status != http.StatusOK || token == "" {
		t.Fatalf("redeeming on the other instance = %d %q, want 200 and a token", status, token)
	}
	recs, err := b.tokens.List(alice)
	if err != nil || len(recs) != 1 {
		t.Fatalf("the daemon was not kept by the instance that paired it: %v %v", recs, err)
	}

	for name, instance := range map[string]env{"the instance that issued it": a, "the instance that redeemed it": b} {
		if _, status := instance.postPair(t, code); status != http.StatusForbidden {
			t.Errorf("a second redeem on %s = %d, want 403: the code is used up for both", name, status)
		}
	}
	if shown := a.list(t, alice).Pairing.Code; shown != "" {
		t.Errorf("the issuing instance still shows %q after the other one used it", shown)
	}
}

func TestAnInstanceThatStartsAPairingReplacesTheCodeOfTheOtherOne(t *testing.T) {
	a, b := twoInstances(t, goserver.NewMemoryPairingStore())
	first := a.startCode(t, alice)
	second := b.startCode(t, alice)
	if first == second {
		t.Fatal("two pairings gave the same code")
	}
	if _, status := a.postPair(t, first); status != http.StatusForbidden {
		t.Errorf("the replaced code = %d, want 403: a user has one code, whichever instance issued it", status)
	}
	if _, status := a.postPair(t, second); status != http.StatusOK {
		t.Errorf("the new code = %d, want 200", status)
	}
}

func TestWrongCodesAreCountedAcrossInstances(t *testing.T) {
	a, b := twoInstances(t, goserver.NewMemoryPairingStore())
	const client = "203.0.113.7"
	for i := range 10 {
		instance := a
		if i%2 == 1 {
			instance = b
		}
		if _, status := instance.postPairFrom(t, "NOPE0-NOPE0", "laptop", client); status != http.StatusForbidden {
			t.Fatalf("wrong code %d = %d, want 403", i, status)
		}
	}
	// Five failures on each instance: together they are the limit. The right code is refused too, on both, because the
	// limit is on the address, and a different address is not held back.
	code := a.startCode(t, alice)
	for name, instance := range map[string]env{"a": a, "b": b} {
		if _, status := instance.postPairFrom(t, code, "laptop", client); status != http.StatusTooManyRequests {
			t.Errorf("instance %s, limited address = %d, want 429", name, status)
		}
	}
	if _, status := b.postPairFrom(t, code, "laptop", "198.51.100.9"); status != http.StatusOK {
		t.Errorf("another address = %d, want 200", status)
	}
}

func TestACodeExpiresByTheClockOfTheServerThatAsks(t *testing.T) {
	a, b := twoInstances(t, goserver.NewMemoryPairingStore())
	code := a.startCode(t, alice)
	b.clock.add(11 * time.Minute)
	if shown := b.list(t, alice).Pairing.Code; shown != "" {
		t.Errorf("an expired code is shown: %q", shown)
	}
	if _, status := b.postPair(t, code); status != http.StatusForbidden {
		t.Errorf("an expired code = %d, want 403", status)
	}
}

// brokenStore wraps a store and fails the methods a test names, as a database that is down fails them.
type brokenStore struct {
	goserver.PairingStore
	mu   sync.Mutex
	fail map[string]bool
	seen map[string]int
}

var errStoreDown = errors.New("redis: connection refused")

func newBrokenStore() *brokenStore {
	return &brokenStore{PairingStore: goserver.NewMemoryPairingStore(), fail: map[string]bool{}, seen: map[string]int{}}
}

func (s *brokenStore) set(method string, down bool) {
	s.mu.Lock()
	s.fail[method] = down
	s.mu.Unlock()
}

func (s *brokenStore) down(method string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[method]++
	return s.fail[method]
}

func (s *brokenStore) Issue(ctx context.Context, user, code string, now, expires time.Time) error {
	if s.down("Issue") {
		return errStoreDown
	}
	return s.PairingStore.Issue(ctx, user, code, now, expires)
}

func (s *brokenStore) Cancel(ctx context.Context, user string) error {
	if s.down("Cancel") {
		return errStoreDown
	}
	return s.PairingStore.Cancel(ctx, user)
}

func (s *brokenStore) Pending(ctx context.Context, user string, now time.Time) (string, time.Time, error) {
	if s.down("Pending") {
		return "", time.Time{}, errStoreDown
	}
	return s.PairingStore.Pending(ctx, user, now)
}

func (s *brokenStore) Redeem(ctx context.Context, code string, now time.Time) (string, error) {
	if s.down("Redeem") {
		return "", errStoreDown
	}
	return s.PairingStore.Redeem(ctx, code, now)
}

func (s *brokenStore) Failures(ctx context.Context, key string, now time.Time, window time.Duration) (int, error) {
	if s.down("Failures") {
		return 0, errStoreDown
	}
	return s.PairingStore.Failures(ctx, key, now, window)
}

func (s *brokenStore) Fail(ctx context.Context, key string, now time.Time, window time.Duration) (int, error) {
	if s.down("Fail") {
		return 0, errStoreDown
	}
	return s.PairingStore.Fail(ctx, key, now, window)
}

func postStatusBody(t *testing.T, e env, code string) (status int, retryAfter, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/agentd/pair",
		strings.NewReader(`{"code":"`+code+`","hostname":"laptop","os":"darwin","arch":"arm64"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Retry-After"), strings.TrimSpace(string(raw))
}

// A store that cannot be asked must not let a pairing through: the daemon gets a token only when the code was checked.
func TestAStoreThatCannotRedeemRefusesThePairingAndLeavesTheCodeUsable(t *testing.T) {
	store := newBrokenStore()
	e := newEnvWith(t, goserver.Options{Pairing: store})
	code := e.startCode(t, alice)

	store.set("Redeem", true)
	status, retryAfter, body := postStatusBody(t, e, code)
	if status != http.StatusServiceUnavailable || retryAfter == "" || !strings.Contains(body, "try again") {
		t.Fatalf("a store that is down = %d, Retry-After %q, %q; want 503, a Retry-After, and words that say to try again", status, retryAfter, body)
	}
	if recs, _ := e.tokens.List(alice); len(recs) != 0 {
		t.Fatalf("a daemon was kept although the code could not be checked: %v", recs)
	}

	store.set("Redeem", false)
	if _, status := e.postPair(t, code); status != http.StatusOK {
		t.Fatalf("the same code after the store came back = %d, want 200: a refused attempt does not use the code", status)
	}
}

func TestAStoreThatCannotCountFailuresRefusesEvenTheRightCode(t *testing.T) {
	store := newBrokenStore()
	e := newEnvWith(t, goserver.Options{Pairing: store})
	code := e.startCode(t, alice)
	store.set("Failures", true)
	if status, _, _ := postStatusBody(t, e, code); status != http.StatusServiceUnavailable {
		t.Fatalf("the rate limit cannot be read = %d, want 503: it is checked before the code", status)
	}
	if recs, _ := e.tokens.List(alice); len(recs) != 0 {
		t.Fatalf("a daemon was kept without the rate limit being checked: %v", recs)
	}
}

func TestAWrongCodeIsStillWrongWhenTheFailureCannotBeRecorded(t *testing.T) {
	store := newBrokenStore()
	e := newEnvWith(t, goserver.Options{Pairing: store})
	store.set("Fail", true)
	if status, _, _ := postStatusBody(t, e, "NOPE0-NOPE0"); status != http.StatusForbidden {
		t.Fatalf("a wrong code with a store that cannot record it = %d, want 403", status)
	}
}

func TestStartingAndCancellingAPairingNameAStoreThatIsDown(t *testing.T) {
	store := newBrokenStore()
	e := newEnvWith(t, goserver.Options{Pairing: store})

	store.set("Issue", true)
	if _, err := e.server.StartPairing(alice); !errors.Is(err, goserver.ErrPairingUnavailable) {
		t.Errorf("StartPairing with a store that is down = %v, want ErrPairingUnavailable", err)
	}
	store.set("Issue", false)
	e.startCode(t, alice)

	store.set("Cancel", true)
	if _, err := e.server.CancelPairing(alice); !errors.Is(err, goserver.ErrPairingUnavailable) {
		t.Errorf("CancelPairing with a store that is down = %v, want ErrPairingUnavailable", err)
	}
}

// The list is how a user sees their daemons: a store that cannot say whether a code is pending takes away the code, not
// the daemons (and not the updates every tab gets).
func TestTheListSurvivesAStoreThatCannotSayWhichCodeIsPending(t *testing.T) {
	store := newBrokenStore()
	e := newEnvWith(t, goserver.Options{Pairing: store})
	e.pair(t, alice)
	e.startCode(t, alice)

	store.set("Pending", true)
	list, err := e.server.List(alice)
	if err != nil {
		t.Fatalf("List = %v, want the list without the code: the daemons are in the Tokens store, which is up", err)
	}
	if len(list.Daemons) != 1 || list.Pairing.Code != "" {
		t.Fatalf("List = %+v, want one daemon and no code", list)
	}
}

// Tabs are connected to one instance; nothing tells them what another instance did until that instance is asked to look.
func TestRefreshTellsTheTabsOfThisInstanceWhatAnotherInstanceChanged(t *testing.T) {
	a, b := twoInstances(t, goserver.NewMemoryPairingStore())
	ch, stop := a.server.Watch(alice)
	defer stop()
	if first := <-ch; first.Pairing.Code != "" {
		t.Fatalf("the first list already has a code: %q", first.Pairing.Code)
	}

	code := b.startCode(t, alice)
	select {
	case list := <-ch:
		t.Fatalf("the tab of the other instance heard of the code without being told: %+v", list)
	case <-time.After(200 * time.Millisecond):
	}

	a.server.Refresh(alice)
	select {
	case list := <-ch:
		if list.Pairing.Code != code {
			t.Fatalf("after Refresh the tab sees %q, want %q", list.Pairing.Code, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Refresh did not reach the tab")
	}
}

// Options.Pairing left nil is the store in this process's memory: an app with one instance changes nothing.
func TestWithoutAPairingStoreCodesLiveInMemory(t *testing.T) {
	e := newEnvWith(t, goserver.Options{Pairing: nil})
	if code := e.startCode(t, alice); code == "" {
		t.Fatal("the default store gave no code")
	}
}
