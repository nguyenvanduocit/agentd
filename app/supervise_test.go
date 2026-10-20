package main

import (
	"strings"
	"testing"
	"time"
)

func TestARestartTheEngineAskedForStartsItAtOnce(t *testing.T) {
	next, crashes := AfterExit(75, time.Second, 3)
	if !next.Restart || next.Delay != 0 || next.Note != "" || crashes != 0 {
		t.Fatalf("after 75: %+v, %d crashes", next, crashes)
	}
}

func TestAStopLeavesTheEngineStoppedForTheUserToStart(t *testing.T) {
	next, _ := AfterExit(0, time.Hour, 0)
	if next.Restart || next.Note == "" {
		t.Fatalf("after 0: %+v", next)
	}
}

func TestACrashRestartsAfterAGrowingDelay(t *testing.T) {
	var delays []time.Duration
	crashes := 0
	for range 3 {
		var next Next
		next, crashes = AfterExit(1, time.Second, crashes)
		if !next.Restart {
			t.Fatalf("crash %d: %+v", crashes, next)
		}
		delays = append(delays, next.Delay)
	}
	if !(delays[0] > 0 && delays[0] < delays[1] && delays[1] < delays[2]) {
		t.Fatalf("delays %v do not grow", delays)
	}
}

func TestAnEngineThatKeepsCrashingQuicklyIsLeftStopped(t *testing.T) {
	crashes := 0
	var next Next
	for range maxCrashes {
		next, crashes = AfterExit(-1, time.Second, crashes)
	}
	if next.Restart || !strings.Contains(next.Note, "log") {
		t.Fatalf("after %d quick crashes: %+v", crashes, next)
	}
}

func TestACrashAfterALongRunStartsTheCountAgain(t *testing.T) {
	_, crashes := AfterExit(1, time.Second, maxCrashes-1)
	if crashes != maxCrashes {
		t.Fatalf("quick crash: %d", crashes)
	}
	next, crashes := AfterExit(1, time.Hour, maxCrashes-1)
	if !next.Restart || crashes != 1 {
		t.Fatalf("crash after an hour: %+v, %d", next, crashes)
	}
}

// An engine that stops at once with the code that means "stay stopped" did not stop because someone asked: it found
// another agentd on this computer (daemon/run takes a lock per home) and left. The window says that, not "stopped".
func TestAnEngineThatLeavesAtOnceWithZeroFoundAnotherAgentd(t *testing.T) {
	const want = "agentd did not start here: another agentd is already running on this computer. Open its menu-bar icon, or quit it first."
	for _, ranFor := range []time.Duration{0, 200 * time.Millisecond, quickStop - time.Millisecond} {
		next, crashes := AfterExit(0, ranFor, 2)
		if next.Restart || next.Note != want || crashes != 0 {
			t.Errorf("AfterExit(0, %v, 2) = %+v, %d; want no restart, the note %q and the crash count reset", ranFor, next, crashes, want)
		}
	}
}

func TestAnEngineThatWasAskedToStopAfterAWhileJustStopped(t *testing.T) {
	for _, ranFor := range []time.Duration{quickStop, 10 * time.Second, time.Hour} {
		next, _ := AfterExit(0, ranFor, 0)
		if next.Restart || next.Note != "agentd was stopped" {
			t.Errorf("AfterExit(0, %v) = %+v, want the plain stop", ranFor, next)
		}
	}
}

func TestTheLockNoteDoesNotChangeWhatTheOtherExitCodesMean(t *testing.T) {
	if next, _ := AfterExit(75, 100*time.Millisecond, 3); !next.Restart || next.Delay != 0 {
		t.Errorf("a quick 75 = %+v, want an immediate restart", next)
	}
	if next, crashes := AfterExit(1, 100*time.Millisecond, 0); !next.Restart || crashes != 1 {
		t.Errorf("a quick crash = %+v, %d, want a restart and a count of 1", next, crashes)
	}
}
