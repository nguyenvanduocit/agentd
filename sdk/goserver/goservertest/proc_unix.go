//go:build !windows

package goservertest

import (
	"errors"
	"os/exec"
	"syscall"
)

// supported is whether this operating system can run the daemon: Windows cannot (run the tests in WSL).
const supported = true

// ownGroup puts the process in a process group of its own, so that signalling the group ends it and what it started.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func terminateGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// processExists is whether the process is still there (a process that has exited and was waited for is not).
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
