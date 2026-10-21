package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
)

// open shows the window for s and records what it reports to the daemon.
func open(s State) (*ui.Tester, *Settings, *[]string) {
	var sent []string
	w := &Settings{State: s, Host: Host{Running: true}, send: func(id string) { sent = append(sent, id) }}
	return ui.NewTester(w.View, 440, 1000), w, &sent
}

func full() State {
	return State{
		Version:  "1.2.3",
		Apps:     []string{"https://app.example"},
		Pages:    []string{"http://localhost:5173"},
		Sessions: 2,
		Agents: []Agent{
			{Name: "claude", Title: "Claude Code", Ready: true},
			{Name: "codex", Title: "Codex", Reason: "Codex is setting up (first run only, a few minutes)."},
		},
	}
}

func TestSettingsShowsWhatTheDaemonServes(t *testing.T) {
	tt, _, _ := open(full())
	for _, want := range []string{"agentd", "v1.2.3", "● Working", "Connected: 1 app, 1 website · 2 agents working", "Apps that can use your agents", "https://app.example", "http://localhost:5173", "website", "Your agents", "Claude Code", "Ready", "Codex", "Codex is setting up (first run only, a few minutes)."} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestSettingsShowsWhyADroppedAppIsStillListed(t *testing.T) {
	reason := "The app stopped this connection."
	tt, _, sent := open(State{
		Version: "1",
		AppList: []AppRow{{Server: "https://b.example", Status: "stopped", Reason: reason}},
		Notice:  "Could not disconnect https://b.example: disk full.",
	})
	for _, want := range []string{reason, "Could not disconnect https://b.example: disk full.", "https://b.example"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Disconnect https://b.example"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*sent, []string{"disconnect:https://b.example"}) {
		t.Fatalf("sent %v", *sent)
	}
}

func TestSettingsSaysWhatAgentdIsAndWhatToDoWhenNothingIsConnected(t *testing.T) {
	tt, _, _ := open(State{Version: "1"})
	if !tt.HasText(firstRunText) || !tt.HasText("How to start") || !tt.HasText("● agentd is ready") || tt.HasText("Your agents") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if tt.HasText("None yet") {
		t.Errorf("the old dead-end sentence is still in %q", tt.Texts())
	}
	for _, jargon := range []string{"daemon", "engine", "origin", "server"} {
		if strings.Contains(strings.ToLower(firstRunText), jargon) {
			t.Errorf("the first-run text says %q", jargon)
		}
	}
}

func TestSettingsSaysAgentdIsStartingBeforeTheFirstState(t *testing.T) {
	tt, _, _ := open(State{})
	if !tt.HasText("Starting…") || tt.HasText("No apps connected yet") || tt.HasText("How to start") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if !tt.HasText("agentd") || tt.HasText("agentd Starting") || tt.HasText("agentd starting") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func count(texts []string, want string) int {
	n := 0
	for _, t := range texts {
		if strings.Contains(t, want) {
			n++
		}
	}
	return n
}

func TestSettingsSaysWhenWebPagesCannotReachAgentdAsALineOfItsOwn(t *testing.T) {
	tt, _, _ := open(State{Version: "1", GatewayOff: true})
	if n := count(tt.Texts(), gatewayOffText); n != 1 {
		t.Fatalf("the sentence is on screen %d times, want once: %q", n, tt.Texts())
	}
	if !tt.HasText("No apps connected yet") {
		t.Errorf("what is connected is still said: %q", tt.Texts())
	}
	if tt.HasText("click Connect") {
		t.Errorf("the window sends the person to a Connect that cannot reach agentd: %q", tt.Texts())
	}

	apps := full()
	apps.GatewayOff = true
	tt, _, _ = open(apps)
	for _, want := range []string{"Connected: 1 app, 1 website · 2 agents working", gatewayOffText, "https://app.example"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if n := count(tt.Texts(), gatewayOffText); n != 1 {
		t.Errorf("the sentence is on screen %d times, want once", n)
	}
}

func TestDisconnectReportsThatOrigin(t *testing.T) {
	tt, _, sent := open(full())
	if err := tt.Click("Disconnect http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"disconnect:http://localhost:5173"}; !reflect.DeepEqual(*sent, want) {
		t.Fatalf("sent %v, want %v", *sent, want)
	}
}

// more opens the part of the window under the More button, as the person does.
func more(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("More"); err != nil {
		t.Fatal(err)
	}
}

func TestTheRarelyNeededPartOfTheWindowOpensUnderMore(t *testing.T) {
	tt, _, _ := open(full())
	for _, hidden := range []string{"Open log", "Restart agentd (stops 2 running agents)", "Quit agentd (stops 2 running agents)"} {
		if tt.HasText(hidden) {
			t.Errorf("%q is on screen before More", hidden)
		}
	}
	more(t, tt)
	if !tt.HasText("Less") || !tt.HasText("Open log") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Less"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("Open log") {
		t.Errorf("Less leaves the controls open: %q", tt.Texts())
	}
}

func TestTheControlsReportTheirAction(t *testing.T) {
	for _, c := range []struct{ click, id string }{{"Open log", "logs"}, {"Restart agentd (stops 2 running agents)", "restart"}, {"Quit agentd (stops 2 running agents)", QuitID}} {
		tt, _, sent := open(full())
		more(t, tt)
		if err := tt.Click(c.click); err != nil {
			t.Fatal(err)
		}
		if want := []string{c.id}; !reflect.DeepEqual(*sent, want) {
			t.Errorf("%s: sent %v, want %v", c.click, *sent, want)
		}
	}
}

func openApp(s State, h Host) (*ui.Tester, *[]string) {
	var sent []string
	w := &Settings{State: s, Host: h, send: func(id string) { sent = append(sent, id) }}
	return ui.NewTester(w.View, 440, 1000), &sent
}

func TestTheAppsWindowOpensAtLoginAndQuits(t *testing.T) {
	tt, sent := openApp(full(), Host{Running: true, OpenAtLogin: true})
	more(t, tt)
	for _, want := range []string{"Start when I log in", "Check for Updates…", "Quit agentd (stops 2 running agents)"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	tt.Click("Quit agentd (stops 2 running agents)")
	if !reflect.DeepEqual(*sent, []string{QuitID}) {
		t.Fatalf("sent %q", *sent)
	}
}

func TestTheAppsWindowSaysWhyAgentdStoppedAndStartsIt(t *testing.T) {
	tt, sent := openApp(State{}, Host{Note: "agentd crashed 5 times in a row: see the log"})
	if !tt.HasText("! agentd is not running") || !tt.HasText("agentd crashed 5 times in a row: see the log") {
		t.Fatalf("no note in %q", tt.Texts())
	}
	tt.Click("Start agentd")
	if !reflect.DeepEqual(*sent, []string{StartID}) {
		t.Fatalf("sent %q", *sent)
	}
}

func TestTheAppsWindowRestartSaysWhatItStopsAndReportsRestart(t *testing.T) {
	tt, sent := openApp(full(), Host{Running: true})
	more(t, tt)
	if err := tt.Click("Restart agentd (stops 2 running agents)"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*sent, []string{"restart"}) {
		t.Fatalf("sent %q", *sent)
	}
	idle := full()
	idle.Sessions = 0
	tt, _ = openApp(idle, Host{Running: true})
	more(t, tt)
	if !tt.HasText("Restart agentd") || tt.HasText("Restart agentd (stops") {
		t.Fatalf("with no agents running: texts %q", tt.Texts())
	}
}

func TestTheAppsWindowChecksForUpdates(t *testing.T) {
	tt, sent := openApp(full(), Host{Running: true})
	tt.Click("Check for Updates…")
	if !reflect.DeepEqual(*sent, []string{CheckUpdatesID}) {
		t.Fatalf("sent %q", *sent)
	}
}

func TestSettingsShowsTheNameEachAppGaveItselfWithItsOriginAndEnd(t *testing.T) {
	end := time.Date(2026, 10, 22, 14, 5, 0, 0, time.Local)
	tt, _, sent := open(State{
		Version:  "1",
		AppList:  []AppRow{{Server: "https://aiocean.io", Name: "AIOcean", Ends: end, Status: "connected"}, {Server: "https://plain.example", Status: "connected"}},
		Pages:    []string{"http://localhost:47899"},
		PageList: []agentui.PageRow{{Origin: "http://localhost:47899", Name: "Docs"}},
	})
	for _, want := range []string{"AIOcean", "https://aiocean.io · app · ends 22 Oct, 14:05", "https://plain.example", "Docs", "http://localhost:47899 · website"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Disconnect AIOcean"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*sent, []string{"disconnect:https://aiocean.io"}) {
		t.Fatalf("sent %v: Disconnect names the app by its origin", *sent)
	}
}
