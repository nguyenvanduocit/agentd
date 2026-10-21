package goclient_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

func TestValidateOriginAcceptsWhatABrowserSends(t *testing.T) {
	for _, origin := range []string{
		"https://app.example.com",
		"https://app.example.com:8443",
		"http://localhost:5173",
		"http://127.0.0.1:5199",
		"http://127.0.0.1",
		"http://[::1]:8080",
		"https://[2001:db8::1]:9000",
	} {
		if err := goclient.ValidateOrigin(origin); err != nil {
			t.Errorf("%s: %v", origin, err)
		}
	}
}

func TestValidateOriginNamesTheFix(t *testing.T) {
	for _, c := range []struct{ origin, want string }{
		{"http://127.0.0.1:5199/", `goclient: origin "http://127.0.0.1:5199/" has a path; use "http://127.0.0.1:5199"`},
		{"https://app.example.com/app", `goclient: origin "https://app.example.com/app" has a path; use "https://app.example.com"`},
		{"https://app.example.com?x=1", `has a query; use "https://app.example.com"`},
		{"https://app.example.com#top", `has a fragment; use "https://app.example.com"`},
		{"https://me@app.example.com", `has a user name; use "https://app.example.com"`},
		{"ftp://app.example.com", `must start with https:// (or http:// for localhost)`},
		{"app.example.com", `must start with https:// (or http:// for localhost)`},
		{"http://app.example.com", `plain http is accepted for localhost only; use "https://app.example.com"`},
		{"https://App.Example.com", `is not in the form a browser sends; use "https://app.example.com"`},
		{"https://app.example.com:443", `is not in the form a browser sends; use "https://app.example.com"`},
		{"", `the origin is empty`},
		{"https://", `has no host`},
		{"https://bücher.example", `is not ASCII`},
	} {
		err := goclient.ValidateOrigin(c.origin)
		var bad *goclient.OriginError
		if !errors.As(err, &bad) {
			t.Errorf("%q: err = %v, want an *OriginError", c.origin, err)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %q does not contain %q", c.origin, err.Error(), c.want)
		}
		if bad.Origin != c.origin {
			t.Errorf("%q: OriginError.Origin = %q", c.origin, bad.Origin)
		}
	}
}

// countingClient is an http client that records how many requests were made.
func countingClient(n *atomic.Int32) *http.Client {
	return clientWith(func(*http.Request) (*http.Response, error) {
		n.Add(1)
		return jsonReply(200, status), nil
	})
}

func TestABadOriginNeverReachesTheDaemon(t *testing.T) {
	const bad = "http://127.0.0.1:5199/"
	var requests atomic.Int32
	c := goclient.WithHTTPClient(countingClient(&requests))
	ctx := context.Background()

	if got := goclient.Detect(ctx, bad, c); got.Found || got.Refused != goclient.ReasonOrigin {
		t.Errorf("Detect = %+v, want not found and refused for the origin", got)
	}
	for name, got := range map[string]goclient.ConnectResult{
		"ConnectBrowser": goclient.ConnectBrowser(ctx, found, bad, "Test app", c),
		"ConnectServer":  goclient.ConnectServer(ctx, found, bad, "Test app", "CODE", c),
	} {
		if got.Granted || got.Reason != goclient.ReasonOrigin || !strings.Contains(got.Message, `use "http://127.0.0.1:5199"`) {
			t.Errorf("%s = %+v", name, got)
		}
	}
	var origin *goclient.OriginError
	if err := goclient.Disconnect(ctx, found, bad, c); !errors.As(err, &origin) {
		t.Errorf("Disconnect: %v", err)
	}
	if _, err := goclient.OpenSession(ctx, found, bad, "claude", c); !errors.As(err, &origin) {
		t.Errorf("OpenSession: %v", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests reached the daemon", n)
	}
}

func TestNewLocalDaemonRefusesABadOriginAtOnce(t *testing.T) {
	_, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "Test app", Origin: "http://127.0.0.1:5199/", Mode: goclient.ModeBrowser})
	var bad *goclient.OriginError
	if !errors.As(err, &bad) || !strings.Contains(err.Error(), `use "http://127.0.0.1:5199"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "Test app", Origin: "http://localhost:5173", Mode: goclient.ModeBrowser}); err != nil {
		t.Fatalf("a good origin: %v", err)
	}
	// A client of the caller's own (a test) needs no origin.
	if _, err := goclient.NewLocalDaemon(goclient.LocalDeps{Mode: goclient.ModeBrowser, Client: &fakeClient{}}); err != nil {
		t.Fatalf("a custom client: %v", err)
	}
}
