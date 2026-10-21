package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "run", "daemon.lock")
}

func TestTheLockFileIsInTheRunDirectoryOfTheHome(t *testing.T) {
	if got := LayoutOf("/h/.agentd").LockFile; got != "/h/.agentd/run/daemon.lock" {
		t.Errorf("LockFile = %q", got)
	}
}

func TestOnlyOneProcessOfAHomeHoldsTheLockAtATime(t *testing.T) {
	file := lockPath(t)
	first, err := AcquireInstanceLock(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Record(LockRecord{PID: 4242, StartedAt: time.Now().UTC(), Kind: KindApp}); err != nil {
		t.Fatal(err)
	}

	// A second open file description conflicts with the first, even inside one process: that is what makes two
	// daemons conflict. It fails at once: a lock that waited would leave the second daemon hanging, not stopped.
	type attempt struct {
		lock *InstanceLock
		err  error
	}
	got := make(chan attempt, 1)
	go func() {
		l, err := AcquireInstanceLock(file)
		got <- attempt{l, err}
	}()
	var second *InstanceLock
	select {
	case a := <-got:
		second, err = a.lock, a.err
	case <-time.After(3 * time.Second):
		t.Fatal("AcquireInstanceLock on a held lock did not return within 3 s: it waits instead of failing")
	}
	var held *HeldError
	if !errors.As(err, &held) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("the second AcquireInstanceLock = %v, want a *HeldError", err)
	}
	if second != nil {
		t.Error("a lock came back with the error")
	}
	if held.Holder.PID != 4242 || held.Holder.Kind != KindApp {
		t.Errorf("the holder the loser read = %+v, want pid 4242 started by the app", held.Holder)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireInstanceLock(file)
	if err != nil {
		t.Fatalf("after Close the lock is free, got %v", err)
	}
	_ = again.Close()
}

func TestAClosedLockCanBeClosedAgain(t *testing.T) {
	l, err := AcquireInstanceLock(lockPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("a second Close = %v, want nil", err)
	}
}

func TestTheLoserLeavesTheHoldersRecordAlone(t *testing.T) {
	file := lockPath(t)
	first, err := AcquireInstanceLock(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	want := LockRecord{PID: 77, StartedAt: time.Now().UTC().Truncate(time.Second), Kind: KindApp}
	if err := first.Record(want); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)

	if _, err := AcquireInstanceLock(file); err == nil {
		t.Fatal("the second lock was granted")
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(before) {
		t.Errorf("the loser changed the record: %q -> %q", before, after)
	}
	if now, _ := os.Stat(file); !now.ModTime().Equal(info.ModTime()) {
		t.Error("the loser touched the file")
	}
}

func TestTheLockFileAndItsDirectoryAreMadePrivate(t *testing.T) {
	file := lockPath(t)
	l, err := AcquireInstanceLock(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, %v; want mode 0600", st, err)
	}
	if st, err := os.Stat(filepath.Dir(file)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("run directory: %v, %v; want mode 0700", st, err)
	}
}

func TestADirectoryThatIsAlreadyThereIsLeftAsItIs(t *testing.T) {
	// A run/ made by something else keeps its permissions; the lock does not change them.
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := AcquireInstanceLock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o755 {
		t.Errorf("run directory mode = %v, want 0755 as it was", st.Mode().Perm())
	}
}

func TestALockInADirectoryThatCannotBeMadeIsAnErrorButNotHeld(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "run")
	if err := os.WriteFile(blocker, []byte("a file where the directory should be"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireInstanceLock(filepath.Join(blocker, "daemon.lock"))
	var held *HeldError
	if err == nil || errors.As(err, &held) {
		t.Fatalf("AcquireInstanceLock = %v, want an ordinary error that is not 'held'", err)
	}
}

func TestWhatKindOfProcessHoldsTheLock(t *testing.T) {
	for ui, want := range map[bool]string{false: KindTerminal, true: KindApp} {
		if got := InstanceKind(ui); got != want {
			t.Errorf("InstanceKind(ui=%v) = %q, want %q", ui, got, want)
		}
	}
}

func TestTheMessageOfAProcessThatCannotStart(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		holder LockRecord
		want   string
	}{
		{"app, minutes ago", LockRecord{PID: 4242, StartedAt: now.Add(-3 * time.Minute), Kind: KindApp},
			"agentd is already running on this computer (pid 4242, started by the app, 3 minutes ago): not starting a second one. Use that one, or quit it first."},
		{"app, a minute ago", LockRecord{PID: 9, StartedAt: now.Add(-61 * time.Second), Kind: KindApp},
			"agentd is already running on this computer (pid 9, started by the app, 1 minute ago): not starting a second one. Use that one, or quit it first."},
		{"terminal, hours ago", LockRecord{PID: 31, StartedAt: now.Add(-5 * time.Hour), Kind: KindTerminal},
			"agentd is already running on this computer (pid 31, started from a terminal, 5 hours ago): not starting a second one. Use that one, or quit it first."},
		{"just now", LockRecord{PID: 31, StartedAt: now.Add(-2 * time.Second), Kind: KindTerminal},
			"agentd is already running on this computer (pid 31, started from a terminal, just now): not starting a second one. Use that one, or quit it first."},
		{"days ago", LockRecord{PID: 31, StartedAt: now.Add(-72 * time.Hour), Kind: KindApp},
			"agentd is already running on this computer (pid 31, started by the app, 3 days ago): not starting a second one. Use that one, or quit it first."},
		{"a record nobody could read", LockRecord{},
			"agentd is already running on this computer: not starting a second one. Use that one, or quit it first."},
		{"a record with a kind this version does not know", LockRecord{PID: 5, StartedAt: now.Add(-10 * time.Minute), Kind: "robot"},
			"agentd is already running on this computer (pid 5, 10 minutes ago): not starting a second one. Use that one, or quit it first."},
	} {
		got := (&HeldError{Holder: c.holder}).Message(now)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("%s: the message is more than one line", c.name)
		}
	}
}
