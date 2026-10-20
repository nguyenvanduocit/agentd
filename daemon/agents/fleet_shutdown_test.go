package agents

import (
	"syscall"
	"testing"
	"time"
)

func groupRuns(pgid int) bool { return syscall.Kill(-pgid, 0) == nil }

// ignoresTerm is an agent that survives SIGTERM, as do its children.
const ignoresTerm = `sh -c 'trap "" TERM; echo up; while :; do sleep 1; done'`

// Shutdown promises that the stragglers are SIGKILLed. The kill timer of a session fires 2 s after its SIGTERM; a
// daemon that exits before that leaves the agent running, so Shutdown does it itself once its grace is over.
func TestShutdownKillsAnAgentThatIgnoresTermBeforeItReturns(t *testing.T) {
	f, sink, _, _ := seamFleet(t, ignoresTerm)
	opened, err := f.Open("s1", "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-opened.PID, syscall.SIGKILL) })
	waitFor(t, "the agent to be up", 5*time.Second, func() bool { return len(sink.data(FrameOut)) > 0 })

	start := time.Now()
	f.Shutdown(300 * time.Millisecond)
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("Shutdown(300ms) took %v", took)
	}
	// The kill timer (termGrace) is 2 s away: a group that is gone within 1 s was killed by Shutdown.
	waitFor(t, "the agent's group to be killed by Shutdown, not by a timer 2 s later", time.Second, func() bool { return !groupRuns(opened.PID) })
}

func TestDrainRefusesNewOpensAndLeavesTheRunningAgentsAlone(t *testing.T) {
	f, _, _, _ := seamFleet(t, "cat")
	if _, err := f.Open("s1", "claude"); err != nil {
		t.Fatal(err)
	}
	f.Drain()
	if _, err := f.Open("s2", "claude"); err == nil || err.Error() != "this daemon is stopping" {
		t.Errorf("an open after Drain: %v", err)
	}
	if f.Size() != 1 || !f.Write("s1", "still here") {
		t.Errorf("Drain touched the agent that ran: size %d", f.Size())
	}
}
