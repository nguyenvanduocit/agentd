package goclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// roundTrip is an http.Client's transport that answers with a function: no network, whatever the URL.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientWith(f roundTrip) *http.Client { return &http.Client{Transport: f} }

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func jsonReply(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return reply(status, string(b))
}

// hang is a daemon that never answers: it holds the request until its context ends, as a real connection would.
func hang(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

var status = map[string]any{"name": "agentd", "version": "1.2.3", "granted": map[string]any{"server": false, "browser": false}}

func with(base map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestDefaultPortsAreTheGatewaysRange(t *testing.T) {
	ports := goclient.DefaultPorts()
	if len(ports) != 10 || ports[0] != 47615 || ports[9] != 47624 {
		t.Fatalf("ports = %v", ports)
	}
	ports[0] = 1
	if goclient.DefaultPorts()[0] != 47615 {
		t.Fatal("DefaultPorts shares its slice")
	}
}

func TestDetectFindsTheDaemonOnTheFirstPortThatAnswersLikeOne(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var origins []string
	c := clientWith(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		origins = append(origins, r.Header.Get("Origin"))
		mu.Unlock()
		if r.URL.Host == "127.0.0.1:47616" {
			return jsonReply(200, status), nil
		}
		return nil, errors.New("connection refused")
	})
	got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c))
	want := goclient.Detected{Found: true, Port: 47616, Version: "1.2.3"}
	if got.Found != want.Found || got.Port != want.Port || got.Version != want.Version || got.Granted != want.Granted || got.Agents != nil {
		t.Fatalf("Detect = %+v", got)
	}
	if !slices.Contains(seen, "http://127.0.0.1:47615/v1/status") {
		t.Fatalf("asked %v", seen)
	}
	for _, o := range origins {
		if o != "http://localhost:5173" {
			t.Fatalf("Origin = %q", o)
		}
	}
}

func TestDetectAnswersTheFirstPortInTheOrderGivenWhateverTheAnswerOrder(t *testing.T) {
	c := clientWith(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "127.0.0.1:47615" {
			time.Sleep(50 * time.Millisecond) // the first port is the slowest
		}
		return jsonReply(200, status), nil
	})
	if got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c), goclient.WithPorts(47615, 47616)); got.Port != 47615 {
		t.Fatalf("port = %d", got.Port)
	}
}

func TestEveryAgentHasATitleTheDaemonsOrItsNameFromADaemonThatSendsNone(t *testing.T) {
	agents := []map[string]any{
		{"name": "claude", "title": "Claude Code", "ready": true, "reason": ""},
		{"name": "codex", "ready": false, "reason": "not installed"},
	}
	c := clientWith(func(*http.Request) (*http.Response, error) {
		return jsonReply(200, with(status, map[string]any{"agents": agents})), nil
	})
	got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c), goclient.WithPorts(47615))
	want := []goclient.AgentInfo{
		{Name: "claude", Title: "Claude Code", Ready: true},
		{Name: "codex", Title: "codex", Reason: "not installed"},
	}
	if !slices.Equal(got.Agents, want) {
		t.Fatalf("agents = %+v", got.Agents)
	}
}

func TestAnEmptyListOfAgentsIsNotTheAbsenceOfOne(t *testing.T) {
	c := clientWith(func(*http.Request) (*http.Response, error) {
		return jsonReply(200, with(status, map[string]any{"agents": []any{}})), nil
	})
	got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c), goclient.WithPorts(47615))
	if got.Agents == nil || len(got.Agents) != 0 {
		t.Fatalf("agents = %#v", got.Agents)
	}
}

func TestADaemonThatSendsNoGrantedIsGrantedNothing(t *testing.T) {
	c := clientWith(func(*http.Request) (*http.Response, error) {
		return reply(200, `{"name":"agentd","version":"0.1"}`), nil
	})
	got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c), goclient.WithPorts(47615))
	if !got.Found || got.Granted != (goclient.Granted{}) {
		t.Fatalf("Detect = %+v", got)
	}
}

func TestAnotherProgramOnAPortIsNotTheDaemon(t *testing.T) {
	for _, body := range []string{`{"name":"something-else"}`, `{"name":"agentd"}`, `{"name":"agentd","version":3}`, `not json`, ``} {
		c := clientWith(func(*http.Request) (*http.Response, error) { return reply(200, body), nil })
		if got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c)); got.Found {
			t.Errorf("%q: found %+v", body, got)
		}
	}
}

func TestARefusalABadBodyAndAHangAllMeanNotFound(t *testing.T) {
	c := clientWith(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "127.0.0.1:47615":
			return jsonReply(403, map[string]any{"reason": "origin"}), nil
		case "127.0.0.1:47616":
			return reply(200, "not json"), nil
		}
		return hang(r)
	})
	started := time.Now()
	got := goclient.Detect(context.Background(), "http://localhost:5173", goclient.WithHTTPClient(c), goclient.WithTimeout(100*time.Millisecond), goclient.WithPorts(47615, 47616, 47617))
	if got.Found {
		t.Fatalf("found %+v", got)
	}
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("took %v", time.Since(started))
	}
}

func TestDetectStopsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	got := goclient.Detect(ctx, "http://localhost:5173", goclient.WithHTTPClient(clientWith(hang)), goclient.WithTimeout(time.Minute))
	if got.Found || time.Since(started) > 2*time.Second {
		t.Fatalf("found %+v after %v", got, time.Since(started))
	}
}

// ---- connect ----

var found = goclient.Daemon{Port: 47615}

const app = "https://app.example.com"

// capture is a request as the daemon would read it.
type capture struct {
	method, url, origin, contentType, body string
}

func capturing(res *http.Response, got *capture) *http.Client {
	return clientWith(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		*got = capture{r.Method, r.URL.String(), r.Header.Get("Origin"), r.Header.Get("Content-Type"), body}
		return res, nil
	})
}

func TestConnectServerPostsTheCodeAndNoServerAndReportsAllow(t *testing.T) {
	var got capture
	c := capturing(jsonReply(200, map[string]any{"granted": true}), &got)
	if r := goclient.ConnectServer(context.Background(), found, app, "Test app", "ABCD", goclient.WithHTTPClient(c)); r != (goclient.ConnectResult{Granted: true}) {
		t.Fatalf("result = %+v", r)
	}
	want := capture{"POST", "http://127.0.0.1:47615/v1/connect", app, "application/json", `{"mode":"server","name":"Test app","code":"ABCD"}`}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestTheBodiesAreWrittenAsJSONStringifyWritesThem(t *testing.T) {
	var got capture
	c := capturing(jsonReply(200, map[string]any{"granted": true}), &got)
	goclient.ConnectServer(context.Background(), found, app, "Test app", `a<b>&"c"`, goclient.WithHTTPClient(c))
	if want := `{"mode":"server","name":"Test app","code":"a<b>&\"c\""}`; got.body != want {
		t.Fatalf("body = %s, want %s", got.body, want)
	}
}

func TestConnectBrowserPostsModeBrowser(t *testing.T) {
	var got capture
	c := capturing(jsonReply(200, map[string]any{"granted": true}), &got)
	if r := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(c)); !r.Granted {
		t.Fatalf("result = %+v", r)
	}
	if got.body != `{"mode":"browser","name":"Test app"}` || got.origin != app {
		t.Fatalf("request = %+v", got)
	}
}

func TestEachRefusalIsItsReasonAndTheDaemonsMessage(t *testing.T) {
	for _, c := range []struct {
		status int
		reason goclient.Reason
	}{
		{403, goclient.ReasonDenied},
		{403, goclient.ReasonTimeout},
		{429, goclient.ReasonCooldown},
		{409, goclient.ReasonBusy},
		{503, goclient.ReasonNoDialog},
		{502, goclient.ReasonPairing},
		{403, goclient.ReasonOrigin},
		{400, goclient.ReasonMalformed},
	} {
		h := clientWith(func(*http.Request) (*http.Response, error) {
			return jsonReply(c.status, map[string]any{"granted": false, "reason": c.reason, "message": "why"}), nil
		})
		want := goclient.ConnectResult{Reason: c.reason, Message: "why"}
		if got := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(h)); got != want {
			t.Errorf("HTTP %d %s: %+v", c.status, c.reason, got)
		}
	}
}

func TestAnAnswerThatIsNotAGrantIsMalformedUnlessItSaysWhy(t *testing.T) {
	for _, c := range []struct{ status, body string }{
		{"200", `{"granted":false}`},
		{"200", `not json`},
		{"500", `{"granted":true}`},
		{"200", `{"granted":"yes"}`},
	} {
		h := clientWith(func(*http.Request) (*http.Response, error) {
			status := 200
			if c.status == "500" {
				status = 500
			}
			return reply(status, c.body), nil
		})
		if got := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(h)); got.Granted || got.Reason != goclient.ReasonMalformed {
			t.Errorf("%s %s: %+v", c.status, c.body, got)
		}
	}
}

func TestADaemonThatWentAwayIsUnreachable(t *testing.T) {
	h := clientWith(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
	got := goclient.ConnectBrowser(context.Background(), found, app, "Test app", goclient.WithHTTPClient(h))
	if got.Granted || got.Reason != goclient.ReasonUnreachable || !strings.Contains(got.Message, "connection refused") {
		t.Fatalf("result = %+v", got)
	}
}

func TestAConnectionThatWaitsForTheDialogEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got := goclient.ConnectBrowser(ctx, found, app, "Test app", goclient.WithHTTPClient(clientWith(hang)))
	if got.Granted || got.Reason != goclient.ReasonCancelled { // the daemon did answer nothing yet: its dialog may still be open
		t.Fatalf("result = %+v", got)
	}
}

func TestDisconnectSendsDeleteAndSaysWhetherTheDaemonConfirmed(t *testing.T) {
	var got capture
	c := capturing(jsonReply(200, map[string]any{"granted": false}), &got)
	if err := goclient.Disconnect(context.Background(), found, app, goclient.WithHTTPClient(c)); err != nil {
		t.Fatal(err)
	}
	if want := (capture{"DELETE", "http://127.0.0.1:47615/v1/connect", app, "", ""}); got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
	refused := clientWith(func(*http.Request) (*http.Response, error) {
		return jsonReply(403, map[string]any{"reason": "origin"}), nil
	})
	if err := goclient.Disconnect(context.Background(), found, app, goclient.WithHTTPClient(refused)); err == nil {
		t.Fatal("a refusal is no confirmation")
	}
	away := clientWith(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
	if err := goclient.Disconnect(context.Background(), found, app, goclient.WithHTTPClient(away)); err == nil {
		t.Fatal("no answer is no confirmation")
	}
}

func TestEveryReasonHasASentenceAPersonCanActOn(t *testing.T) {
	for _, r := range knownReasons { // every constant: the words themselves are tested against the golden file
		if len(goclient.ReasonText(r)) <= 20 {
			t.Errorf("%s: %q", r, goclient.ReasonText(r))
		}
	}
	if text := goclient.ReasonText("from-a-newer-daemon"); !strings.Contains(text, "from-a-newer-daemon") {
		t.Errorf("a reason this package does not know: %q", text)
	}
}
