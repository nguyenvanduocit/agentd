package update

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Log is where the package says what it did (status lines) and what went wrong (warnings); *proc.Logger is one.
type Log interface {
	Say(message string)
	Warn(message string)
}

type discard struct{}

func (discard) Say(string)  {}
func (discard) Warn(string) {}

func orDiscard(l Log) Log {
	if l == nil {
		return discard{}
	}
	return l
}

// seconds is a duration as the log says it: whole seconds, rounded.
func seconds(d time.Duration) int {
	return int(math.Round(d.Seconds()))
}

// WatcherOptions configures NewWatcher.
type WatcherOptions struct {
	// Base is the releases page the offer is read from (see ReleaseBase).
	Base string
	// Interval is how often to ask again after the first time.
	Interval time.Duration
	// Timeout bounds one asking; zero means 15 s.
	Timeout time.Duration
	// OnOffer takes each answer. It runs on the watcher's goroutine, one at a time.
	OnOffer func(Offer)
	// Client makes the requests; nil means http.DefaultClient.
	Client *http.Client
	// Log is told once per distinct reason an asking failed; nil says nothing.
	Log Log
}

const (
	defaultWatchTimeout = 15 * time.Second
	// maxOfferBytes bounds latest.json, which is a few hundred bytes.
	maxOfferBytes = 1 << 20
)

// Watcher asks the releases page for latest.json at start and every interval, and hands each answer to OnOffer. An
// answer that cannot be had (the host is down, refuses, redirects where it must not, sends something that is not an
// offer, or does not answer) changes nothing: it is asked again at the next interval, and said once per distinct
// reason in the log.
type Watcher struct {
	o           WatcherOptions
	log         Log
	ctx         context.Context
	cancel      context.CancelFunc
	stopped     atomic.Bool
	wg          sync.WaitGroup
	lastProblem string
}

// NewWatcher makes a watcher that does nothing until Start.
func NewWatcher(o WatcherOptions) *Watcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Watcher{o: o, log: orDiscard(o.Log), ctx: ctx, cancel: cancel}
}

// Start asks at once and then every interval, on a goroutine of its own.
func (w *Watcher) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.check()
		t := time.NewTicker(w.o.Interval)
		defer t.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-t.C:
				w.check()
			}
		}
	}()
}

// Stop ends the asking and an asking already on its way; no offer is handed over after it returns, unless OnOffer is
// the caller (then the one running finishes).
func (w *Watcher) Stop() {
	w.stopped.Store(true)
	w.cancel()
}

func (w *Watcher) check() {
	offer, err := w.fetch()
	if err != nil {
		if w.stopped.Load() {
			return
		}
		why := err.Error()
		if why != w.lastProblem {
			w.log.Warn(fmt.Sprintf("cannot read the release offer from %s: %s; asking again in %d s", w.o.Base, why, seconds(w.o.Interval)))
		}
		w.lastProblem = why
		return
	}
	w.lastProblem = ""
	if !w.stopped.Load() {
		w.o.OnOffer(offer)
	}
}

func (w *Watcher) fetch() (Offer, error) {
	timeout := w.o.Timeout
	if timeout == 0 {
		timeout = defaultWatchTimeout
	}
	ctx, cancel := context.WithTimeout(w.ctx, timeout)
	defer cancel()
	res, err := FetchFollowing(ctx, w.o.Client, LatestURL(w.o.Base))
	if err != nil {
		return Offer{}, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_ = res.Body.Close()
		return Offer{}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	text, err := ReadCapped(res, maxOfferBytes)
	if err != nil {
		return Offer{}, err
	}
	offer, ok := ParseLatest(string(text))
	if !ok {
		return Offer{}, fmt.Errorf("latest.json is not an offer")
	}
	return offer, nil
}
