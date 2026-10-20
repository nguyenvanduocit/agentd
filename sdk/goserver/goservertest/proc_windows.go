//go:build windows

package goservertest

import (
	"os"
	"os/exec"
)

// supported is whether this operating system can run the daemon: Windows cannot (run the tests in WSL). The package
// still compiles there, so that an app's tests build on every platform and skip this one.
const supported = false

func ownGroup(*exec.Cmd) {}

func terminateGroup(pid int) { killGroup(pid) }

func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
