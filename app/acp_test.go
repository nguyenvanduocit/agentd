package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The address of the standard ACP endpoint reaches the app with its token in the state the daemon shows.
// The menu and the window offer to copy it for a program that speaks ACP (an editor, a script); the token is copied,
// never drawn.

const testToken = "tokentokentokentokentokentokentokentokento"

func withAcp() State {
	s := full()
	s.Acp = &AcpAccess{Base: "ws://127.0.0.1:47615/acp/", Token: testToken, Agents: []string{"claude"}}
	return s
}

func TestTheMenuOffersToCopyTheAddressOfEachReadyAgent(t *testing.T) {
	s := withAcp()
	s.Acp.Agents = []string{"claude", "codex"}
	s.Agents = []Agent{{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Ready: true}}
	menu := BuildMenu(s, Host{Running: true})
	var group *Entry
	for i := range menu {
		if menu[i].Label == "Use in an editor or tool" {
			group = &menu[i]
		}
	}
	if group == nil || len(group.Children) != 2 {
		t.Fatalf("the menu has no submenu of addresses to copy: %+v", menu)
	}
	for i, want := range []struct{ id, label string }{{"copy-acp:claude", "Copy address for Claude Code"}, {"copy-acp:codex", "Copy address for Codex"}} {
		if got := group.Children[i]; got.ID != want.id || got.Label != want.label || got.Disabled {
			t.Errorf("child %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestTheMenuOffersNothingToCopyWithoutAnAddressOrAnAgent(t *testing.T) {
	for name, s := range map[string]State{
		"no endpoint":         full(),
		"no agent is ready":   func() State { s := withAcp(); s.Acp.Agents = nil; return s }(),
		"before the first":    {},
		"the engine stopped":  withAcp(),
		"gateway off (state)": func() State { s := withAcp(); s.Acp = nil; s.GatewayOff = true; return s }(),
	} {
		host := Host{Running: true}
		if name == "the engine stopped" {
			host = Host{Note: "agentd could not start."}
		}
		for _, e := range BuildMenu(s, host) {
			if e.Label == "Use in an editor or tool" || strings.HasPrefix(e.ID, "copy-acp:") {
				t.Errorf("%s: the menu offers %+v", name, e)
			}
		}
	}
}

func TestAcpAddress(t *testing.T) {
	s := withAcp()
	if got, ok := AcpAddress(s, "claude"); !ok || got != "ws://127.0.0.1:47615/acp/claude?token="+testToken {
		t.Errorf("AcpAddress(claude) = %q, %v", got, ok)
	}
	s.Acp.Base = "ws://127.0.0.1:47616/acp" // a base without the final slash works too
	if got, _ := AcpAddress(s, "claude"); got != "ws://127.0.0.1:47616/acp/claude?token="+testToken {
		t.Errorf("AcpAddress with a base with no slash = %q", got)
	}
	for _, agent := range []string{"codex", "", "../claude", "claude?x=1", "claude/extra"} {
		if got, ok := AcpAddress(s, agent); ok {
			t.Errorf("AcpAddress(%q) = %q: only an agent the daemon said is ready has an address", agent, got)
		}
	}
	if _, ok := AcpAddress(full(), "claude"); ok {
		t.Error("an address from a state with no endpoint")
	}
}

func TestTheSettingsWindowOffersTheAddressWithoutShowingTheToken(t *testing.T) {
	s := withAcp()
	tt, _, sent := open(s)
	if tt.HasText("Use agentd in an editor or other tool") {
		t.Errorf("the address section is on screen before More: %q", tt.Texts())
	}
	more(t, tt)
	for _, want := range append([]string{"Use agentd in an editor or other tool"}, acpIntro...) {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, testToken) || strings.Contains(text, "token=") || strings.Contains(text, "ws://") {
			t.Errorf("the window draws the address or the token: %q", text)
		}
	}
	if err := tt.Click("Copy address for Claude Code"); err != nil {
		t.Fatalf("no copy button: %v; texts %q", err, tt.Texts())
	}
	if len(*sent) != 1 || (*sent)[0] != "copy-acp:claude" {
		t.Errorf("the button reported %v, want [copy-acp:claude]", *sent)
	}
}

func TestTheSettingsWindowHasNoAddressSectionWithoutAnEndpointOrAnAgent(t *testing.T) {
	for name, s := range map[string]State{
		"no endpoint":       full(),
		"no agent is ready": func() State { s := withAcp(); s.Acp.Agents = nil; return s }(),
	} {
		tt, _, _ := open(s)
		more(t, tt)
		if tt.HasText("Use agentd in an editor or other tool") {
			t.Errorf("%s: the window has the section", name)
		}
	}
}

func TestTheNoticeAfterACopyIsShownInTheWindow(t *testing.T) {
	var sent []string
	w := &Settings{State: withAcp(), Host: Host{Running: true}, More: true, send: func(id string) { sent = append(sent, id) }, Notice: "Copied the address for Claude Code. Paste it into the agent URL setting of your editor or tool. It works like a password: don't share it."}
	tt := ui.NewTester(w.View, 440, 1000)
	if !tt.HasText(w.Notice) {
		t.Errorf("no notice in %q", tt.Texts())
	}
}

// Choosing a copy puts the address on the clipboard in the app, tells the daemon nothing, and never logs it.
func TestChoosingACopyPutsTheAddressOnTheClipboardAndReportsNothing(t *testing.T) {
	var clipboard, reported []string
	a := &app{report: func(id string) { reported = append(reported, id) }, copy: func(text string) { clipboard = append(clipboard, text) }}
	a.settings = Settings{State: withAcp(), send: a.choose}

	a.choose("copy-acp:claude")
	if len(clipboard) != 1 || clipboard[0] != "ws://127.0.0.1:47615/acp/claude?token="+testToken {
		t.Fatalf("the clipboard got %q", clipboard)
	}
	if len(reported) != 0 {
		t.Errorf("the choice was reported to the daemon: %v (the token must stay between the daemon and the app)", reported)
	}
	if want := "Copied the address for Claude Code. Paste it into the agent URL setting of your editor or tool. It works like a password: don't share it."; a.settings.Notice != want {
		t.Errorf("notice %q, want %q", a.settings.Notice, want)
	}
	if strings.Contains(a.settings.Notice, testToken) {
		t.Error("the notice holds the token")
	}
}

func TestChoosingACopyWithNoAddressSaysSoAndCopiesNothing(t *testing.T) {
	for name, state := range map[string]State{"no endpoint": full(), "before the first state": {}} {
		var clipboard []string
		a := &app{report: func(string) {}, copy: func(text string) { clipboard = append(clipboard, text) }}
		a.settings = Settings{State: state, send: a.choose}
		a.choose("copy-acp:claude")
		if len(clipboard) != 0 {
			t.Errorf("%s: the clipboard got %q", name, clipboard)
		}
		if want := "agentd has no address to copy yet. Try again in a moment."; a.settings.Notice != want {
			t.Errorf("%s: notice %q, want %q", name, a.settings.Notice, want)
		}
	}
	// An agent the daemon did not say is ready has no address.
	var clipboard []string
	a := &app{report: func(string) {}, copy: func(text string) { clipboard = append(clipboard, text) }}
	a.settings = Settings{State: withAcp(), send: a.choose}
	for _, id := range []string{"copy-acp:codex", "copy-acp:../claude", "copy-acp:"} {
		a.choose(id)
	}
	if len(clipboard) != 0 {
		t.Errorf("an address was copied for an agent that is not ready: %q", clipboard)
	}
}
