//go:build unix

package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func fcntlGetFD(fd int) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(flags), nil
}

// A program the daemon starts (an agent) must not inherit the lock: the lock belongs to the daemon alone, and
// an agent that outlives a killed daemon would otherwise keep every later daemon out.
func TestTheLockDescriptorIsNotInheritedByWhatTheDaemonStarts(t *testing.T) {
	l, err := AcquireInstanceLock(filepath.Join(t.TempDir(), "run", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	rc, err := l.f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var ferr error
	if err := rc.Control(func(fd uintptr) { flags, ferr = fcntlGetFD(int(fd)) }); err != nil || ferr != nil {
		t.Fatalf("F_GETFD: %v, %v", err, ferr)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Error("the lock descriptor is not close-on-exec: an agent would inherit it")
	}
}

// And the proof that matters: a child that is still running does not keep the lock when its parent is gone.
func TestAChildThatOutlivesTheLockHolderDoesNotKeepTheLock(t *testing.T) {
	file := filepath.Join(t.TempDir(), "run", "daemon.lock")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	// The holder is this process; the child is started while it holds the lock, so a descriptor it inherited would keep
	// the lock alive after the holder's own is closed.
	l, err := AcquireInstanceLock(file)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		_ = l.Close()
		t.Skip("cannot start sleep:", err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	// Close stands for the holder dying: the descriptor is gone and only the child is left.
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		again, err := AcquireInstanceLock(file)
		if err == nil {
			_ = again.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lock is still held after its holder closed it, with a child running: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
