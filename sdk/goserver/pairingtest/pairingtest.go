// Package pairingtest checks a goserver.PairingStore against its contract. An app that keeps pairing codes in Redis or
// SQL so that several instances of it can share them runs its store through Run (and RunShared, with two handles
// that stand for two instances) in its own tests:
//
//	func TestMyStore(t *testing.T) {
//		pairingtest.Run(t, func() goserver.PairingStore { return newMyStore(t) })
//	}
//
// Every check is one promise of the contract and has a name that says which; Check returns the ones a store broke
// without needing a *testing.T, so that the suite itself can be tested against stores that break them. Since 0.8.0.
package pairingtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// Failure is a promise of the contract that a store broke.
type Failure struct {
	// Name says which promise.
	Name string
	// Err says how it was broken.
	Err error
}

type check struct {
	name string
	run  func(newStore func() goserver.PairingStore) error
}

type sharedCheck struct {
	name string
	run  func(newPair func() (a, b goserver.PairingStore)) error
}

var (
	t0     = time.Unix(1_700_000_000, 0).UTC()
	ttl    = 10 * time.Minute
	window = time.Minute
	ctx    = context.Background()
)

const (
	codeA = "ABCDE23456"
	codeB = "FGHJK78923"
)

// Run runs the contract against stores made by newStore (a fresh, empty one per call), one subtest per promise.
func Run(t *testing.T, newStore func() goserver.PairingStore) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := guard(func() error { return c.run(newStore) }); err != nil {
				t.Error(err)
			}
		})
	}
}

// RunShared runs the promises that matter when several instances of an app share a store. newPair makes two handles
// over one empty backend, as two processes would hold: whatever one does, the other sees.
func RunShared(t *testing.T, newPair func() (a, b goserver.PairingStore)) {
	t.Helper()
	for _, c := range sharedChecks {
		t.Run(c.name, func(t *testing.T) {
			if err := guard(func() error { return c.run(newPair) }); err != nil {
				t.Error(err)
			}
		})
	}
}

// Check is Run without a *testing.T: the promises newStore's stores broke, none if they keep them all.
func Check(newStore func() goserver.PairingStore) []Failure {
	var failed []Failure
	for _, c := range checks {
		if err := guard(func() error { return c.run(newStore) }); err != nil {
			failed = append(failed, Failure{c.name, err})
		}
	}
	return failed
}

// CheckShared is RunShared without a *testing.T.
func CheckShared(newPair func() (a, b goserver.PairingStore)) []Failure {
	var failed []Failure
	for _, c := range sharedChecks {
		if err := guard(func() error { return c.run(newPair) }); err != nil {
			failed = append(failed, Failure{c.name, err})
		}
	}
	return failed
}

// guard turns a store that panics into a broken promise.
func guard(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the store panicked: %v", r)
		}
	}()
	return f()
}

func expect(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func sameTime(a, b time.Time) bool {
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

var checks = []check{
	{"issue then pending", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		code, expires, err := s.Pending(ctx, "u1", t0)
		if err != nil {
			return fmt.Errorf("Pending: %w", err)
		}
		return expect(code == codeA && sameTime(expires, t0.Add(ttl)), "Pending = %q, %v; want %q, %v", code, expires, codeA, t0.Add(ttl))
	}},

	{"pending is empty for a user without a code", func(newStore func() goserver.PairingStore) error {
		code, _, err := newStore().Pending(ctx, "nobody", t0)
		if err != nil {
			return fmt.Errorf("Pending: %w", err)
		}
		return expect(code == "", "Pending = %q for a user who has none, want the empty string and a nil error", code)
	}},

	{"redeem uses the code up: a second redeem finds nothing", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		user, err := s.Redeem(ctx, codeA, t0.Add(time.Second))
		if err != nil || user != "u1" {
			return fmt.Errorf("first Redeem = %q, %v; want u1 and a nil error", user, err)
		}
		if _, err := s.Redeem(ctx, codeA, t0.Add(2*time.Second)); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("second Redeem = %v, want ErrCodeNotFound", err)
		}
		code, _, err := s.Pending(ctx, "u1", t0.Add(3*time.Second))
		if err != nil {
			return fmt.Errorf("Pending: %w", err)
		}
		return expect(code == "", "Pending after the code was used = %q, want none", code)
	}},

	{"redeem of an unknown code or of the empty string finds nothing", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		for _, code := range []string{codeB, ""} {
			if _, err := s.Redeem(ctx, code, t0); !errors.Is(err, goserver.ErrCodeNotFound) {
				return fmt.Errorf("Redeem(%q) = %v, want ErrCodeNotFound", code, err)
			}
		}
		return nil
	}},

	{"a code is live while now is before expires and not at expires", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		expires := t0.Add(ttl)
		if err := s.Issue(ctx, "u1", codeA, t0, expires); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if code, _, _ := s.Pending(ctx, "u1", expires.Add(-time.Second)); code != codeA {
			return fmt.Errorf("Pending a second before expires = %q, want the code", code)
		}
		if code, _, err := s.Pending(ctx, "u1", expires); err != nil || code != "" {
			return fmt.Errorf("Pending at expires = %q, %v; want none: a code is live only while now is before expires", code, err)
		}
		if _, err := s.Redeem(ctx, codeA, expires); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("Redeem at expires = %v, want ErrCodeNotFound", err)
		}
		return nil
	}},

	{"an expired code is not redeemed even when it was never swept", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if _, err := s.Redeem(ctx, codeA, t0.Add(ttl+time.Hour)); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("Redeem an hour after expiry = %v, want ErrCodeNotFound", err)
		}
		return nil
	}},

	{"issue replaces the code of the user: the old one is dead, the new one lives", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := s.Issue(ctx, "u1", codeB, t0.Add(time.Second), t0.Add(ttl+time.Second)); err != nil {
			return fmt.Errorf("second Issue: %w", err)
		}
		if _, err := s.Redeem(ctx, codeA, t0.Add(2*time.Second)); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("Redeem of the replaced code = %v, want ErrCodeNotFound", err)
		}
		if code, _, _ := s.Pending(ctx, "u1", t0.Add(2*time.Second)); code != codeB {
			return fmt.Errorf("Pending = %q, want the new code", code)
		}
		user, err := s.Redeem(ctx, codeB, t0.Add(3*time.Second))
		return expect(err == nil && user == "u1", "Redeem of the new code = %q, %v; want u1", user, err)
	}},

	{"issue does not take a live code of another user, but may reuse an expired one", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := s.Issue(ctx, "u2", codeA, t0.Add(time.Minute), t0.Add(ttl)); !errors.Is(err, goserver.ErrCodeTaken) {
			return fmt.Errorf("Issue of a code that is live for another user = %v, want ErrCodeTaken", err)
		}
		if user, err := s.Redeem(ctx, codeA, t0.Add(2*time.Minute)); err != nil || user != "u1" {
			return fmt.Errorf("the refused Issue changed the owner: Redeem = %q, %v; want u1", user, err)
		}
		// an expired code is free
		if err := s.Issue(ctx, "u1", codeB, t0, t0.Add(time.Minute)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := s.Issue(ctx, "u2", codeB, t0.Add(2*time.Minute), t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue of a code whose owner's code ran out = %v, want nil", err)
		}
		user, err := s.Redeem(ctx, codeB, t0.Add(3*time.Minute))
		return expect(err == nil && user == "u2", "Redeem of the reused code = %q, %v; want u2", user, err)
	}},

	{"cancel forgets the code and is harmless without one", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Cancel(ctx, "u1"); err != nil {
			return fmt.Errorf("Cancel without a code = %w, want nil", err)
		}
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := s.Issue(ctx, "u2", codeB, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := s.Cancel(ctx, "u1"); err != nil {
			return fmt.Errorf("Cancel: %w", err)
		}
		if _, err := s.Redeem(ctx, codeA, t0); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("Redeem of a cancelled code = %v, want ErrCodeNotFound", err)
		}
		if code, _, _ := s.Pending(ctx, "u1", t0); code != "" {
			return fmt.Errorf("Pending after Cancel = %q, want none", code)
		}
		user, err := s.Redeem(ctx, codeB, t0)
		return expect(err == nil && user == "u2", "Cancel touched another user: Redeem = %q, %v; want u2", user, err)
	}},

	{"users have codes of their own", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		for user, code := range map[string]string{"u1": codeA, "u2": codeB} {
			if err := s.Issue(ctx, user, code, t0, t0.Add(ttl)); err != nil {
				return fmt.Errorf("Issue for %s: %w", user, err)
			}
		}
		if user, err := s.Redeem(ctx, codeA, t0); err != nil || user != "u1" {
			return fmt.Errorf("Redeem = %q, %v; want u1", user, err)
		}
		code, _, err := s.Pending(ctx, "u2", t0)
		return expect(err == nil && code == codeB, "the other user's code after u1's was used = %q, %v; want %q", code, err, codeB)
	}},

	{"fail counts per key and answers the count", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		for want := 1; want <= 3; want++ {
			got, err := s.Fail(ctx, "203.0.113.1", t0.Add(time.Duration(want)*time.Second), window)
			if err != nil || got != want {
				return fmt.Errorf("Fail #%d = %d, %v; want %d", want, got, err, want)
			}
		}
		if got, err := s.Failures(ctx, "203.0.113.1", t0.Add(4*time.Second), window); err != nil || got != 3 {
			return fmt.Errorf("Failures = %d, %v; want 3", got, err)
		}
		if got, err := s.Failures(ctx, "203.0.113.2", t0.Add(4*time.Second), window); err != nil || got != 0 {
			return fmt.Errorf("Failures of another key = %d, %v; want 0", got, err)
		}
		got, err := s.Fail(ctx, "203.0.113.2", t0.Add(5*time.Second), window)
		return expect(err == nil && got == 1, "Fail of another key = %d, %v; want 1", got, err)
	}},

	{"failures does not record anything", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		for range 5 {
			if got, err := s.Failures(ctx, "203.0.113.1", t0, window); err != nil || got != 0 {
				return fmt.Errorf("Failures = %d, %v; want 0 however often it is asked", got, err)
			}
		}
		got, err := s.Fail(ctx, "203.0.113.1", t0, window)
		return expect(err == nil && got == 1, "Fail after five reads = %d, %v; want 1", got, err)
	}},

	{"failures age out of the window", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		for _, at := range []time.Duration{0, 10 * time.Second, 20 * time.Second} {
			if _, err := s.Fail(ctx, "k", t0.Add(at), window); err != nil {
				return fmt.Errorf("Fail: %w", err)
			}
		}
		// a failure counts while now is before its time plus the window
		for _, c := range []struct {
			at   time.Duration
			want int
		}{{59 * time.Second, 3}, {60 * time.Second, 2}, {70 * time.Second, 1}, {80 * time.Second, 0}} {
			if got, err := s.Failures(ctx, "k", t0.Add(c.at), window); err != nil || got != c.want {
				return fmt.Errorf("Failures %v after the first = %d, %v; want %d", c.at, got, err, c.want)
			}
		}
		got, err := s.Fail(ctx, "k", t0.Add(90*time.Second), window)
		return expect(err == nil && got == 1, "Fail after the others aged out = %d, %v; want 1", got, err)
	}},

	{"one code is redeemed by exactly one of many concurrent redeemers", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		if err := s.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		return concurrentRedeem([]goserver.PairingStore{s}, codeA, "u1", 32)
	}},

	{"concurrent issues leave the user one live code", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		const n = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- s.Issue(ctx, "u1", fmt.Sprintf("CODE%06d", i), t0, t0.Add(ttl))
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return fmt.Errorf("a concurrent Issue for the same user failed: %w", err)
			}
		}
		pending, _, err := s.Pending(ctx, "u1", t0)
		if err != nil || pending == "" {
			return fmt.Errorf("Pending = %q, %v; want the code that won", pending, err)
		}
		live := 0
		for i := range n {
			code := fmt.Sprintf("CODE%06d", i)
			user, err := s.Redeem(ctx, code, t0)
			switch {
			case err == nil && user == "u1":
				live++
				if code != pending {
					return fmt.Errorf("the code %s was redeemable but Pending said %s", code, pending)
				}
			case errors.Is(err, goserver.ErrCodeNotFound):
			default:
				return fmt.Errorf("Redeem(%s) = %q, %v", code, user, err)
			}
		}
		return expect(live == 1, "%d of the issued codes were live, want exactly one: a user has at most one code", live)
	}},

	{"concurrent fails lose no count", func(newStore func() goserver.PairingStore) error {
		s := newStore()
		const n = 50
		var wg sync.WaitGroup
		start := make(chan struct{})
		counts := make(chan int, n)
		errs := make(chan error, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got, err := s.Fail(ctx, "k", t0, window)
				counts <- got
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(counts)
		close(errs)
		for err := range errs {
			if err != nil {
				return fmt.Errorf("Fail: %w", err)
			}
		}
		var got []int
		for c := range counts {
			got = append(got, c)
		}
		sort.Ints(got)
		for i, c := range got {
			if c != i+1 {
				return fmt.Errorf("the counts answered by %d concurrent Fails were %v, want 1..%d each once", n, got, n)
			}
		}
		total, err := s.Failures(ctx, "k", t0, window)
		return expect(err == nil && total == n, "Failures = %d, %v; want %d", total, err, n)
	}},
}

// concurrentRedeem has n goroutines, spread over handles, redeem one live code at the same instant: exactly one wins.
func concurrentRedeem(handles []goserver.PairingStore, code, user string, n int) error {
	var wg sync.WaitGroup
	start := make(chan struct{})
	type result struct {
		user string
		err  error
	}
	results := make(chan result, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			u, err := handles[i%len(handles)].Redeem(ctx, code, t0.Add(time.Second))
			results <- result{u, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	won := 0
	for r := range results {
		switch {
		case r.err == nil && r.user == user:
			won++
		case errors.Is(r.err, goserver.ErrCodeNotFound):
		default:
			return fmt.Errorf("a concurrent Redeem answered %q, %v", r.user, r.err)
		}
	}
	return expect(won == 1, "%d of %d concurrent Redeems of one code succeeded, want exactly 1", won, n)
}

var sharedChecks = []sharedCheck{
	{"a code issued through one handle is pending and redeemable through the other", func(newPair func() (a, b goserver.PairingStore)) error {
		a, b := newPair()
		if err := a.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if code, _, err := b.Pending(ctx, "u1", t0); err != nil || code != codeA {
			return fmt.Errorf("the other handle's Pending = %q, %v; want the code", code, err)
		}
		if user, err := b.Redeem(ctx, codeA, t0); err != nil || user != "u1" {
			return fmt.Errorf("the other handle's Redeem = %q, %v; want u1", user, err)
		}
		if _, err := a.Redeem(ctx, codeA, t0); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("Redeem through the first handle after the other used the code = %v, want ErrCodeNotFound", err)
		}
		return nil
	}},

	{"concurrent redeems through both handles succeed exactly once", func(newPair func() (a, b goserver.PairingStore)) error {
		a, b := newPair()
		if err := a.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		return concurrentRedeem([]goserver.PairingStore{a, b}, codeA, "u1", 32)
	}},

	{"issuing through one handle replaces the code issued through the other", func(newPair func() (a, b goserver.PairingStore)) error {
		a, b := newPair()
		if err := a.Issue(ctx, "u1", codeA, t0, t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue: %w", err)
		}
		if err := b.Issue(ctx, "u1", codeB, t0.Add(time.Second), t0.Add(ttl)); err != nil {
			return fmt.Errorf("Issue through the other handle: %w", err)
		}
		if _, err := a.Redeem(ctx, codeA, t0.Add(2*time.Second)); !errors.Is(err, goserver.ErrCodeNotFound) {
			return fmt.Errorf("the replaced code was redeemed through the handle that issued it: %v", err)
		}
		user, err := a.Redeem(ctx, codeB, t0.Add(2*time.Second))
		return expect(err == nil && user == "u1", "the new code through the first handle = %q, %v; want u1", user, err)
	}},

	{"failures recorded through either handle count together", func(newPair func() (a, b goserver.PairingStore)) error {
		a, b := newPair()
		for i := 1; i <= 6; i++ {
			h := a
			if i%2 == 0 {
				h = b
			}
			if got, err := h.Fail(ctx, "k", t0, window); err != nil || got != i {
				return fmt.Errorf("Fail #%d through alternating handles = %d, %v; want %d", i, got, err, i)
			}
		}
		for name, h := range map[string]goserver.PairingStore{"first": a, "second": b} {
			if got, err := h.Failures(ctx, "k", t0, window); err != nil || got != 6 {
				return fmt.Errorf("Failures through the %s handle = %d, %v; want 6", name, got, err)
			}
		}
		return nil
	}},
}
