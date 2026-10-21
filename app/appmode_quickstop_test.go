//go:build !windows

package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// scripted is an app whose engines are the given ones, in order. Its waits are recorded and run by hand, as the main
// thread would run them, so no test sleeps.
type scripted struct {
	d        *desktop
	engines  []*Engine
	launched int
	waits    []time.Duration
	due      func() // what the wait that is pending runs when it is over
}

func newScripted(engines ...*Engine) *scripted {
	s := &scripted{engines: engines}
	s.d = &desktop{}
	s.d.launch = func(EngineFiles, string, string, OpenLink) (*Engine, error) {
		e := s.engines[s.launched]
		s.launched++
		return e, nil
	}
	s.d.after = func(delay time.Duration, f func()) *time.Timer {
		s.waits = append(s.waits, delay)
		s.due = f
		return time.NewTimer(time.Hour)
	}
	return s
}

// over ends the wait that is pending, as the main thread would: it fails the test when none is.
func (s *scripted) over(t *testing.T) {
	t.Helper()
	if s.due == nil {
		t.Fatal("no wait is pending")
	}
	f := s.due
	s.due = nil
	f()
}

// leftAfter is an engine that exited with code after it ran for ranFor; the test tells the app so with ended.
func leftAfter(code int, ranFor time.Duration) *Engine {
	return &Engine{done: make(chan struct{}), code: code, started: time.Now().Add(-ranFor)}
}

// up is an engine that keeps running.
func up() *Engine { return &Engine{done: make(chan struct{}), started: time.Now()} }

// The note a person reads when another agentd really is running (AfterExit's, once the tries are spent).
var otherAgentdNote = func() string { next, _ := AfterExit(0, 0, 0); return next.Note }()

// On the first launch the app has just ended the launchd job of a terminal install; launchctl returns before that engine
// has exited, so the new engine can find the lock of the home held and leave at once with 0 although nothing else will
// run a second later. The app tries again before it says another agentd is running.
func TestAnEngineThatLeavesAtOnceWithZeroIsStartedAgainBeforeAnythingIsSaid(t *testing.T) {
	first, second := leftAfter(0, 100*time.Millisecond), up()
	s := newScripted(first, second)
	s.d.start()
	s.d.ended(first)

	if len(s.waits) != 1 || s.waits[0] != quickStopWait || s.d.restart == nil {
		t.Fatalf("waits %v, restart %v; want one wait of %v", s.waits, s.d.restart, quickStopWait)
	}
	if note := s.d.settings.Host.Note; note == otherAgentdNote || s.d.settings.Host.Running {
		t.Fatalf("while it waits: running %v, note %q; the person must not be told another agentd runs", s.d.settings.Host.Running, note)
	}

	s.over(t) // the wait is over
	if s.launched != 2 || s.d.engine != second || !s.d.settings.Host.Running || s.d.settings.Host.Note != "" {
		t.Fatalf("launched %d, running %v, note %q; want the second engine up and no note", s.launched, s.d.settings.Host.Running, s.d.settings.Host.Note)
	}
}

// Another agentd that stays is still told to the person, after the last try and not before, and the tries are bounded.
func TestTheTriesAreBoundedAndThenAnotherAgentdIsSaid(t *testing.T) {
	engines := make([]*Engine, 0, maxQuickStops+1)
	for range maxQuickStops + 1 {
		engines = append(engines, leftAfter(0, 100*time.Millisecond))
	}
	s := newScripted(engines...)
	s.d.start()
	for i := range maxQuickStops {
		s.d.ended(engines[i])
		if len(s.waits) != i+1 {
			t.Fatalf("after quick exit %d: waits %v, want %d", i+1, s.waits, i+1)
		}
		s.over(t)
	}
	s.d.ended(engines[maxQuickStops]) // the last try also found the lock held

	if len(s.waits) != maxQuickStops || s.launched != maxQuickStops+1 {
		t.Fatalf("waits %v, launched %d; want %d waits and %d launches", s.waits, s.launched, maxQuickStops, maxQuickStops+1)
	}
	if s.d.restart != nil || s.d.settings.Host.Running || s.d.settings.Host.Note != otherAgentdNote {
		t.Fatalf("restart %v, running %v, note %q; want it stopped with the note %q", s.d.restart, s.d.settings.Host.Running, s.d.settings.Host.Note, otherAgentdNote)
	}
}

// An exit 0 after a while is a Stop someone asked for (an app's server, the menu): it is not tried again.
func TestAStopAfterAWhileIsNotTriedAgain(t *testing.T) {
	stopped := leftAfter(0, time.Hour)
	s := newScripted(stopped)
	s.d.start()
	s.d.ended(stopped)

	if len(s.waits) != 0 || s.launched != 1 || s.d.restart != nil || s.d.settings.Host.Note != "agentd was stopped" {
		t.Fatalf("waits %v, launched %d, note %q; want the plain stop", s.waits, s.launched, s.d.settings.Host.Note)
	}
}

// A quick crash is not the lock: it keeps its own delay and its own count.
func TestAQuickCrashKeepsItsOwnHandling(t *testing.T) {
	crashed := leftAfter(1, 100*time.Millisecond)
	s := newScripted(crashed)
	s.d.start()
	s.d.ended(crashed)

	if len(s.waits) != 1 || s.waits[0] != time.Second || s.d.crashes != 1 || s.d.quickStops != 0 {
		t.Fatalf("waits %v, crashes %d, quick stops %d; want the crash delay of 1s, one crash and no quick stop", s.waits, s.d.crashes, s.d.quickStops)
	}
}

// Quit with nothing running is not held, and it ends the wait: no engine may start behind it.
func TestQuitDuringTheWaitIsNotHeldAndEndsTheWait(t *testing.T) {
	first := leftAfter(0, 100*time.Millisecond)
	s := newScripted(first)
	s.d.start()
	s.d.ended(first)

	q := &mygo.QuitEvent{}
	s.d.beforeQuit(q)

	if q.DefaultPrevented() {
		t.Fatal("the quit was held although no engine runs")
	}
	if s.d.restart != nil {
		t.Fatal("the wait is still pending after Quit")
	}
}

// Start, chosen during the wait, starts one engine and ends the wait; the wait's own start, if it was already on its way,
// finds the engine running.
func TestStartByHandDuringTheWaitStartsOnceAndTheLateWaitDoesNothing(t *testing.T) {
	first, second := leftAfter(0, 100*time.Millisecond), up()
	s := newScripted(first, second)
	s.d.start()
	s.d.ended(first)
	if s.due == nil {
		t.Fatal("no wait is pending")
	}
	late := s.due

	s.d.handle(StartID)
	if s.launched != 2 || s.d.restart != nil || s.d.quickStops != 0 {
		t.Fatalf("launched %d, restart %v, quick stops %d; want 2, no wait and a fresh count", s.launched, s.d.restart, s.d.quickStops)
	}
	late()
	if s.launched != 2 {
		t.Fatalf("the late wait launched %d engines in all, want 2", s.launched)
	}
}

// The count is of quick stops in a row: an engine that ran, or crashed, or was restarted, gives the next one its tries.
func TestAnEngineThatRanGivesTheNextQuickStopItsTriesBack(t *testing.T) {
	a, b := leftAfter(0, 100*time.Millisecond), leftAfter(0, 100*time.Millisecond)
	c := leftAfter(exitRestart, time.Hour) // ran for a long time, then asked to be started again
	d := leftAfter(0, 100*time.Millisecond)
	s := newScripted(a, b, c, d, up())
	s.d.start()
	s.d.ended(a)
	s.over(t)
	s.d.ended(b)
	s.over(t)
	s.d.ended(c) // the restart it asked for
	s.over(t)
	s.d.ended(d) // a quick stop again: it gets a try

	want := []time.Duration{quickStopWait, quickStopWait, 0, quickStopWait}
	if len(s.waits) != len(want) {
		t.Fatalf("waits %v, want %v", s.waits, want)
	}
	for i := range want {
		if s.waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", s.waits, want)
		}
	}
	if s.d.settings.Host.Note == otherAgentdNote {
		t.Fatal("the person was told another agentd runs although the tries were back")
	}
}
