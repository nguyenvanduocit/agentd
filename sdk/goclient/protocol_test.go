package goclient_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

func TestDetectReadsTheProtocolTheDaemonStates(t *testing.T) {
	stating := clientWith(func(*http.Request) (*http.Response, error) {
		return jsonReply(200, with(status, map[string]any{"protocol": 1})), nil
	})
	if got := goclient.Detect(context.Background(), app, goclient.WithHTTPClient(stating), goclient.WithPorts(47615)); !got.Found || got.Protocol != 1 {
		t.Errorf("Detect = %+v, want protocol 1", got)
	}
	without := clientWith(func(*http.Request) (*http.Response, error) { return jsonReply(200, status), nil })
	if got := goclient.Detect(context.Background(), app, goclient.WithHTTPClient(without), goclient.WithPorts(47615)); !got.Found || got.Protocol != 0 {
		t.Errorf("Detect = %+v, want protocol 0 for a daemon that states none", got)
	}
}

func TestSupportedProtocolsIsACopy(t *testing.T) {
	got := goclient.SupportedProtocols()
	if !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("SupportedProtocols = %v", got)
	}
	got[0] = 99
	if goclient.SupportedProtocols()[0] != 1 {
		t.Fatal("SupportedProtocols shares its slice")
	}
}

func TestDetectedIncompatibleIsASentenceOnlyForAProtocolThisClientDoesNotSpeak(t *testing.T) {
	for _, c := range []struct {
		name string
		d    goclient.Detected
		want string // "" is compatible
	}{
		{"nothing found", goclient.Detected{}, ""},
		{"the daemon states no protocol", goclient.Detected{Found: true}, ""},
		{"the daemon speaks protocol 1", goclient.Detected{Found: true, Protocol: 1}, ""},
		{"the daemon speaks protocol 2", goclient.Detected{Found: true, Protocol: 2}, ""},
		{"the daemon speaks a newer protocol", goclient.Detected{Found: true, Protocol: 3}, "newer than this app understands"},
	} {
		got := c.d.Incompatible()
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: Incompatible() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestALocalDaemonOfAProtocolThisClientDoesNotSpeakCannotConnect(t *testing.T) {
	local, client := setup(t, setupOptions{detected: []goclient.Detected{{Found: true, Port: 47615, Version: "9.0.0", Protocol: 3}}})
	local.Detect(bg)
	st := local.State()
	if st.Incompatible == "" || st.CanConnect || st.Error != st.Incompatible {
		t.Fatalf("state = %+v, want an Incompatible sentence, no CanConnect, and Error repeating it", st)
	}
	if local.Connect(bg) {
		t.Error("Connect succeeded with a daemon this client does not speak to")
	}
	for _, call := range client.Calls() {
		if strings.HasPrefix(call, "connect") || call == "startPairing" {
			t.Errorf("the client asked the daemon: %v", client.Calls())
		}
	}
}

func TestALocalDaemonOfAProtocolItSpeaksHasNoIncompatible(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{{Found: true, Port: 47615, Version: "1.0.0", Protocol: 1}}})
	local.Detect(bg)
	if st := local.State(); st.Incompatible != "" || !st.CanConnect {
		t.Errorf("state = %+v", st)
	}
}

// Disconnect already answered with an error; a caller could not tell which refusal it was.
func TestDisconnectsRefusalCarriesItsReason(t *testing.T) {
	refused := clientWith(func(*http.Request) (*http.Response, error) {
		return jsonReply(403, map[string]any{"reason": "peer", "message": "another user"}), nil
	})
	err := goclient.Disconnect(context.Background(), found, app, goclient.WithHTTPClient(refused))
	var refusal *goclient.DisconnectError
	if !errors.As(err, &refusal) || refusal.Status != 403 || refusal.Reason != goclient.Reason("peer") || refusal.Message != "another user" {
		t.Fatalf("err = %#v, want a *DisconnectError with status 403, reason peer", err)
	}
	if want := "the daemon refused (HTTP 403, peer): another user"; err.Error() != want {
		t.Errorf("message = %q, want %q: callers read it", err.Error(), want)
	}

	away := clientWith(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
	err = goclient.Disconnect(context.Background(), found, app, goclient.WithHTTPClient(away))
	if err == nil || errors.As(err, &refusal) || !strings.Contains(err.Error(), "the daemon did not answer") {
		t.Errorf("no answer = %v, want the transport error and no *DisconnectError", err)
	}
}

func TestALocalDaemonShowsAFailedDisconnect(t *testing.T) {
	local, client := setup(t, setupOptions{detected: []goclient.Detected{connected}})
	local.Detect(bg)
	client.disconnectErr = &goclient.DisconnectError{Status: 403, Reason: goclient.Reason("peer")}
	local.Disconnect(bg)
	if got, want := local.State().Error, goclient.ReasonText(goclient.Reason("peer")); got != want {
		t.Errorf("Error = %q, want %q: a disconnect that failed must not look as if it worked", got, want)
	}

	client.disconnectErr = errors.New("connection refused")
	local.Disconnect(bg)
	if got, want := local.State().Error, goclient.ReasonText(goclient.ReasonUnreachable); got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}

	client.disconnectErr = nil
	local.Disconnect(bg)
	if got := local.State().Error; got != "" {
		t.Errorf("Error = %q after a disconnect the daemon confirmed", got)
	}
}
