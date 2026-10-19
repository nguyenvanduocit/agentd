//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// Detach makes the program lead its own process group, so one signal ends it and everything it started.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// SignalGroup signals the process group led by pgid: SIGKILL when hard, SIGTERM otherwise. A group that is already
// gone is not an error.
func SignalGroup(pgid int, hard bool) {
	signalGroup(syscall.Kill, pgid, hard)
}

func signalGroup(kill func(pid int, sig syscall.Signal) error, pgid int, hard bool) {
	// No group to signal: -1 is a failed spawn (kill(1) is init), 0 the daemon's own group, 1 every process of the user (kill(-1)).
	if pgid <= 1 {
		return
	}
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	_ = kill(-pgid, sig) // ESRCH: the group is gone
}
