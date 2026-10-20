package main

import (
	"reflect"
	"testing"
)

func labels(es []Entry) []string {
	var out []string
	for _, e := range es {
		if e.Separator {
			out = append(out, "---")
			continue
		}
		out = append(out, e.Label)
	}
	return out
}

func TestSummary(t *testing.T) {
	cases := []struct {
		name string
		s    State
		want string
	}{
		{"before the first state", State{}, "Starting…"},
		{"nothing connected", State{Version: "1"}, "Nothing connected"},
		{"one app", State{Version: "1", Apps: []string{"https://a"}}, "1 app"},
		{"apps and pages", State{Version: "1", Apps: []string{"https://a", "https://b"}, Pages: []string{"https://p"}}, "2 apps, 1 web page"},
		{"sessions", State{Version: "1", Apps: []string{"https://a"}, Sessions: 1}, "1 app, 1 agent running"},
		{"several sessions", State{Version: "1", Apps: []string{"https://a"}, Sessions: 3}, "1 app, 3 agents running"},
		// A first-run person with nothing connected is the one whose page cannot reach agentd: the problem is said first.
		{"gateway off, nothing connected", State{Version: "1", GatewayOff: true}, gatewayOffText},
		{"gateway off, an app connected", State{Version: "1", Apps: []string{"https://a"}, GatewayOff: true}, gatewayOffText},
	}
	for _, c := range cases {
		if got := Summary(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheGatewayOffSentenceIsPlainEnglish(t *testing.T) {
	const want = "Web pages can't reach agentd: its ports are in use by another program."
	if gatewayOffText != want {
		t.Fatalf("got %q, want %q", gatewayOffText, want)
	}
}

func TestBeforeTheFirstStateTheMenuSaysAgentdIsStarting(t *testing.T) {
	got := labels(BuildMenu(State{}, Host{}))[:2]
	if want := []string{"agentd", "Starting…"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRestartSaysWhatItEnds(t *testing.T) {
	for name, h := range map[string]Host{"daemon": {}, "app": {App: true, Running: true}} {
		for sessions, want := range map[int]string{0: "Restart", 1: "Restart (ends 1 agent)", 2: "Restart (ends 2 agents)"} {
			found := false
			for _, e := range BuildMenu(State{Version: "1", Managed: true, Sessions: sessions}, h) {
				if e.ID == "restart" {
					found = true
					if e.Label != want || e.Disabled {
						t.Errorf("%s with %d sessions: got %+v, want label %q", name, sessions, e, want)
					}
				}
			}
			if !found {
				t.Errorf("%s: no restart entry", name)
			}
		}
	}
}

func TestBuildMenuWithoutApps(t *testing.T) {
	got := labels(BuildMenu(State{Version: "1.2.3", Managed: true, Log: true}, Host{}))
	want := []string{"agentd 1.2.3", "Nothing connected", "---", "Settings…", "Open log", "Restart", "Stop daemon"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildMenuListsEachAppAndPageWithItsOwnDisconnect(t *testing.T) {
	m := BuildMenu(State{Version: "1", Managed: true, Apps: []string{"https://a"}, Pages: []string{"https://p"}}, Host{})
	var ids []string
	for _, e := range m {
		for _, c := range e.Children {
			ids = append(ids, c.ID)
		}
	}
	want := []string{"disconnect:https://a", "disconnect:https://p"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	if m[3].Label != "https://a" || m[4].Label != "https://p (web page)" {
		t.Fatalf("labels: %v", labels(m))
	}
}

func TestStopSaysWhatItEnds(t *testing.T) {
	m := BuildMenu(State{Version: "1", Managed: true, Sessions: 2}, Host{})
	last := m[len(m)-1]
	if last.ID != "stop" || last.Label != "Stop daemon (ends 2 agents)" {
		t.Fatalf("got %+v", last)
	}
	one := BuildMenu(State{Version: "1", Sessions: 1}, Host{})
	if got := one[len(one)-1].Label; got != "Stop daemon (ends 1 agent)" {
		t.Fatalf("got %q", got)
	}
}

func TestRestartIsDisabledWhenNothingRestartsTheDaemon(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1", Managed: false}, Host{}) {
		if e.ID == "restart" {
			if !e.Disabled || e.Label != "Restart (not managed)" {
				t.Fatalf("got %+v", e)
			}
			return
		}
	}
	t.Fatal("no restart entry")
}

func TestParseStateRejectsWhatIsNotAStateMessage(t *testing.T) {
	if _, ok := ParseState([]byte(`{"t":"state","version":"1","apps":["https://a"],"sessions":2,"managed":true}`)); !ok {
		t.Fatal("a state message was refused")
	}
	for _, bad := range []string{``, `nope`, `{"t":"other"}`, `{"version":"1"}`} {
		if _, ok := ParseState([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestActionLineIsOneJSONObject(t *testing.T) {
	got := string(ActionLine("disconnect:https://a"))
	if got != `{"t":"action","id":"disconnect:https://a"}` {
		t.Fatalf("got %s", got)
	}
}

func TestSettingsOpensTheWindowAndIsNotReported(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1"}, Host{}) {
		if e.Label == "Settings…" {
			if e.ID != SettingsID {
				t.Fatalf("got %+v", e)
			}
			return
		}
	}
	t.Fatal("no settings entry")
}

func TestOpenLogIsDisabledWhenTheDaemonWritesToATerminal(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1", Log: false}, Host{}) {
		if e.ID == "logs" {
			if !e.Disabled || e.Label != "Open log (in the terminal)" {
				t.Fatalf("got %+v", e)
			}
			return
		}
	}
	t.Fatal("no log entry")
}

func TestTheAppsMenuQuitsAgentdAndHandlesTheLogItself(t *testing.T) {
	s := State{Version: "1.2.3", Apps: []string{"https://a"}, Sessions: 2}
	got := BuildMenu(s, Host{App: true, Running: true})
	last := got[len(got)-1]
	if last.ID != QuitID || last.Label != "Quit agentd (ends 2 agents)" {
		t.Fatalf("last entry %+v", last)
	}
	if !reflect.DeepEqual(labels(got)[len(got)-5:], []string{"Settings…", "Check for Updates…", "Open log", "Restart (ends 2 agents)", "Quit agentd (ends 2 agents)"}) {
		t.Fatalf("controls %q", labels(got))
	}
	for _, e := range got {
		if (e.ID == "logs" || e.ID == "restart") && e.Disabled {
			t.Errorf("%s is off in the app, which keeps the log and restarts the engine", e.ID)
		}
	}
}

func TestTheAppsMenuOffersStartWhenTheEngineStopped(t *testing.T) {
	got := BuildMenu(State{}, Host{App: true, Note: "agentd was stopped"})
	want := []string{"agentd", "agentd was stopped", "---", "Start agentd", "Settings…", "Check for Updates…", "Open log", "Quit agentd"}
	if !reflect.DeepEqual(labels(got), want) {
		t.Fatalf("got %q, want %q", labels(got), want)
	}
	if got[3].ID != StartID {
		t.Fatalf("Start is %+v", got[3])
	}
}
