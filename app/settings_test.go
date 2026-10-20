package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// open shows the window for s and records what it reports to the daemon.
func open(s State) (*ui.Tester, *Settings, *[]string) {
	var sent []string
	w := &Settings{State: s, send: func(id string) { sent = append(sent, id) }}
	return ui.NewTester(w.View, 440, 640), w, &sent
}

func full() State {
	return State{
		Version:   "1.2.3",
		Apps:      []string{"https://app.example"},
		Pages:     []string{"http://localhost:5173"},
		Sessions:  2,
		Managed:   true,
		Log:       true,
		Updates:   true,
		Updatable: true,
		Agents: []Agent{
			{Name: "claude", Title: "Claude Code", Ready: true},
			{Name: "codex", Title: "Codex", Reason: "installing the Codex adapter"},
		},
	}
}

func TestSettingsShowsWhatTheDaemonServes(t *testing.T) {
	tt, _, _ := open(full())
	for _, want := range []string{"agentd 1.2.3", "1 app, 1 web page, 2 agents running", "https://app.example", "http://localhost:5173", "Claude Code", "Ready", "Codex", "installing the Codex adapter", "Install updates automatically"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestSettingsSaysWhatAgentdIsAndWhatToDoWhenNothingIsConnected(t *testing.T) {
	tt, _, _ := open(State{Version: "1"})
	if !tt.HasText("agentd lets apps you approve run AI coding agents on this computer. Go back to the app that sent you here and choose Connect.") || tt.HasText("Agents") {
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
	if !tt.HasText("Starting…") || tt.HasText("Nothing connected") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if !tt.HasText("agentd") || tt.HasText("agentd Starting") || tt.HasText("agentd starting") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestSettingsSaysWhenWebPagesCannotReachAgentd(t *testing.T) {
	tt, _, _ := open(State{Version: "1", GatewayOff: true})
	if !tt.HasText("Web pages can't reach agentd: its ports are in use by another program.") || tt.HasText("Nothing connected") {
		t.Fatalf("texts %q", tt.Texts())
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

func TestTheUpdatesSwitchReportsTheNewSettingAndShowsItAtOnce(t *testing.T) {
	tt, w, sent := open(full())
	if err := tt.Click("Install updates automatically"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Install updates automatically"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"updates:off", "updates:on"}; !reflect.DeepEqual(*sent, want) {
		t.Fatalf("sent %v, want %v", *sent, want)
	}
	if !w.State.Updates {
		t.Fatal("the switch does not show the last choice")
	}
}

func TestTheUpdatesSwitchIsOffAndSaysWhyWhenTheDaemonCannotUpdate(t *testing.T) {
	s := full()
	s.Updatable = false
	tt, _, sent := open(s)
	if err := tt.Click("Install updates automatically"); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %v", *sent)
	}
	if !tt.HasText("cannot update itself") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestTheControlsReportTheirAction(t *testing.T) {
	for _, c := range []struct{ click, id string }{{"Open log", "logs"}, {"Restart (ends 2 agents)", "restart"}, {"Stop daemon (ends 2 agents)", "stop"}} {
		tt, _, sent := open(full())
		if err := tt.Click(c.click); err != nil {
			t.Fatal(err)
		}
		if want := []string{c.id}; !reflect.DeepEqual(*sent, want) {
			t.Errorf("%s: sent %v, want %v", c.click, *sent, want)
		}
	}
}

func TestRestartDoesNothingWhenNoServiceRestartsTheDaemon(t *testing.T) {
	s := full()
	s.Managed = false
	tt, _, sent := open(s)
	if err := tt.Click("Restart (ends 2 agents)"); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %v", *sent)
	}
	if !tt.HasText("No service restarts this daemon") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestOpenLogDoesNothingAndSaysWhereTheLogIsWhenThereIsNoFile(t *testing.T) {
	s := full()
	s.Log = false
	tt, _, sent := open(s)
	if err := tt.Click("Open log"); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %v", *sent)
	}
	if !tt.HasText("writes its log to the terminal it runs in") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func openApp(s State, h Host) (*ui.Tester, *[]string) {
	var sent []string
	w := &Settings{State: s, Host: h, send: func(id string) { sent = append(sent, id) }}
	return ui.NewTester(w.View, 440, 640), &sent
}

func TestTheAppsWindowOpensAtLoginAndQuits(t *testing.T) {
	tt, sent := openApp(full(), Host{App: true, Running: true, OpenAtLogin: true})
	for _, want := range []string{"Open at login", "Check for Updates…", "Quit agentd (ends 2 agents)"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("Install updates automatically") {
		t.Error("the app's updates are not the engine's switch")
	}
	tt.Click("Quit agentd (ends 2 agents)")
	if !reflect.DeepEqual(*sent, []string{QuitID}) {
		t.Fatalf("sent %q", *sent)
	}
}

func TestTheAppsWindowSaysWhyAgentdStoppedAndStartsIt(t *testing.T) {
	tt, sent := openApp(State{}, Host{App: true, Note: "agentd crashed 5 times in a row: see the log"})
	if !tt.HasText("agentd crashed 5 times in a row: see the log") {
		t.Fatalf("no note in %q", tt.Texts())
	}
	tt.Click("Start agentd")
	if !reflect.DeepEqual(*sent, []string{StartID}) {
		t.Fatalf("sent %q", *sent)
	}
}

func TestTheAppsWindowRestartSaysWhatItEndsAndReportsRestart(t *testing.T) {
	tt, sent := openApp(full(), Host{App: true, Running: true})
	if err := tt.Click("Restart (ends 2 agents)"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*sent, []string{"restart"}) {
		t.Fatalf("sent %q", *sent)
	}
	idle := full()
	idle.Sessions = 0
	tt, _ = openApp(idle, Host{App: true, Running: true})
	if !tt.HasText("Restart") || tt.HasText("Restart (ends") {
		t.Fatalf("with no agents running: texts %q", tt.Texts())
	}
}

func TestTheAppsWindowChecksForUpdates(t *testing.T) {
	tt, sent := openApp(full(), Host{App: true, Running: true})
	tt.Click("Check for Updates…")
	if !reflect.DeepEqual(*sent, []string{CheckUpdatesID}) {
		t.Fatalf("sent %q", *sent)
	}
}
