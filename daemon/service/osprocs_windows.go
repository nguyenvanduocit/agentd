package service

import (
	"errors"
	"os"
)

// osProcesses is Processes for Windows, where the daemon has no service: the restart loop needs signals and sessions.
type osProcesses struct{}

var errWindows = errors.New("the restart loop is not available on Windows")

func (osProcesses) Alive(int) bool      { return false }
func (osProcesses) Terminate(int) error { return errWindows }
func (osProcesses) StartDetached(string, []string, string, []string, *os.File) error {
	return errWindows
}
func (osProcesses) Run(string, []string) int { return 127 }
