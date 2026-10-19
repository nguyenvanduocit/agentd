//go:build !windows

package tray

import (
	"testing"
	"time"
)

// The daemon as a CLI install runs it: the menu starts the tray program, and its choices come back on its stdout.

func startWithTray(t *testing.T, f *menuFixture, tray *fakeTray) {
	t.Helper()
	f.deps.Open = func(onAction func(Action)) Link {
		tr, _ := fastTray(tray.file, onAction)
		tr.log = f.deps.Log
		return tr
	}
	f.start(t)
}

func TestTrayProgramGetsTheStateOfTheMenu(t *testing.T) {
	f := newMenu(t)
	writeRegistry(t, f.file, []string{"https://a.example"}, nil)
	f.linked["https://a.example"] = true
	tray := newFakeTray(t, nil, "")
	startWithTray(t, f, tray)
	waitFor(t, "the first state", 3*time.Second, func() bool { return len(tray.stateLines()) >= 1 })
	s, ok := parseState(tray.stateLines()[0])
	if !ok || s.Version != "0.0.0-test" || len(s.Apps) != 1 || !s.Managed || s.GatewayOff || s.Log || !s.Updates {
		t.Fatalf("got %+v", s)
	}
}

func TestTrayProgramStopInItsMenuStopsTheDaemonWith0(t *testing.T) {
	f := newMenu(t)
	tray := newFakeTray(t, []string{`{"t":"action","id":"stop"}`}, "")
	startWithTray(t, f, tray)
	select {
	case code := <-f.stopped:
		if code != 0 {
			t.Fatalf("stopped with %d", code)
		}
	case <-time.After(minWait):
		t.Fatal("not stopped")
	}
}

func TestTrayProgramUpdatesSwitchOfItsWindowShowsTheSetting(t *testing.T) {
	f := newMenu(t)
	tray := newFakeTray(t, []string{`{"t":"action","id":"updates:off"}`}, "")
	startWithTray(t, f, tray)
	waitFor(t, "a state with updates off", 3*time.Second, func() bool {
		for _, l := range tray.stateLines() {
			if s, ok := parseState(l); ok && !s.Updates {
				return true
			}
		}
		return false
	})
}

func TestTrayProgramActionItDoesNotKnowDoesNothing(t *testing.T) {
	f := newMenu(t)
	tray := newFakeTray(t, []string{`{"t":"action","id":"format-disk"}`, "not json"}, "")
	startWithTray(t, f, tray)
	time.Sleep(300 * time.Millisecond)
	if len(f.stopped) != 0 {
		t.Fatal("the daemon was stopped")
	}
}

func TestTrayProgramEndsWithTheDaemon(t *testing.T) {
	f := newMenu(t)
	tray := newFakeTray(t, nil, "")
	f.deps.Open = func(onAction func(Action)) Link {
		tr, _ := fastTray(tray.file, onAction)
		return tr
	}
	end := StartMenu(f.deps)
	waitFor(t, "the tray pid", 3*time.Second, func() bool { return tray.pidNow() != 0 })
	pid := tray.pidNow()
	end()
	waitFor(t, "the tray to end", 3*time.Second, func() bool { return !alive(pid) })
}
