//go:build !windows

package ui

import "os/exec"

func hideWindow(*exec.Cmd) {}
