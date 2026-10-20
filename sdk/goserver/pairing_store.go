package goserver

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCodeNotFound is what PairingStore.Redeem answers for a code that was never issued, was used, was replaced,
// cancelled, or has expired. Since 0.8.0.
var ErrCodeNotFound = errors.New("pairing code not found, used or expired")

// ErrCodeTaken is what PairingStore.Issue answers when the code is live and belongs to another user. The Server draws
// another code. Since 0.8.0.
var ErrCodeTaken = errors.New("pairing code already issued to another user")

// ErrPairingUnavailable is what the Server answers when its PairingStore cannot be asked (the error wraps the store's
// own): StartPairing, CancelPairing and Pair return it, and POST /agentd/pair answers 503 with a Retry-After. A pairing
// is never accepted without the store having checked the code, so an app that sees this error has not paired anyone.
// Since 0.8.0.
var ErrPairingUnavailable = errors.New("pairing is unavailable right now: the pairing store did not answer")

// PairingStore keeps the pairing codes that are waiting to be used and the count of wrong ones. The Server asks it for
// everything it knows about a code, so that several instances of an app can share one: a code that one instance issued
// is redeemed at another, and wrong codes count against the same limit wherever they arrive. Options.Pairing is where
// an app passes its own (Redis, SQL, ...); without one the codes live in this process's memory
// (NewMemoryPairingStore), which is right for one instance. The package pairingtest runs a store against this contract.
//
// Codes reach a store in their canonical form: ten characters, upper case, without the dash that the user sees. Every
// time argument is the Server's clock (Options.Now): a store judges "expired" and "within the window" with the time it is
// given and never with its own clock, so that tests need not sleep. A store backed by a service with its own expiry
// (a Redis TTL) may also let an entry go early, but not later than the time says. Time is compared to the millisecond
// at least.
//
// Methods are safe for concurrent use, by many goroutines and, for a shared store, by many processes. A method that
// returns an error other than the ones named here did not do its job: the Server then refuses the pairing (it fails
// closed) and tells the user to try again.
//
// Since 0.8.0.
type PairingStore interface {
	// Issue makes code the one pending code of userID until expires, replacing the code userID had, in one step: no
	// observer sees a user with two live codes or with none in between. A code that is live for another user is not
	// replaced: Issue answers ErrCodeTaken and changes nothing. A code that has expired by now is free to be issued
	// again. A code is live while now is before expires.
	Issue(ctx context.Context, userID, code string, now, expires time.Time) error

	// Cancel forgets userID's pending code. Without one it does nothing and answers nil.
	Cancel(ctx context.Context, userID string) error

	// Pending is userID's code and the time it expires, or an empty code (and a nil error) when userID has none, or
	// the one it had has expired at now.
	Pending(ctx context.Context, userID string, now time.Time) (code string, expires time.Time, err error)

	// Redeem uses up code and answers the user it was issued to. It is atomic: of any number of concurrent Redeems of
	// one live code, in this process or in others, exactly one succeeds and the rest answer ErrCodeNotFound. An unknown
	// code, a used one, an expired one (now is not before expires) and the empty string answer ErrCodeNotFound. After
	// an error other than that one the code may or may not have been used up.
	Redeem(ctx context.Context, code string, now time.Time) (userID string, err error)

	// Failures is how many wrong codes were recorded under key (the address they came from) within window before
	// now: a failure recorded at t counts while now is before t+window. It records nothing.
	Failures(ctx context.Context, key string, now time.Time, window time.Duration) (int, error)

	// Fail records one wrong code under key at now and answers how many there are now within window, this one
	// included, counted and recorded in one step: concurrent Fails answer 1, 2, 3, ... with no count lost or repeated.
	Fail(ctx context.Context, key string, now time.Time, window time.Duration) (int, error)
}

// NewMemoryPairingStore is the PairingStore that keeps everything in this process's memory. It is what a Server uses
// when Options.Pairing is nil. Since 0.8.0.
func NewMemoryPairingStore() PairingStore { return newMemoryPairingStore() }

type pendingCode struct {
	code    string
	expires time.Time
}

// memoryPairingStore: one code per user, found by user and by code.
type memoryPairingStore struct {
	mu     sync.Mutex
	byUser map[string]pendingCode
	byCode map[string]string
	// codesSwept and failuresSwept are when the entries that have aged out were last dropped: a user who started a
	// pairing and never finished it, and an address that failed once, do not stay in memory for ever.
	codesSwept    time.Time
	failures      map[string][]time.Time
	failuresSwept time.Time
}

func newMemoryPairingStore() *memoryPairingStore {
	return &memoryPairingStore{byUser: map[string]pendingCode{}, byCode: map[string]string{}, failures: map[string][]time.Time{}}
}

// codeSweepEvery is how often the codes that ran out are dropped.
const codeSweepEvery = time.Minute

func (s *memoryPairingStore) sweepCodes(now time.Time) {
	if now.Sub(s.codesSwept) < codeSweepEvery {
		return
	}
	s.codesSwept = now
	for user, p := range s.byUser {
		if !now.Before(p.expires) {
			delete(s.byUser, user)
			delete(s.byCode, p.code)
		}
	}
}

func (s *memoryPairingStore) Issue(_ context.Context, userID, code string, now, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepCodes(now)
	if owner, taken := s.byCode[code]; taken && owner != userID {
		if now.Before(s.byUser[owner].expires) {
			return ErrCodeTaken
		}
		delete(s.byUser, owner) // it ran out: the code is free
		delete(s.byCode, code)
	}
	if old, ok := s.byUser[userID]; ok {
		delete(s.byCode, old.code)
	}
	s.byUser[userID] = pendingCode{code: code, expires: expires}
	s.byCode[code] = userID
	return nil
}

func (s *memoryPairingStore) Cancel(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byUser[userID]; ok {
		delete(s.byCode, old.code)
		delete(s.byUser, userID)
	}
	return nil
}

func (s *memoryPairingStore) Pending(_ context.Context, userID string, now time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byUser[userID]; ok && now.Before(p.expires) {
		return p.code, p.expires, nil
	}
	return "", time.Time{}, nil
}

func (s *memoryPairingStore) Redeem(_ context.Context, code string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, ok := s.byCode[code]
	if !ok {
		return "", ErrCodeNotFound
	}
	live := now.Before(s.byUser[userID].expires)
	delete(s.byUser, userID) // used up, or ran out: gone either way
	delete(s.byCode, code)
	if !live {
		return "", ErrCodeNotFound
	}
	return userID, nil
}

// trimWindow drops the failures that are a window old or older; they are in the order they were recorded.
func trimWindow(failures []time.Time, now time.Time, window time.Duration) []time.Time {
	keep := 0
	for keep < len(failures) && now.Sub(failures[keep]) >= window {
		keep++
	}
	return failures[keep:]
}

// sweepFailures drops the clients whose failures have all aged out, at most once per window, so the map holds only the
// clients that failed within the last two windows however many addresses have come and gone.
func (s *memoryPairingStore) sweepFailures(now time.Time, window time.Duration) {
	if now.Sub(s.failuresSwept) < window {
		return
	}
	s.failuresSwept = now
	for key, failures := range s.failures {
		if len(trimWindow(failures, now, window)) == 0 {
			delete(s.failures, key)
		}
	}
}

func (s *memoryPairingStore) Failures(_ context.Context, key string, now time.Time, window time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepFailures(now, window)
	return len(trimWindow(s.failures[key], now, window)), nil
}

func (s *memoryPairingStore) Fail(_ context.Context, key string, now time.Time, window time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepFailures(now, window)
	recent := append(trimWindow(s.failures[key], now, window), now)
	s.failures[key] = recent
	return len(recent), nil
}

// failureClients is how many addresses the store holds failures for (tests of the sweep).
func (s *memoryPairingStore) failureClients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.failures)
}
