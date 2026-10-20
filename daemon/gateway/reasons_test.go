package gateway

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

// A refusal is `reason`, the legacy value every page SDK since 0.4 knows, and for the cases a newer SDK can say
// better, `code`. A page that ignores `code` keeps working: it sees the reason it always saw.

func refusedFrom(t *testing.T, origins []string, c *clock) (*Gateway, *world) {
	t.Helper()
	w := newWorld()
	w.ask = func(consent.Question) consent.Answer { return consent.AnswerDeny }
	g := startGatewayAt(t, w.deps(), c.read)
	for _, o := range origins {
		wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, o), 403, map[string]any{"reason": "denied"})
	}
	return g, w
}

func TestACooldownSaysHowLongToWait(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	g, _ := refusedFrom(t, []string{page}, c)
	r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, page)
	wantReply(t, r, 429, map[string]any{"granted": false, "reason": "cooldown", "retryAfter": 30})
	if _, has := r.body["code"]; has {
		t.Errorf("a cooldown has no code: %v", r.body)
	}
	if got := r.header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if got := r.header.Get("Access-Control-Expose-Headers"); got != "Retry-After" {
		t.Errorf("a page cannot read Retry-After unless it is exposed: Access-Control-Expose-Headers = %q", got)
	}

	c.now = c.now.Add(10*time.Second + 500*time.Millisecond) // 19.5 s left: whole seconds, rounded up
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 429, map[string]any{"reason": "cooldown", "retryAfter": 20})
}

func TestALockoutIsALockedCodeOnACooldownReasonWithTheWholeWait(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	others := []string{"http://localhost:5171", "http://localhost:5172", "http://localhost:5173"}
	g, _ := refusedFrom(t, others, c)

	r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, "http://localhost:5174")
	wantReply(t, r, 429, map[string]any{"granted": false, "reason": "cooldown", "code": "locked", "retryAfter": 600})
	if got := r.header.Get("Retry-After"); got != "600" {
		t.Errorf("Retry-After = %q, want 600", got)
	}

	// An origin that is still in its own cooldown waits for the longer of the two.
	c.now = c.now.Add(590 * time.Second) // the 30 s cooldowns are over, the lockout has 10 s left
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, others[0]), 429, map[string]any{"reason": "cooldown", "code": "locked", "retryAfter": 10})

	c.now = c.now.Add(10 * time.Second)
	if r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, others[0]); r.status == http.StatusTooManyRequests {
		t.Errorf("still refused after the lockout ended: %v", r.body)
	}
}

func TestAnAllowWithoutAFolderIsNoFolderOnANoDialogReason(t *testing.T) {
	w := newWorld()
	w.chosen = []string{}
	g := startGateway(t, w.deps(), nil)
	r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, page)
	wantReply(t, r, 503, map[string]any{"granted": false, "reason": "no-dialog", "code": "no-folder"})
	if _, has := r.body["retryAfter"]; has {
		t.Errorf("nothing to wait for: %v", r.body)
	}
}

func TestABrowserGrantThatCannotBeSavedIsNotSavedOnAPairingReason(t *testing.T) {
	w := newWorld()
	deps := w.deps()
	deps.GrantBrowser = func(string, []string) error { return errors.New("disk full") }
	g := startGateway(t, deps, nil)
	r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, page)
	wantReply(t, r, 502, map[string]any{"granted": false, "reason": "pairing", "code": "not-saved", "message": "disk full"})
}

func TestAnAppThatRefusesTheCodeIsPairingWithNoCode(t *testing.T) {
	w := newWorld()
	deps := w.deps()
	deps.ConnectServer = func(server, _ string, _ []string) error { return errors.New(server + " refused the code") }
	g := startGateway(t, deps, nil)
	r := connectAs(t, g.Port, map[string]any{"mode": "server", "code": "WRONG"}, "https://app.example.com")
	wantReply(t, r, 502, map[string]any{"granted": false, "reason": "pairing"})
	if _, has := r.body["code"]; has {
		t.Errorf("the app's refusal is the plain pairing reason: %v", r.body)
	}
}

func TestOnlyAWaitCarriesRetryAfter(t *testing.T) {
	w := newWorld()
	w.ask = func(consent.Question) consent.Answer { return consent.AnswerTimeout }
	g := startGateway(t, w.deps(), nil)
	for i, want := range []struct {
		status int
		reason string
	}{{403, "timeout"}, {429, "cooldown"}} {
		r := connectAs(t, g.Port, map[string]any{"mode": "browser"}, page)
		_, has := r.body["retryAfter"]
		if r.status != want.status || r.body["reason"] != want.reason || has != (want.reason == "cooldown") {
			t.Fatalf("call %d: %d %v, want %d %s (retryAfter only on a cooldown)", i, r.status, r.body, want.status, want.reason)
		}
		if _, hasHeader := r.header["Retry-After"]; hasHeader != (want.reason == "cooldown") {
			t.Errorf("call %d: Retry-After header present = %v", i, hasHeader)
		}
	}
}
