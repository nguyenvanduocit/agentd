package ui

import (
	"strings"
	"testing"
	"time"
)

const acpToken = "tokentokentokentokentokentokentokentokento"

var acpState = AcpState{Base: "ws://127.0.0.1:47615/acp/", Token: acpToken, Agents: []string{"claude", "codex"}}

// The address of the standard ACP endpoint, with its token, goes to the app and the tray on the private channel they
// share with the daemon; it is in the state only while there is an endpoint to reach.
func TestTheStateCarriesTheAcpAddressOnlyWhenThereIsOne(t *testing.T) {
	if line := NewState(Facts{Version: "1"}).line(); strings.Contains(line, `"acp"`) {
		t.Errorf("a state with no address has an acp field: %s", line)
	}
	line := NewState(Facts{Version: "1", Acp: &acpState}).line()
	want := `"acp":{"base":"ws://127.0.0.1:47615/acp/","token":"` + acpToken + `","agents":["claude","codex"]}`
	if !strings.HasSuffix(line, want+"}") {
		t.Errorf("the state line is %s, want it to end with %s", line, want)
	}
}

func TestMenuSendsTheAcpAddressWhenThereIsOneAndNothingWhenThereIsNot(t *testing.T) {
	f := newMenu(t)
	ok := true
	f.deps.Acp = func() (AcpState, bool) { f.mu.Lock(); defer f.mu.Unlock(); return acpState, ok }
	f.start(t)
	waitUntil(t, "the state with the address", func() bool { s := f.link.last(t); return s.Acp != nil })
	got := f.link.last(t).Acp
	if got == nil || got.Base != acpState.Base || got.Token != acpToken || strings.Join(got.Agents, ",") != "claude,codex" {
		t.Fatalf("the link got %+v", got)
	}

	f.mu.Lock()
	ok = false
	f.mu.Unlock()
	waitUntil(t, "the state without the address", func() bool { return f.link.last(t).Acp == nil })
}

func TestMenuWithNoAcpSourceSendsNoAddress(t *testing.T) {
	f := newMenu(t)
	f.start(t)
	time.Sleep(60 * time.Millisecond)
	if s := f.link.last(t); s.Acp != nil {
		t.Errorf("the state has an address though the daemon has no endpoint: %+v", s.Acp)
	}
}

// Neither the token nor the address is ever written to the log.
func TestMenuNeverLogsTheAcpToken(t *testing.T) {
	f := newMenu(t)
	f.deps.Acp = func() (AcpState, bool) { return acpState, true }
	f.start(t)
	waitUntil(t, "the state with the address", func() bool { return f.link.last(t).Acp != nil })
	if out := f.log.String(); strings.Contains(out, acpToken) || strings.Contains(out, "token") {
		t.Errorf("the log holds the token: %s", out)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
