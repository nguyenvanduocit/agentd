//go:build !unix && !windows

package home

// AcquireInstanceLock on a system with neither flock nor a share-nothing open takes no lock: the daemon runs, and two of
// them on one home are not stopped. (No supported platform is here; the file keeps the package building.)
func AcquireInstanceLock(string) (*InstanceLock, error) { return &InstanceLock{}, nil }
