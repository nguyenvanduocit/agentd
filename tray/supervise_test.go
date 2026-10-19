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
