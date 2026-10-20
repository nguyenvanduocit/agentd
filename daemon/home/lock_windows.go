//go:build windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION: the file is open in another process that shares it with nobody.
const errSharingViolation = syscall.Errno(32)

// AcquireInstanceLock takes the lock of a home: file is its LockFile. Windows has no flock in the standard library, so
// the lock is the open itself, with a share mode of nothing: a second open fails with a sharing violation for as long as
// the first process holds the handle, and the system closes the handle when the process ends. (For the same reason the
// loser cannot read the holder's record; its message then leaves the details out. The daemon is not a Windows
// product, the docs say WSL; this keeps the package building and the lock honest there.)
func AcquireInstanceLock(file string) (*InstanceLock, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	name, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, &HeldError{}
		}
		return nil, err
	}
	return &InstanceLock{f: os.NewFile(uintptr(h), file)}, nil
}
