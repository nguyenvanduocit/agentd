package proc

import "os/exec"

// The daemon runs on macOS and Linux. Windows has no process groups to signal; these keep the package building there.

func Detach(*exec.Cmd) {}

func SignalGroup(int, bool) {}

func GroupAlive(int) bool { return false }
