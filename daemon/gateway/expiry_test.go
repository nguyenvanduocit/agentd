package gateway

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// grantedOn is the day the page of these tests was allowed.
var grantedOn = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func clockAt(at time.Time) func() time.Time { return func() time.Time { return at } }

// The protocol a page reads is the page protocol, not the one an app's server speaks to the daemon: they change for
// different reasons and must be free to differ.
func TestStatusNamesThePageProtocol(t *testing.T) {
	g := startGateway(t, newWorld().deps(), nil)
	if got, _ := status(t, g.Port, page).body["protocol"].(float64); got != float64(PageProtocol) {
		t.Fatalf("protocol = %v, want %d", got, PageProtocol)
	}
	if PageProtocol != 2 {
		t.Fatalf("PageProtocol = %d: it is bumped only when the page protocol changes incompatibly (2: connect names the app), and the SDKs say which they speak", PageProtocol)
	}
}

func TestAPageGrantHoldsForThirtyDaysAndThenTheOriginIsNotGranted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		want  bool
	}{
		{"the day after", 24 * time.Hour, true},
		{"a minute before the end", registry.BrowserGrantTTL - time.Minute, true},
		{"at the end", registry.BrowserGrantTTL, false},
		{"long after", 90 * 24 * time.Hour, false},
	} {
		w := newWorld()
		w.reg = w.reg.AddBrowser(page, grantedOn, registry.Access{})
		g := startGatewayAt(t, w.deps(), clockAt(grantedOn.Add(tc.after)))
		st := status(t, g.Port, page)
		if _, browser := granted(t, st); browser != tc.want {
			t.Errorf("%s: granted.browser = %v, want %v", tc.name, browser, tc.want)
		}
		if _, listed := st.body["agents"]; listed != tc.want {
			t.Errorf("%s: agents listed = %v, want %v: a lapsed origin sees nothing of the machine", tc.name, listed, tc.want)
		}
	}
}

func TestAPageWhoseGrantLapsedIsAskedAgainAndGrantedAnew(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	w.chosen = []string{"/work/new"}
	w.reg = w.reg.AddBrowser(page, grantedOn, registry.Access{Folders: []string{"/work/old"}})
	now := grantedOn.Add(registry.BrowserGrantTTL + time.Hour)
	w.now = clockAt(now)
	g := startGatewayAt(t, w.deps(), clockAt(now))

	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
	if ran() != 1 {
		t.Fatalf("the dialog ran %d times, want once: a lapsed grant asks the user again", ran())
	}
	if !w.reg.HasBrowser(page, now.Add(time.Hour)) {
		t.Error("the new grant is not live")
	}
	if got := w.reg.BrowserFolders(page); !reflect.DeepEqual(got, []string{"/work/new"}) {
		t.Errorf("folders = %v, want the ones just chosen", got)
	}
	if len(w.reg.Browsers) != 1 {
		t.Errorf("grants = %v, want the old one replaced", w.reg.Browsers)
	}
}

func TestAPageWhoseGrantIsLiveIsNotAskedAgain(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	w.reg = w.reg.AddBrowser(page, grantedOn, registry.Access{})
	g := startGatewayAt(t, w.deps(), clockAt(grantedOn.Add(10*24*time.Hour)))
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
	if ran() != 0 {
		t.Fatalf("the dialog ran %d times for a grant of ten days ago", ran())
	}
}

func TestTheSessionSocketOfALapsedGrantIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		want  int // upgrades that reached the sessions
	}{
		{"live", 10 * 24 * time.Hour, 1},
		{"lapsed", registry.BrowserGrantTTL + time.Second, 0},
	} {
		w := newWorld()
		w.reg = w.reg.AddBrowser(page, grantedOn, registry.Access{})
		deps := w.deps()
		deps.Upgrade = func(origin string, rw http.ResponseWriter, _ *http.Request) {
			w.mu.Lock()
			w.upgraded = append(w.upgraded, origin)
			w.mu.Unlock()
			drop(rw)
		}
		g := startGatewayAt(t, deps, clockAt(grantedOn.Add(tc.after)))
		_ = dialSession(t, g.Port)
		w.mu.Lock()
		got := len(w.upgraded)
		w.mu.Unlock()
		if got != tc.want {
			t.Errorf("%s: %d upgrades reached the sessions, want %d", tc.name, got, tc.want)
		}
	}
}

// What the user chose in the window is how long the grant lasts, counted from now; a choice of nothing is the default.
func TestTheGrantEndsWhenTheUserChoseAndAfterTheDefaultWhenTheyChoseNone(t *testing.T) {
	grantedOn := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		lasts time.Duration
		want  string
	}{
		{"an hour", time.Hour, "2026-10-19T13:00:00Z"},
		{"a week", 7 * 24 * time.Hour, "2026-10-26T12:00:00Z"},
		{"no choice is the default", 0, "2026-11-18T12:00:00Z"},
		{"never", consent.NoEnd, registry.Never},
	} {
		for _, mode := range []string{"browser", "server"} {
			w := newWorld()
			w.lasts = tc.lasts
			w.now = func() time.Time { return grantedOn }
			g := startGatewayAt(t, w.deps(), clockAt(grantedOn))
			body := map[string]any{"mode": mode}
			if mode == "server" {
				body["code"] = code
			}
			wantReply(t, connectAs(t, g.Port, body, "https://app.example.com"), 200, map[string]any{"granted": true})
			w.mu.Lock()
			var got string
			if mode == "browser" {
				got = w.reg.Browsers[0].ExpiresAt
			} else {
				got = w.reg.Apps[0].ExpiresAt
			}
			w.mu.Unlock()
			if got != tc.want {
				t.Errorf("%s, %s: expiresAt = %q, want %q", tc.name, mode, got, tc.want)
			}
		}
	}
}
