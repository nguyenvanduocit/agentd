package proc

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLogger(t *testing.T) {
	var out, errOut bytes.Buffer
	l := NewLogger(&out, &errOut)
	l.now = func() time.Time { return time.Date(2026, 10, 19, 9, 8, 7, 6_000_000, time.UTC) }

	l.Say("plain")
	l.Warn("problem")
	l.StampLines()
	l.Say("stamped")
	l.Warn("stamped problem")

	if got, want := out.String(), "plain\n2026-10-19T09:08:07.006Z stamped\n"; got != want {
		t.Fatalf("stdout %q, want %q", got, want)
	}
	if got, want := errOut.String(), "problem\n2026-10-19T09:08:07.006Z stamped problem\n"; got != want {
		t.Fatalf("stderr %q, want %q", got, want)
	}
}

func TestCrashOnPanic(t *testing.T) {
	var out, errOut bytes.Buffer
	l := NewLogger(&out, &errOut)
	code := -1
	l.exit = func(c int) { code = c }
	tracker := NewTracker()

	func() {
		defer l.CrashOnPanic(tracker)
		panic("thrown from test")
	}()
	if code != 1 || !strings.Contains(errOut.String(), "fatal: uncaught exception: thrown from test\n") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}

	code = -1
	errOut.Reset()
	func() { defer l.CrashOnPanic(tracker) }()
	if code != -1 || errOut.Len() != 0 {
		t.Fatalf("a function that does not panic crashed: exit %d, stderr %q", code, errOut.String())
	}
}
