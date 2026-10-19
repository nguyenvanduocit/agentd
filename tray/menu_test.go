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
		{"nothing connected", State{}, "Nothing connected"},
		{"one app", State{Apps: []string{"https://a"}}, "1 app"},
		{"apps and pages", State{Apps: []string{"https://a", "https://b"}, Pages: []string{"https://p"}}, "2 apps, 1 web page"},
		{"sessions", State{Apps: []string{"https://a"}, Sessions: 1}, "1 app, 1 agent running"},
		{"several sessions", State{Apps: []string{"https://a"}, Sessions: 3}, "1 app, 3 agents running"},
		{"gateway off", State{Apps: []string{"https://a"}, GatewayOff: true}, "1 app, web pages cannot connect"},
	}
	for _, c := range cases {
		if got := Summary(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildMenuWithoutApps(t *testing.T) {
	got := labels(BuildMenu(State{Version: "1.2.3", Managed: true}))
	want := []string{"agentd 1.2.3", "Nothing connected", "---", "Open log", "Restart", "Stop daemon"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildMenuListsEachAppAndPageWithItsOwnDisconnect(t *testing.T) {
	m := BuildMenu(State{Version: "1", Managed: true, Apps: []string{"https://a"}, Pages: []string{"https://p"}})
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
	m := BuildMenu(State{Version: "1", Managed: true, Sessions: 2})
	last := m[len(m)-1]
	if last.ID != "stop" || last.Label != "Stop daemon (ends 2 agents)" {
		t.Fatalf("got %+v", last)
	}
	one := BuildMenu(State{Version: "1", Sessions: 1})
	if got := one[len(one)-1].Label; got != "Stop daemon (ends 1 agent)" {
		t.Fatalf("got %q", got)
	}
}

func TestRestartIsDisabledWhenNothingRestartsTheDaemon(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1", Managed: false}) {
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
