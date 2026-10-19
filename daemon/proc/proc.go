// Package proc is the daemon's process-level plumbing: running a program to the end, the process groups of agents
// and how they are ended, the daemon's log, and the record of live agent groups that lets the next start reap the
// leftovers of a daemon that died without cleaning up.
package proc

import (
	"bytes"
	"errors"
	"os/exec"
)

// Exited is the outcome of a program run to the end.
type Exited struct {
	Code   int
	Stdout string
	Stderr string
}

// exitMissing is the code of a program that did not run to an exit status: not found, not executable, killed by a
// signal.
const exitMissing = 127

// Exec runs a program to the end. It never fails: a program that is missing or fails is an exit code (127 for
// missing). A nil env is the daemon's own environment.
func Exec(file string, args []string, env []string) Exited {
	cmd := exec.Command(file, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return Exited{Code: 0, Stdout: stdout.String(), Stderr: stderr.String()}
	}
	code := exitMissing
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		code = exit.ExitCode()
	}
	text := stderr.String()
	if text == "" {
		text = err.Error()
	}
	return Exited{Code: code, Stdout: stdout.String(), Stderr: text}
}
