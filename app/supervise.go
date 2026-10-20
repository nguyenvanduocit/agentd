package main

import (
	"fmt"
	"time"
)

// Exit codes are the engine's protocol with whatever runs it (daemon/home/exit.go): 0 stays stopped (a Stop from an
// app's server), 75 asks to be started again (a Restart, an update), anything else is a crash.
const (
	exitStopped = 0
	exitRestart = 75
	// An engine that stops with 0 sooner than this after it started was not stopped by anyone: it found another agentd on
	// the home (the engine takes a lock per home, daemon/run/instance.go) and left, as it is told to, with 0.
	quickStop = 3 * time.Second
	// An engine that ran this long before it crashed was working: its crash starts the count again.
	quickRun   = 10 * time.Second
	maxCrashes = 5
	maxDelay   = 30 * time.Second
	// On the first launch the app has just ended the launchd job of a terminal install (TakeOverCLIService): launchctl
	// returns before that engine has exited, so the new engine can find the lock of the home held for a moment and leave
	// at once with 0, when nothing else will be running a second later. The app starts it again after quickStopWait, up
	// to maxQuickStops times in a row, before it tells the person that another agentd is running.
	maxQuickStops = 2
	quickStopWait = 1500 * time.Millisecond
)

// leftAtOnce is whether an engine that exited with code after ranFor was not stopped by anyone: it found another agentd
// on the home and left, as it is told to, with 0.
func leftAtOnce(code int, ranFor time.Duration) bool {
	return code == exitStopped && ranFor < quickStop
}

// Next is what the app does after its engine exits.
type Next struct {
	Restart bool
	Delay   time.Duration
	// Note says why the engine does not run, for the window and the menu; empty when it starts again.
	Note string
}

// AfterExit decides what follows an exit of the engine that ran for ranFor, after `crashes` quick crashes in a row:
// a restart it asked for at once, a stop left for the user to start again, a crash started again after a delay that
// doubles, until maxCrashes quick crashes leave it stopped. It returns the crashes counted now.
func AfterExit(code int, ranFor time.Duration, crashes int) (Next, int) {
	switch code {
	case exitRestart:
		return Next{Restart: true}, 0
	case exitStopped:
		if leftAtOnce(code, ranFor) {
			return Next{Note: "agentd did not start here: another agentd is already running on this computer. Open its menu-bar icon, or quit it first."}, 0
		}
		return Next{Note: "agentd was stopped"}, 0
	}
	if ranFor >= quickRun {
		crashes = 0
	}
	crashes++
	if crashes >= maxCrashes {
		return Next{Note: fmt.Sprintf("agentd crashed %d times in a row: see the log", crashes)}, crashes
	}
	delay := time.Second << (crashes - 1)
	return Next{Restart: true, Delay: min(delay, maxDelay)}, crashes
}
