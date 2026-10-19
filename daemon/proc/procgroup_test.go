//go:build !windows

package proc

import (
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

type call struct {
	pid int
	sig syscall.Signal
}

func TestSignalGroup(t *testing.T) {
	var calls []call
	kill := func(pid int, sig syscall.Signal) error {
		calls = append(calls, call{pid, sig})
		return nil
	}
	for _, pgid := range []int{-1, 0, 1} {
		signalGroup(kill, pgid, true)
	}
	if len(calls) != 0 {
		t.Fatalf("a pid that leads no group of its own was signalled: %+v", calls)
	}
	signalGroup(kill, 4242, false)
	signalGroup(kill, 4243, true)
	want := []call{{-4242, syscall.SIGTERM}, {-4243, syscall.SIGKILL}}
	if !slices.Equal(calls, want) {
		t.Fatalf("got %+v, want %+v", calls, want)
	}
}

// A child started with Detach leads its own group, and the tracker ends it together with what it started.
func TestDetachAndTracker(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & wait")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || pgid != cmd.Process.Pid {
		t.Fatalf("pgid %d, err %v, want the child's own pid %d", pgid, err, cmd.Process.Pid)
	}
	tracker := NewTracker()
	tracker.Track(pgid)
	tracker.Track(1 << 30) // a group that is already gone is not an error
	tracker.Untrack(1 << 30)
	tracker.KillAll()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the group is still running after KillAll")
	}
	if err := syscall.Kill(-pgid, 0); err == nil {
		t.Fatal("a member of the group is still alive")
	}
}
