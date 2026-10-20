//go:build !windows

package main

import (
	"testing"
	"time"
)

// standIn is an app whose engine is a counter: launching it runs nothing.
func standIn() (*desktop, *int) {
	launched := new(int)
	d := &desktop{files: EngineFiles{Engine: "/engine"}}
	d.launch = func(EngineFiles, string, string, func([]byte)) (*Engine, error) {
		*launched++
		return &Engine{done: make(chan struct{})}, nil
	}
	return d, launched
}

// The wait after a crash and the Start the person chooses both end in start(); the second to arrive must not run a
// second engine on the same home, which would leave the first with nobody to end it.
func TestStartingWhileAnEngineRunsStartsNoSecondOne(t *testing.T) {
	d, launched := standIn()
	d.start()
	d.start()
	if *launched != 1 {
		t.Fatalf("launched %d engines, want 1", *launched)
	}
}

func TestStartByHandWhileARestartWaitsStartsOnceAndEndsTheWait(t *testing.T) {
	d, launched := standIn()
	fired := make(chan struct{})
	d.restart = time.AfterFunc(50*time.Millisecond, func() { close(fired) })

	d.handle(StartID)

	select {
	case <-fired:
		t.Fatal("the restart that was waiting still ran")
	case <-time.After(200 * time.Millisecond):
	}
	if *launched != 1 || d.restart != nil {
		t.Fatalf("launched %d engines, restart %v; want 1 and no wait left", *launched, d.restart)
	}
}

func TestAStartThatFollowsTheWaitFindsTheEngineAlreadyRunning(t *testing.T) {
	d, launched := standIn()
	d.handle(StartID) // the person chose Start while the app counted down
	d.start()         // the countdown ran out
	if *launched != 1 {
		t.Fatalf("launched %d engines, want 1", *launched)
	}
}
