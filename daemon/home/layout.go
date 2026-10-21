package home

import "path"

// Layout is where the daemon keeps its files under its home (~/.agentd).
type Layout struct {
	Home string
	// AdaptersDir holds the adapters of the agents (claude, codex): adapters/<agent>/<version>, installed once by the daemon.
	AdaptersDir string
	// LockFile is held by the one daemon that runs on this home (see AcquireInstanceLock).
	LockFile string
}

// LayoutOf is the layout under `home`. It always uses "/" separators.
func LayoutOf(home string) Layout {
	return Layout{
		Home:        home,
		AdaptersDir: path.Join(home, "adapters"),
		LockFile:    path.Join(home, "run", "daemon.lock"),
	}
}
