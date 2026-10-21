package goclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// goldenReasons is contract/testdata/page-reasons.json: the refusal reasons of the gateway and the sentences, the one
// source the daemon, this package and the web SDK are tested against.
type goldenReasons struct {
	Reasons       []goldenReason `json:"reasons"`
	ClientReasons []goldenReason `json:"clientReasons"`
	Fallback      string         `json:"fallback"`
}

type goldenReason struct {
	Reason string `json:"reason"`
	Legacy string `json:"legacy"`
	Text   string `json:"text"`
}

func loadGolden(t *testing.T) goldenReasons {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/page-reasons.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenReasons
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// knownReasons is every Reason constant by its wire name: a reason added to the golden file needs its constant here.
var knownReasons = map[string]goclient.Reason{
	"denied": goclient.ReasonDenied, "timeout": goclient.ReasonTimeout, "cooldown": goclient.ReasonCooldown,
	"locked": goclient.ReasonLocked, "busy": goclient.ReasonBusy, "no-dialog": goclient.ReasonNoDialog,
	"no-folder": goclient.ReasonNoFolder, "pairing": goclient.ReasonPairing, "not-saved": goclient.ReasonNotSaved,
	"origin": goclient.ReasonOrigin, "host": goclient.ReasonHost, "malformed": goclient.ReasonMalformed,
	"not-found": goclient.ReasonNotFound, "error": goclient.ReasonError, "peer": goclient.ReasonPeer,
	"unreachable": goclient.ReasonUnreachable, "cancelled": goclient.ReasonCancelled,
}

func TestReasonTextsAreTheGoldenFile(t *testing.T) {
	g := loadGolden(t)
	entries := append(append([]goldenReason{}, g.Reasons...), g.ClientReasons...)
	if len(entries) != len(knownReasons) {
		t.Errorf("the golden file has %d reasons, the package has %d constants", len(entries), len(knownReasons))
	}
	for _, e := range entries {
		constant, ok := knownReasons[e.Reason]
		if !ok {
			t.Errorf("%q is in the golden file and has no Reason constant", e.Reason)
			continue
		}
		if string(constant) != e.Reason {
			t.Errorf("constant for %q is %q", e.Reason, constant)
		}
		if got := goclient.ReasonText(constant); got != e.Text {
			t.Errorf("ReasonText(%q) = %q, want %q", e.Reason, got, e.Text)
		}
	}
}

func TestEveryLegacyReasonIsAKnownReason(t *testing.T) {
	g := loadGolden(t)
	known := map[string]bool{}
	for _, e := range g.Reasons {
		known[e.Reason] = true
	}
	for _, e := range g.Reasons {
		if e.Legacy != "" && !known[e.Legacy] {
			t.Errorf("%q has the legacy reason %q, which is not in the file", e.Reason, e.Legacy)
		}
	}
}

func TestAReasonThisPackageDoesNotKnowGetsTheFallbackSentenceThatNamesIt(t *testing.T) {
	g := loadGolden(t)
	want := strings.ReplaceAll(g.Fallback, "{reason}", "something-new")
	if got := goclient.ReasonText("something-new"); got != want {
		t.Fatalf("ReasonText = %q, want %q", got, want)
	}
}

func refusalWith(status int, body map[string]any, header http.Header) *http.Client {
	return clientWith(func(*http.Request) (*http.Response, error) {
		res := jsonReply(status, body)
		for k, v := range header {
			res.Header[k] = v
		}
		return res, nil
	})
}

func TestTheSpecificCodeOfARefusalIsItsReasonWhenThisPackageKnowsIt(t *testing.T) {
	for _, c := range []struct {
		name string
		body map[string]any
		want goclient.Reason
	}{
		{"a code this package knows", map[string]any{"granted": false, "reason": "cooldown", "code": "locked", "message": "m"}, goclient.ReasonLocked},
		{"no-folder over no-dialog", map[string]any{"granted": false, "reason": "no-dialog", "code": "no-folder"}, goclient.ReasonNoFolder},
		{"not-saved over pairing", map[string]any{"granted": false, "reason": "pairing", "code": "not-saved"}, goclient.ReasonNotSaved},
		{"a daemon older than codes", map[string]any{"granted": false, "reason": "cooldown"}, goclient.ReasonCooldown},
		{"a code from the future", map[string]any{"granted": false, "reason": "cooldown", "code": "never-heard-of-it"}, goclient.ReasonCooldown},
		{"a code and no reason", map[string]any{"granted": false, "code": "locked"}, goclient.ReasonLocked},
		{"neither", map[string]any{"granted": false}, goclient.ReasonMalformed},
	} {
		got := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(refusalWith(429, c.body, nil)))
		if got.Granted || got.Reason != c.want {
			t.Errorf("%s: %+v, want reason %q", c.name, got, c.want)
		}
	}
}

func TestHowLongToWaitComesFromTheBodyOrTheHeader(t *testing.T) {
	for _, c := range []struct {
		name   string
		body   map[string]any
		header http.Header
		want   time.Duration
	}{
		{"the body", map[string]any{"granted": false, "reason": "cooldown", "retryAfter": 17}, nil, 17 * time.Second},
		{"the header", map[string]any{"granted": false, "reason": "cooldown"}, http.Header{"Retry-After": {"29"}}, 29 * time.Second},
		{"the body wins", map[string]any{"granted": false, "reason": "cooldown", "retryAfter": 5}, http.Header{"Retry-After": {"29"}}, 5 * time.Second},
		{"neither", map[string]any{"granted": false, "reason": "cooldown"}, nil, 0},
		{"a value that is no number", map[string]any{"granted": false, "reason": "cooldown"}, http.Header{"Retry-After": {"soon"}}, 0},
		{"a negative value", map[string]any{"granted": false, "reason": "cooldown", "retryAfter": -3}, nil, 0},
	} {
		got := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(refusalWith(429, c.body, c.header)))
		if got.RetryAfter != c.want {
			t.Errorf("%s: RetryAfter = %v, want %v", c.name, got.RetryAfter, c.want)
		}
	}
}

func TestAContextThatEndsIsCancelledNotUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got := goclient.ConnectBrowser(ctx, found, app, "Test app", goclient.WithHTTPClient(clientWith(hang)))
	if got.Granted || got.Reason != goclient.ReasonCancelled {
		t.Fatalf("result = %+v, want reason cancelled", got)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if got := goclient.ConnectServer(cancelled, found, app, "Test app", "CODE", goclient.WithHTTPClient(clientWith(hang))); got.Reason != goclient.ReasonCancelled {
		t.Fatalf("a cancelled context: %+v", got)
	}
}
