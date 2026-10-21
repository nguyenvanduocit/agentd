package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CLIService names the launchd job of an agentd that an earlier version installed from a terminal, and its plist;
// AGENTD_SERVICE_LABEL and AGENTD_SERVICE_DIR name another, so that a test never touches the user's.
func CLIService(home string, env func(string) string) (label, plist string) {
	label = env("AGENTD_SERVICE_LABEL")
	if label == "" {
		label = "com.agentd.daemon"
	}
	dir := env("AGENTD_SERVICE_DIR")
	if dir == "" {
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return label, filepath.Join(dir, label+".plist")
}

// TakeOverCLIService ends and removes the launchd job of a terminal install, so that only the app's engine runs: it
// serves the same apps, from the same files. It reports whether there was one.
func TakeOverCLIService(home string) (bool, error) {
	label, plist := CLIService(home, os.Getenv)
	if _, err := os.Stat(plist); err != nil {
		return false, nil
	}
	// bootout fails for a job that is not loaded: what matters is that none runs after it.
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
	if err := os.Remove(plist); err != nil {
		return true, fmt.Errorf("remove %s: %w", plist, err)
	}
	return true, nil
}
