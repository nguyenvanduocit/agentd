package goclient_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// A daemon that is there and refuses the call (the origin is wrong, the host is wrong, the caller is another user) is
// not "no daemon": a person who reads "agentd is not running" goes looking for an install problem that is not there.
func TestDetectSaysWhatTheGatewayRefusedInsteadOfNotFound(t *testing.T) {
	for _, c := range []struct {
		status int
		reason string
	}{
		{403, "origin"},
		{421, "host"},
		{403, "peer"},
		{404, "not-found"},
		{500, "error"},
	} {
		h := refusalWith(c.status, map[string]any{"granted": false, "reason": c.reason, "message": "why"}, nil)
		got := goclient.Detect(context.Background(), app, goclient.WithPorts(47615), goclient.WithHTTPClient(h))
		if got.Found || got.Refused != goclient.Reason(c.reason) {
			t.Errorf("%d %s: %+v", c.status, c.reason, got)
		}
	}
}

func TestAnotherProgramsErrorIsNotTheGatewaysRefusal(t *testing.T) {
	for name, res := range map[string]func() *http.Response{
		"text":                    func() *http.Response { return reply(403, "forbidden") },
		"json without a reason":   func() *http.Response { return reply(403, `{"error":"nope"}`) },
		"a reason, but granted":   func() *http.Response { return reply(403, `{"granted":true,"reason":"origin"}`) },
		"an empty reason":         func() *http.Response { return reply(403, `{"granted":false,"reason":""}`) },
		"a reason that is a list": func() *http.Response { return reply(403, `{"granted":false,"reason":["origin"]}`) },
		"a success":               func() *http.Response { return reply(200, `{"granted":false,"reason":"origin"}`) },
	} {
		h := clientWith(func(*http.Request) (*http.Response, error) { return res(), nil })
		if got := goclient.Detect(context.Background(), app, goclient.WithPorts(47615), goclient.WithHTTPClient(h)); got.Found || got.Refused != "" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestADaemonThatAnswersBeatsARefusalOnAnotherPort(t *testing.T) {
	h := clientWith(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "127.0.0.1:47615" {
			return jsonReply(403, map[string]any{"granted": false, "reason": "origin"}), nil
		}
		return jsonReply(200, status), nil
	})
	got := goclient.Detect(context.Background(), app, goclient.WithPorts(47615, 47616), goclient.WithHTTPClient(h))
	if !got.Found || got.Port != 47616 || got.Refused != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestTheFirstRefusalInThePortOrderIsTheOneReported(t *testing.T) {
	h := clientWith(func(r *http.Request) (*http.Response, error) {
		reason := "host"
		if r.URL.Host == "127.0.0.1:47615" {
			reason = "origin"
		}
		return jsonReply(403, map[string]any{"granted": false, "reason": reason}), nil
	})
	got := goclient.Detect(context.Background(), app, goclient.WithPorts(47616, 47615), goclient.WithHTTPClient(h))
	if got.Refused != goclient.ReasonHost { // the order given: 47616 first
		t.Fatalf("got %+v", got)
	}
}

func TestADetectedHasTheDaemonItIsFoundOn(t *testing.T) {
	if got := (goclient.Detected{Found: true, Port: 47619}).Daemon(); got != (goclient.Daemon{Port: 47619}) {
		t.Fatalf("Daemon = %+v", got)
	}
}
