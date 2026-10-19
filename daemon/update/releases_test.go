package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var releaseOffer = Offer{Version: "2.0.0", URL: "/download/v2.0.0/agentd-2.0.0.tgz", SHA256: testSHA, Signature: testSig}

var releaseOfferJSON = func() string {
	b, err := json.Marshal(releaseOffer)
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// waitFor polls cond until it holds, or fails the test after timeout.
func waitFor(t testing.TB, what string, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// logCapture is a Log that keeps what it is told.
type logCapture struct {
	mu    sync.Mutex
	said  []string
	warns []string
}

func (l *logCapture) Say(m string)  { l.mu.Lock(); l.said = append(l.said, m); l.mu.Unlock() }
func (l *logCapture) Warn(m string) { l.mu.Lock(); l.warns = append(l.warns, m); l.mu.Unlock() }
func (l *logCapture) warned() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

// offers collects what a watcher hands over.
type offers struct {
	mu   sync.Mutex
	list []Offer
}

func (o *offers) add(x Offer) { o.mu.Lock(); o.list = append(o.list, x); o.mu.Unlock() }
func (o *offers) len() int    { o.mu.Lock(); defer o.mu.Unlock(); return len(o.list) }
func (o *offers) first() Offer {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.list[0]
}

// release serves latest.json through handle(n), n being the number of the request.
func release(t *testing.T, handle func(n int, w http.ResponseWriter, r *http.Request)) (string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest/download/latest.json" {
			http.NotFound(w, r)
			return
		}
		handle(int(hits.Add(1)), w, r)
	}))
	t.Cleanup(s.Close)
	return s.URL, &hits
}

func watch(t *testing.T, base string, interval, timeout time.Duration, log Log) (*Watcher, *offers) {
	t.Helper()
	got := &offers{}
	w := NewWatcher(WatcherOptions{Base: base, Interval: interval, Timeout: timeout, OnOffer: got.add, Log: log})
	t.Cleanup(w.Stop)
	w.Start()
	return w, got
}

func serveOffer(_ int, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(releaseOfferJSON))
}

func TestWatcher(t *testing.T) {
	t.Run("asks at once, and hands the offer over", func(t *testing.T) {
		base, _ := release(t, serveOffer)
		_, got := watch(t, base, time.Minute, 2*time.Second, nil)
		waitFor(t, "the first offer", func() bool { return got.len() > 0 }, 5*time.Second)
		if got.first() != releaseOffer {
			t.Errorf("got %+v", got.first())
		}
	})

	t.Run("asks again every interval", func(t *testing.T) {
		base, hits := release(t, serveOffer)
		_, got := watch(t, base, 100*time.Millisecond, 2*time.Second, nil)
		waitFor(t, "three offers", func() bool { return got.len() >= 3 }, 5*time.Second)
		if hits.Load() < 3 {
			t.Errorf("%d requests", hits.Load())
		}
	})

	t.Run("a server that is down, a refusal, a redirect, a body that is not an offer and a hang each change nothing and are asked again", func(t *testing.T) {
		var last atomic.Int64
		base, _ := release(t, func(n int, w http.ResponseWriter, r *http.Request) {
			last.Store(int64(n))
			switch n {
			case 1:
				http.Error(w, "down", http.StatusServiceUnavailable)
			case 2:
				http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
			case 3:
				_, _ = w.Write([]byte("not json"))
			case 4:
				<-r.Context().Done()
			default:
				serveOffer(n, w, r)
			}
		})
		log := &logCapture{}
		_, got := watch(t, base, 80*time.Millisecond, 150*time.Millisecond, log)
		waitFor(t, "the offer after five answers", func() bool { return got.len() > 0 }, 10*time.Second)
		if last.Load() < 5 {
			t.Errorf("the offer came after %d requests", last.Load())
		}
		if got.first() != releaseOffer {
			t.Errorf("got %+v", got.first())
		}
		warns := log.warned()
		if len(warns) != 4 || !strings.Contains(warns[0], "HTTP 503") || !strings.Contains(warns[2], "latest.json is not an offer") {
			t.Errorf("warnings: %q", warns)
		}
	})

	t.Run("a problem is said once, however often it comes again", func(t *testing.T) {
		base, hits := release(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusServiceUnavailable)
		})
		log := &logCapture{}
		watch(t, base, 30*time.Millisecond, time.Second, log)
		waitFor(t, "four requests", func() bool { return hits.Load() >= 4 }, 5*time.Second)
		if warns := log.warned(); len(warns) != 1 || !strings.Contains(warns[0], "asking again in 0 s") {
			t.Errorf("warnings: %q", warns)
		}
	})

	t.Run("a redirect inside the server's own origin is followed (GitHub sends a release asset on to another host)", func(t *testing.T) {
		var behind atomic.Int64
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest/download/latest.json":
				http.Redirect(w, r, "/download/v2.0.0/latest.json", http.StatusFound)
			case "/download/v2.0.0/latest.json":
				behind.Add(1)
				serveOffer(0, w, r)
			default:
				http.NotFound(w, r)
			}
		}))
		defer s.Close()
		_, got := watch(t, s.URL, time.Minute, 2*time.Second, nil)
		waitFor(t, "the offer behind the redirect", func() bool { return got.len() > 0 }, 5*time.Second)
		if got.first() != releaseOffer || behind.Load() != 1 {
			t.Errorf("got %+v after %d requests behind the redirect", got.first(), behind.Load())
		}
	})

	t.Run("a redirect loop ends: it is not followed forever", func(t *testing.T) {
		var hits atomic.Int64
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Redirect(w, r, "/latest/download/latest.json", http.StatusFound)
		}))
		defer s.Close()
		_, got := watch(t, s.URL, time.Minute, 2*time.Second, nil)
		waitFor(t, "the hops to stop", func() bool { return hits.Load() >= 6 }, 5*time.Second)
		time.Sleep(300 * time.Millisecond)
		if hits.Load() >= 12 || got.len() != 0 {
			t.Errorf("%d requests, %d offers", hits.Load(), got.len())
		}
	})

	t.Run("stop ends the asking", func(t *testing.T) {
		base, hits := release(t, serveOffer)
		w, got := watch(t, base, 50*time.Millisecond, 2*time.Second, nil)
		waitFor(t, "an offer", func() bool { return got.len() > 0 }, 5*time.Second)
		w.Stop()
		time.Sleep(120 * time.Millisecond) // a request already on its way when Stop was called may still land
		seen := hits.Load()
		time.Sleep(300 * time.Millisecond)
		if hits.Load() != seen {
			t.Errorf("%d requests after stop", hits.Load()-seen)
		}
	})
}
