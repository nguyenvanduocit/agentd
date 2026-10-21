package main

import (
	"reflect"
	"testing"
	"time"

	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
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
		{"before the first state", State{}, ""},
		{"nothing connected", State{Version: "1"}, "No apps connected yet"},
		{"one app", State{Version: "1", Apps: []string{"https://a"}}, "Connected: 1 app"},
		{"apps and pages", State{Version: "1", Apps: []string{"https://a", "https://b"}, Pages: []string{"https://p"}}, "Connected: 2 apps, 1 website"},
		{"sessions", State{Version: "1", Apps: []string{"https://a"}, Sessions: 1}, "Connected: 1 app · 1 agent working"},
		{"several sessions", State{Version: "1", Apps: []string{"https://a"}, Sessions: 3}, "Connected: 1 app · 3 agents working"},
		// An editor that uses an agent through the ACP address is no app: the agent still works.
		{"agents working for no app", State{Version: "1", Sessions: 1}, "No apps connected · 1 agent working"},
		// Apps connect outbound, so a gateway that is off hides nothing from the count: its sentence is a line of its own.
		{"gateway off, nothing connected", State{Version: "1", GatewayOff: true}, "No apps connected yet"},
		{"gateway off, an app connected", State{Version: "1", Apps: []string{"https://a"}, Sessions: 2, GatewayOff: true}, "Connected: 1 app · 2 agents working"},
	}
	for _, c := range cases {
		if got := Summary(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHeadlineSaysWhetherAgentdWorksAndWhatNeedsTheUser(t *testing.T) {
	failing := []AppRow{{Server: "https://a", Status: "refused", Reason: "The app no longer lets this computer connect."}}
	cases := []struct {
		name string
		s    State
		h    Host
		want string
	}{
		{"before the first state", State{}, Host{Running: true}, "Starting…"},
		{"nothing saved", State{Version: "1"}, Host{Running: true}, "● agentd is ready"},
		{"an app connected", State{Version: "1", Apps: []string{"https://a"}}, Host{Running: true}, "● Working"},
		{"a website connected", State{Version: "1", Pages: []string{"https://p"}}, Host{Running: true}, "● Working"},
		{"one app cannot connect", State{Version: "1", AppList: failing}, Host{Running: true}, "! https://a can't connect"},
		{"two apps cannot connect", State{Version: "1", AppList: []AppRow{failing[0], {Server: "https://b", Status: "stopped", Reason: "The app ended this connection."}}}, Host{Running: true}, "! 2 apps can't connect"},
		{"a problem outranks a working app", State{Version: "1", Apps: []string{"https://b"}, AppList: append([]AppRow{{Server: "https://b", Status: "connected"}}, failing...)}, Host{Running: true}, "! https://a can't connect"},
		{"an app is connecting", State{Version: "1", AppList: []AppRow{{Server: "https://a", Status: "connecting"}}}, Host{Running: true}, "● Connecting https://a…"},
		{"the engine does not run", State{}, Host{Note: "agentd was stopped"}, "! agentd is not running"},
		{"the engine runs", State{Version: "1"}, Host{Running: true}, "● agentd is ready"},
	}
	for _, c := range cases {
		if got := Headline(c.s, c.h); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheGatewayOffSentenceIsPlainEnglish(t *testing.T) {
	const want = "Websites can't reach agentd: another program is using its port. Apps still work."
	if gatewayOffText != want {
		t.Fatalf("got %q, want %q", gatewayOffText, want)
	}
}

func TestTheGatewayOffNoteIsALineOfItsOwn(t *testing.T) {
	if got := GatewayNote(State{Version: "1"}); got != "" {
		t.Errorf("gateway on: got %q", got)
	}
	if got := GatewayNote(State{GatewayOff: true}); got != "" {
		t.Errorf("before the first state nothing is known: got %q", got)
	}
	if got := GatewayNote(State{Version: "1", GatewayOff: true}); got != gatewayOffText {
		t.Errorf("gateway off: got %q", got)
	}

	m := BuildMenu(State{Version: "1", Apps: []string{"https://a"}, Sessions: 1, GatewayOff: true}, Host{Running: true})
	if got, want := labels(m)[:5], []string{"● Working", "Connected: 1 app · 1 agent working", gatewayOffText, "---", "https://a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if note := m[2]; !note.Disabled || note.ID != "" || len(note.Children) != 0 {
		t.Errorf("the note is a line to read, not an action: %+v", note)
	}
	for _, e := range BuildMenu(State{Version: "1"}, Host{Running: true}) {
		if e.Label == gatewayOffText {
			t.Error("the note is there with the gateway on")
		}
	}
}

func TestBeforeTheFirstStateTheMenuSaysAgentdIsStarting(t *testing.T) {
	got := labels(BuildMenu(State{}, Host{Running: true}))[:2]
	if want := []string{"Starting…", "---"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRestartSaysWhatItStopsWhileWebsitesCannotReachAgentd(t *testing.T) {
	for sessions, want := range map[int]string{0: "Restart agentd", 1: "Restart agentd (stops 1 running agent)", 2: "Restart agentd (stops 2 running agents)"} {
		found := false
		for _, e := range BuildMenu(State{Version: "1", GatewayOff: true, Sessions: sessions}, Host{Running: true}) {
			if e.ID == "restart" {
				found = true
				if e.Label != want || e.Disabled {
					t.Errorf("with %d sessions: got %+v, want label %q", sessions, e, want)
				}
			}
		}
		if !found {
			t.Errorf("with %d sessions: no restart entry", sessions)
		}
	}
}

func TestTheAppsMenuOffersRestartOnlyWhileWebsitesCannotReachAgentd(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1"}, Host{Running: true}) {
		if e.ID == "restart" {
			t.Errorf("a healthy agentd offers %+v: Restart lives in the window", e)
		}
	}
}

func TestBuildMenuWithoutApps(t *testing.T) {
	got := labels(BuildMenu(State{Version: "1.2.3"}, Host{Running: true}))
	want := []string{"● agentd is ready", "No apps connected yet", "---", "Settings…", "Quit agentd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildMenuShowsWhyADroppedAppIsStillListed(t *testing.T) {
	reason := "The app no longer accepts this computer: its access was taken back."
	notice := "Could not disconnect https://b.example: disk full."
	m := BuildMenu(State{
		Version: "1",
		Apps:    []string{"https://a.example"},
		AppList: []AppRow{
			{Server: "https://a.example", Status: "connected"},
			{Server: "https://b.example", Status: "refused", Reason: reason},
		},
		Notice: notice,
	}, Host{Running: true})
	var sawNotice, sawReason, sawDisconnect bool
	for _, e := range m {
		if e.Label == notice && e.Disabled {
			sawNotice = true
		}
		if e.Label != "! https://b.example" {
			continue
		}
		for _, c := range e.Children {
			if c.Label == reason && c.Disabled {
				sawReason = true
			}
			if c.ID == "disconnect:https://b.example" && c.Label == "Disconnect" {
				sawDisconnect = true
			}
		}
	}
	if m[0].Label != "! https://b.example can't connect" || !m[0].Disabled {
		t.Errorf("the headline does not name the app that cannot connect: %+v", m[0])
	}
	if !sawNotice || !sawReason || !sawDisconnect {
		t.Fatalf("notice %v, reason %v, disconnect %v in %+v", sawNotice, sawReason, sawDisconnect, m)
	}
}

func TestBuildMenuListsEachAppAndPageWithItsOwnDisconnect(t *testing.T) {
	m := BuildMenu(State{Version: "1", Apps: []string{"https://a"}, Pages: []string{"https://p"}}, Host{Running: true})
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
	if m[3].Label != "https://a" || m[4].Label != "https://p (website)" {
		t.Fatalf("labels: %v", labels(m))
	}
}

func TestSettingsOpensTheWindowAndIsNotReported(t *testing.T) {
	for _, e := range BuildMenu(State{Version: "1"}, Host{Running: true}) {
		if e.Label == "Settings…" {
			if e.ID != SettingsID {
				t.Fatalf("got %+v", e)
			}
			return
		}
	}
	t.Fatal("no settings entry")
}

func TestTheAppsMenuHasTheFewControlsThatNeedNoWindow(t *testing.T) {
	s := State{Version: "1.2.3", Apps: []string{"https://a"}, Sessions: 2}
	got := BuildMenu(s, Host{Running: true})
	last := got[len(got)-1]
	if last.ID != QuitID || last.Label != "Quit agentd (stops 2 running agents)" {
		t.Fatalf("last entry %+v", last)
	}
	if !reflect.DeepEqual(labels(got)[len(got)-2:], []string{"Settings…", "Quit agentd (stops 2 running agents)"}) {
		t.Fatalf("controls %q", labels(got))
	}
}

func TestTheAppsMenuOffersStartWhenTheEngineStopped(t *testing.T) {
	got := BuildMenu(State{}, Host{Note: "agentd was stopped"})
	want := []string{"! agentd is not running", "agentd was stopped", "---", "Start agentd", "Settings…", "Open log", "Quit agentd"}
	if !reflect.DeepEqual(labels(got), want) {
		t.Fatalf("got %q, want %q", labels(got), want)
	}
	if got[3].ID != StartID {
		t.Fatalf("Start is %+v", got[3])
	}
}

func TestBuildMenuShowsTheNameOfAnAppAndOfAPageWithTheirOriginsUnder(t *testing.T) {
	end := time.Date(2026, 10, 22, 14, 5, 0, 0, time.Local)
	m := BuildMenu(State{
		Version:  "1",
		Apps:     []string{"https://aiocean.io"},
		AppList:  []AppRow{{Server: "https://aiocean.io", Name: "AIOcean", Ends: end, Status: "connected"}},
		Pages:    []string{"http://localhost:47899"},
		PageList: []agentui.PageRow{{Origin: "http://localhost:47899", Name: "Docs"}},
	}, Host{Running: true})
	if m[3].Label != "AIOcean" || m[4].Label != "Docs (website)" {
		t.Fatalf("labels: %v", labels(m))
	}
	if got := m[3].Children[0]; got.Label != "https://aiocean.io · ends 22 Oct, 14:05" || !got.Disabled {
		t.Errorf("under the app: %+v", got)
	}
	if got := m[4].Children[0]; got.Label != "http://localhost:47899" || !got.Disabled {
		t.Errorf("under the page: %+v", got)
	}
	if last := m[3].Children[len(m[3].Children)-1]; last.ID != "disconnect:https://aiocean.io" {
		t.Errorf("Disconnect names the origin: %+v", last)
	}
}

func TestTheHeadlineNamesAnAppThatCannotConnectByItsName(t *testing.T) {
	s := State{Version: "1", AppList: []AppRow{{Server: "https://a", Name: "AIOcean", Status: "refused", Reason: "x"}}}
	if got := Headline(s, Host{Running: true}); got != "! AIOcean can't connect" {
		t.Errorf("headline %q", got)
	}
}
