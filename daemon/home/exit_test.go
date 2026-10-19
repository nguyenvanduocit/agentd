package home

import "testing"

func TestExitCodesAreTheProtocolWithTheService(t *testing.T) {
	if ExitStopped != 0 || ExitRestart != 75 {
		t.Fatalf("ExitStopped = %d, ExitRestart = %d; want 0 and 75", ExitStopped, ExitRestart)
	}
	if ClassifyExit(0) != MeaningStopped || ClassifyExit(75) != MeaningRestart {
		t.Error("0 stays stopped, 75 restarts now")
	}
	for _, crash := range []int{1, 2, 74, 76, 127, 137, 143, -1} {
		if got := ClassifyExit(crash); got != MeaningCrash {
			t.Errorf("ClassifyExit(%d) = %q, want crash", crash, got)
		}
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
		{"Restart exits 75 when a service restarts the daemon", ControlRestart, true, ControlOutcome{Exit: true, Code: 75}},
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
