//go:build !windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// osProcesses is Processes for the processes of this machine.
type osProcesses struct{}

func (osProcesses) Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (osProcesses) Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func (osProcesses) StartDetached(file string, args []string, dir string, env []string, out *os.File) error {
	cmd := exec.Command(file, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (osProcesses) Run(file string, args []string) int {
	cmd := exec.Command(file, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return exit.ExitCode()
		}
		if exit == nil {
			return 127
		}
	}
	return 0
}
