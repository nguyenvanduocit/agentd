package pairingtest_test

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
	"github.com/nguyenvanduocit/agentd/sdk/goserver/pairingtest"
)

// The suite is the contract: the store that ships with goserver passes it, and so does a store that shares nothing
// with it in structure (its state is JSON text that every call decodes and encodes again).
func TestTheMemoryStorePassesTheSuite(t *testing.T) {
	pairingtest.Run(t, func() goserver.PairingStore { return goserver.NewMemoryPairingStore() })
}

func TestAStoreThatCopiesThroughJSONPassesTheSuite(t *testing.T) {
	pairingtest.Run(t, func() goserver.PairingStore { return pairingtest.NewJSONStore() })
}

// Two handles over one backend are two instances of an app sharing a store.
func TestTwoHandlesOverOneMemoryStorePassTheSharedSuite(t *testing.T) {
	pairingtest.RunShared(t, func() (a, b goserver.PairingStore) {
		s := goserver.NewMemoryPairingStore()
		return s, s
	})
}

func TestTwoHandlesOverOneJSONStorePassTheSharedSuite(t *testing.T) {
	pairingtest.RunShared(t, func() (a, b goserver.PairingStore) { return pairingtest.NewJSONStorePair() })
}

// A suite that cannot fail proves nothing. These stores break one promise each; the suite must name the broken one.

// keepsTheCode answers a redeem without using the code up.
type keepsTheCode struct{ goserver.PairingStore }

func (s keepsTheCode) Redeem(ctx context.Context, code string, now time.Time) (string, error) {
	user, err := s.PairingStore.Redeem(ctx, code, now)
	if err == nil {
		// put it back, as a store that only reads would leave it
		_ = s.PairingStore.Issue(ctx, user, code, now, now.Add(time.Hour))
	}
	return user, err
}

// nonAtomic is correct in everything but one thing: its redeem looks at the code, waits, and only then takes it, so
// concurrent redeemers all see the code and all succeed.
type nonAtomic struct {
	failures goserver.PairingStore

	mu     sync.Mutex
	byUser map[string]entry
	byCode map[string]string
}

type entry struct {
	code    string
	expires time.Time
}

func newNonAtomic() *nonAtomic {
	return &nonAtomic{failures: goserver.NewMemoryPairingStore(), byUser: map[string]entry{}, byCode: map[string]string{}}
}

func (s *nonAtomic) Issue(_ context.Context, user, code string, now, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner, taken := s.byCode[code]; taken && owner != user && now.Before(s.byUser[owner].expires) {
		return goserver.ErrCodeTaken
	}
	if old, ok := s.byUser[user]; ok {
		delete(s.byCode, old.code)
	}
	s.byUser[user] = entry{code, expires}
	s.byCode[code] = user
	return nil
}

func (s *nonAtomic) Cancel(_ context.Context, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byUser[user]; ok {
		delete(s.byCode, old.code)
		delete(s.byUser, user)
	}
	return nil
}

func (s *nonAtomic) Pending(_ context.Context, user string, now time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.byUser[user]; ok && now.Before(e.expires) {
		return e.code, e.expires, nil
	}
	return "", time.Time{}, nil
}

func (s *nonAtomic) Redeem(_ context.Context, code string, now time.Time) (string, error) {
	s.mu.Lock()
	user, ok := s.byCode[code]
	live := ok && now.Before(s.byUser[user].expires)
	s.mu.Unlock() // the look is over
	if !live {
		return "", goserver.ErrCodeNotFound
	}
	runtime.Gosched()
	time.Sleep(2 * time.Millisecond) // every concurrent redeemer has looked by now
	s.mu.Lock()
	delete(s.byCode, code) // the take
	delete(s.byUser, user)
	s.mu.Unlock()
	return user, nil
}

func (s *nonAtomic) Failures(ctx context.Context, key string, now time.Time, window time.Duration) (int, error) {
	return s.failures.Failures(ctx, key, now, window)
}

func (s *nonAtomic) Fail(ctx context.Context, key string, now time.Time, window time.Duration) (int, error) {
	return s.failures.Fail(ctx, key, now, window)
}

// forgetsTheWindow counts every failure for ever.
type forgetsTheWindow struct{ goserver.PairingStore }

func (s forgetsTheWindow) Failures(ctx context.Context, key string, now time.Time, _ time.Duration) (int, error) {
	return s.PairingStore.Failures(ctx, key, now, 1000*time.Hour)
}

func TestTheSuiteNamesAStoreThatDoesNotUseACodeUp(t *testing.T) {
	failed := pairingtest.Check(func() goserver.PairingStore { return keepsTheCode{goserver.NewMemoryPairingStore()} })
	if !namesOne(failed, "redeem uses the code up") {
		t.Fatalf("the suite did not name the broken promise; it reported %v", names(failed))
	}
}

func TestTheSuiteNamesAStoreWhoseRedeemIsNotAtomic(t *testing.T) {
	failed := pairingtest.Check(func() goserver.PairingStore { return newNonAtomic() })
	if !namesOne(failed, "one code is redeemed by exactly one of many concurrent redeemers") {
		t.Fatalf("the suite did not name the broken promise; it reported %v", names(failed))
	}
}

func TestTheSuiteNamesAStoreThatNeverForgetsAFailure(t *testing.T) {
	failed := pairingtest.Check(func() goserver.PairingStore { return forgetsTheWindow{goserver.NewMemoryPairingStore()} })
	if !namesOne(failed, "failures age out of the window") {
		t.Fatalf("the suite did not name the broken promise; it reported %v", names(failed))
	}
}

func TestACorrectStoreHasNoFailures(t *testing.T) {
	if failed := pairingtest.Check(func() goserver.PairingStore { return goserver.NewMemoryPairingStore() }); len(failed) != 0 {
		t.Fatalf("the memory store failed %v", names(failed))
	}
}

func namesOne(failed []pairingtest.Failure, name string) bool {
	for _, f := range failed {
		if strings.Contains(f.Name, name) {
			return true
		}
	}
	return false
}

func names(failed []pairingtest.Failure) []string {
	out := make([]string, len(failed))
	for i, f := range failed {
		out[i] = f.Name + ": " + f.Err.Error()
	}
	return out
}
