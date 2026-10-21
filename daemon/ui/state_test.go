package ui

import (
	"reflect"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
)

var claude = pagetools.AgentInfo{Name: "claude", Title: "Claude Code", Ready: true, Reason: ""}

var baseFacts = Facts{Version: "1", GatewayPort: 47615}

func TestNewStateIsWhatTheAppShows(t *testing.T) {
	got := NewState(Facts{Version: "1.2.3", Apps: []string{"https://a"}, Browsers: []string{"http://localhost:5173"}, Sessions: 2, GatewayPort: 47615, Agents: []pagetools.AgentInfo{claude}})
	want := State{Version: "1.2.3", Apps: []string{"https://a"}, AppList: []AppRow{}, Pages: []string{"http://localhost:5173"}, PageList: []PageRow{}, Sessions: 2, GatewayOff: false, Agents: []pagetools.AgentInfo{claude}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNewStateSaysWhenNoPortWasFreeForTheGateway(t *testing.T) {
	f := baseFacts
	f.GatewayPort = 0
	if !NewState(f).GatewayOff {
		t.Fatal("gatewayOff is false")
	}
}

func TestActionOfReadsWhatAUserCanChooseInTheMenuAndTheSettingsWindow(t *testing.T) {
	cases := map[string]Action{
		"restart":                                {Kind: ActionRestart},
		"disconnect:https://a.example":           {Kind: ActionDisconnect, Origin: "https://a.example"},
		"disconnect:http://localhost:5173":       {Kind: ActionDisconnect, Origin: "http://localhost:5173"},
		"disconnect:HTTPS://A.EXAMPLE":           {},
		"disconnect:https://a.example:443":       {},
		"disconnect:https://a.example/with/path": {},
	}
	for id, want := range cases {
		got, ok := ActionOf(id)
		if want.Kind == "" {
			// Only an origin in its serialized form is one; the rest is refused.
			if ok {
				t.Errorf("%s: got %+v, want none", id, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %+v %v, want %+v", id, got, ok, want)
		}
	}
}

func TestActionOfIgnoresAnythingElse(t *testing.T) {
	for _, id := range []string{"format-disk", "logs", "stop", "updates:on", "settings", "disconnect:", "disconnect:ftp://x", "disconnect:http://evil.example", "oops", ""} {
		if got, ok := ActionOf(id); ok {
			t.Errorf("%q: got %+v, want none", id, got)
		}
	}
}
