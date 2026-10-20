package ui

import (
	"os/exec"
	"syscall"
)

// hideWindow starts the tray program without a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
