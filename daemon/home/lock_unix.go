//go:build unix

package home

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireInstanceLock takes the lock of a home: file is its LockFile. It never waits: a lock that is held is a
// *HeldError carrying what the holder wrote, any other failure (no permission, a file system without locks) is an
// ordinary error. The file is opened close-on-exec (Go's default), so no agent the daemon starts can
// inherit the lock and keep a later daemon out after this one is gone; and it is not truncated, so the record of the
// holder survives a failed attempt.
func AcquireInstanceLock(file string) (*InstanceLock, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	var lockErr error
	if err := rc.Control(func(fd uintptr) { lockErr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB) }); err != nil {
		_ = f.Close()
		return nil, err
	}
	if lockErr != nil {
		holder := readHolder(f)
		_ = f.Close()
		if errors.Is(lockErr, syscall.EWOULDBLOCK) {
			return nil, &HeldError{Holder: holder}
		}
		return nil, lockErr
	}
	return &InstanceLock{f: f}, nil
}
