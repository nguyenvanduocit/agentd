package home

import "testing"

func TestExitCodesAreTheProtocolWithTheApp(t *testing.T) {
	if ExitStopped != 0 || ExitRestart != 75 {
		t.Fatalf("ExitStopped = %d, ExitRestart = %d; want 0 and 75", ExitStopped, ExitRestart)
	}
}

func TestControlOutcomeOf(t *testing.T) {
	for _, tc := range []struct {
		name    string
		c       Control
		managed bool
		want    ControlOutcome
	}{
		{"Stop exits 0 when managed", ControlStop, true, ControlOutcome{Exit: true, Code: 0}},
		{"Stop exits 0 when not managed", ControlStop, false, ControlOutcome{Exit: true, Code: 0}},
		{"Restart exits 75 when the app starts the daemon again", ControlRestart, true, ControlOutcome{Exit: true, Code: 75}},
		{"Restart is ignored when none does", ControlRestart, false, ControlOutcome{Why: "not managed: restart ignored"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ControlOutcomeOf(tc.c, tc.managed); got != tc.want {
				t.Errorf("ControlOutcomeOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIsManagedIsOnlyAgentdManagedOne(t *testing.T) {
	if !IsManaged(Env{"AGENTD_MANAGED": "1"}) {
		t.Error("AGENTD_MANAGED=1 is managed")
	}
	if IsManaged(Env{"AGENTD_MANAGED": "0"}) || IsManaged(Env{}) {
		t.Error("anything else is not")
	}
}
