//go:build !windows

package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeTray is a stand-in for the tray program: it records its pid and every argument list and line it gets, and prints
// `reply` after the first line. tail is what it does then (the default keeps reading until its input closes).
type fakeTray struct {
	file, lines, pid, starts string
}

func newFakeTray(t *testing.T, reply []string, tail string) *fakeTray {
	t.Helper()
	dir := t.TempDir()
	f := &fakeTray{file: filepath.Join(dir, "tray"), lines: filepath.Join(dir, "lines"), pid: filepath.Join(dir, "pid"), starts: filepath.Join(dir, "starts")}
	if tail == "" {
		tail = "cat >> $LINES"
	}
	var echoes strings.Builder
	for _, r := range reply {
		echoes.WriteString("echo '" + r + "'\n")
	}
	script := "#!/bin/sh\nLINES=" + f.lines + "\necho \"$*\" >> " + f.starts + "\necho $$ > " + f.pid + "\nread first\necho \"$first\" >> $LINES\n" + echoes.String() + tail + "\n"
	if err := os.WriteFile(f.file, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeTray) stateLines() []string {
	b, err := os.ReadFile(f.lines)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeTray) pidNow() int {
	b, err := os.ReadFile(f.pid)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (f *fakeTray) startCount() int {
	b, err := os.ReadFile(f.starts)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// fastTray is a Tray with the timings of a test.
func fastTray(file string, onAction func(Action)) (*Tray, *safeBuf) {
	log, out := testLog()
	tr := NewTray(file, onAction, log)
	tr.quickWindow, tr.restartDelay, tr.endGrace = 30*time.Second, 10*time.Millisecond, 100*time.Millisecond
	return tr, out
}

func TestTrayGetsTheStateOnItsStdinAndIsStartedAsChild(t *testing.T) {
	f := newFakeTray(t, nil, "")
	tr, _ := fastTray(f.file, func(Action) {})
	defer tr.Stop()
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the first state", 3*time.Second, func() bool { return len(f.stateLines()) >= 1 })
	if got := f.stateLines()[0]; got != NewState(baseFacts).line() {
		t.Fatalf("got %s", got)
	}
	if b, _ := os.ReadFile(f.starts); strings.TrimSpace(string(b)) != "child" {
		t.Fatalf("started with %q, want the argument child", b)
	}
}

func TestTrayGetsANewStateWhenSomethingChangesAndOnlyThen(t *testing.T) {
	f := newFakeTray(t, nil, "")
	tr, _ := fastTray(f.file, func(Action) {})
	defer tr.Stop()
	tr.Start()
	tr.Update(NewState(baseFacts))
	waitFor(t, "the first state", 3*time.Second, func() bool { return len(f.stateLines()) >= 1 })
	tr.Update(NewState(baseFacts))
	tr.Update(NewState(baseFacts))
	changed := baseFacts
	changed.Browsers = []string{page}
	tr.Update(NewState(changed))
	waitFor(t, "the state with the page", 3*time.Second, func() bool { return len(f.stateLines()) >= 2 })
	time.Sleep(50 * time.Millisecond)
	if n := len(f.stateLines()); n != 2 {
		t.Fatalf("the tray got %d lines: %v", n, f.stateLines())
	}
}

func TestTrayReportsTheChoicesOfItsMenuAndIgnoresTheRest(t *testing.T) {
	f := newFakeTray(t, []string{`{"t":"action","id":"format-disk"}`, "not json", `{"t":"action","id":"stop"}`}, "")
	actions := make(chan Action, 4)
	tr, _ := fastTray(f.file, func(a Action) { actions <- a })
	defer tr.Stop()
	tr.Update(NewState(baseFacts))
	tr.Start()
	select {
	case a := <-actions:
		if a.Kind != ActionStop {
			t.Fatalf("got %+v, want stop", a)
		}
	case <-time.After(minWait):
		t.Fatal("no action")
	}
	select {
	case a := <-actions:
		t.Fatalf("an unknown line became %+v", a)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTrayPassesWhatTheProgramSaysOnStderrToTheLog(t *testing.T) {
	f := newFakeTray(t, []string{}, "echo 'no display here' >&2\ncat >> $LINES")
	tr, out := fastTray(f.file, func(Action) {})
	defer tr.Stop()
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the stderr line", 3*time.Second, func() bool { return strings.Contains(out.String(), "tray: no display here\n") })
}

func TestTrayEndsWithTheDaemonItsInputCloses(t *testing.T) {
	f := newFakeTray(t, nil, "")
	tr, _ := fastTray(f.file, func(Action) {})
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the tray pid", 3*time.Second, func() bool { return f.pidNow() != 0 })
	pid := f.pidNow()
	tr.Stop()
	waitFor(t, "the tray to end", 3*time.Second, func() bool { return !alive(pid) })
}

func TestTrayIsKilledWhenItDoesNotEndWithItsInput(t *testing.T) {
	f := newFakeTray(t, nil, "trap '' HUP\nwhile true; do sleep 0.05; done") // ignores the end of its input
	tr, _ := fastTray(f.file, func(Action) {})
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the tray pid", 3*time.Second, func() bool { return f.pidNow() != 0 })
	pid := f.pidNow()
	tr.Stop()
	waitFor(t, "the tray to be killed", 3*time.Second, func() bool { return !alive(pid) })
}

func TestTrayThatCannotStartIsGivenUpAfterAFewTries(t *testing.T) {
	f := newFakeTray(t, nil, "exit 1")
	tr, out := fastTray(f.file, func(Action) {})
	defer tr.Stop()
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the give-up line", 5*time.Second, func() bool { return strings.Contains(out.String(), "tray: giving up after 3 quick exits") })
	time.Sleep(100 * time.Millisecond)
	if n := f.startCount(); n != maxQuickExits {
		t.Fatalf("started %d times, want %d", n, maxQuickExits)
	}
}

func TestTrayThatIsNotThereIsTriedAndGivenUp(t *testing.T) {
	tr, out := fastTray(filepath.Join(t.TempDir(), "missing"), func(Action) {})
	defer tr.Stop()
	tr.Start()
	waitFor(t, "the give-up line", 5*time.Second, func() bool { return strings.Contains(out.String(), "tray: giving up") })
	if !strings.Contains(out.String(), "tray: cannot start ") {
		t.Fatalf("no line that it cannot start: %s", out.String())
	}
}

func TestTrayThatRanForAWhileIsStartedAnewAndGetsTheStateAgain(t *testing.T) {
	// It ends after the first line, but not at once: that is a crash, not a missing display.
	f := newFakeTray(t, nil, "sleep 0.5")
	tr, _ := fastTray(f.file, func(Action) {})
	tr.quickWindow = 50 * time.Millisecond
	defer tr.Stop()
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the second start with the state again", 5*time.Second, func() bool { return f.startCount() >= 2 && len(f.stateLines()) >= 2 })
	if got := f.stateLines(); got[0] != got[1] {
		t.Fatalf("the new tray got another state: %v", got)
	}
}

func TestTrayStoppedIsNotStartedAgain(t *testing.T) {
	f := newFakeTray(t, nil, "exit 1")
	tr, _ := fastTray(f.file, func(Action) {})
	tr.Update(NewState(baseFacts))
	tr.Start()
	waitFor(t, "the first start", 3*time.Second, func() bool { return f.startCount() >= 1 })
	tr.Stop()
	n := f.startCount()
	time.Sleep(150 * time.Millisecond)
	if f.startCount() > n+1 { // one start may have been under way when Stop came
		t.Fatalf("started again after Stop: %d -> %d", n, f.startCount())
	}
}

// A tray program that shows the consent window answers on its stdout: the relay of the Tray hears it.
func TestTrayAsksThroughTheProgramAndHearsTheAnswer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tray")
	script := `#!/bin/sh
while read line; do
  case "$line" in
    *'"t":"consent"'*)
      id=$(printf '%s' "$line" | sed 's/.*"id":"\([0-9a-f]*\)".*/\1/')
      echo '{"t":"consent-shown","id":"'$id'"}'
      echo '{"t":"consent-answer","id":"'$id'","answer":"allow","folders":["/work/a","/work/b"]}'
      ;;
  esac
done
`
	if err := os.WriteFile(file, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log, _ := testLog()
	tr := NewTray(file, func(Action) {}, log)
	tr.Start()
	t.Cleanup(tr.Stop)
	got, asked := tr.Ask(question, 10)
	if !asked || got.Answer != "allow" || len(got.Folders) != 2 || got.Folders[0] != "/work/a" {
		t.Fatalf("decision = %+v, asked = %v", got, asked)
	}
}

func TestTrayWithoutAProgramRunningIsNotAsked(t *testing.T) {
	log, _ := testLog()
	tr := NewTray("/nonexistent/tray", func(Action) {}, log)
	if _, asked := tr.Ask(question, 10); asked {
		t.Fatal("asked")
	}
}
