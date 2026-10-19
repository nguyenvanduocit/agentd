package home

import "path"

// Layout is everything the installer, the launcher and the daemon share under the daemon's home (~/.agentd).
type Layout struct {
	Home string
	// NodeDir is a Node.js the installer downloaded; its bin directory goes first on the launcher's PATH.
	NodeDir string
	// DaemonDir holds <version>/ directories, `current` (a symlink to one of them) and the rollback records.
	DaemonDir   string
	CurrentLink string
	// PreviousFile is the version before `current`: the launcher points `current` back at it after two quick crashes.
	PreviousFile string
	// RejectedFile is a version that crashed twice at start: the daemon does not update to it again until the server serves another.
	RejectedFile string
	// CrashesFile is "<version> <count>": consecutive quick crashes of one version, kept by the launcher.
	CrashesFile string
	Launcher    string
	LogFile     string
	// LoopPidFile is the process id of a launcher that runs in restart-loop mode (no service manager).
	LoopPidFile string
	// AdaptersDir holds the adapters of the agents (claude, codex): adapters/<agent>/<version>, installed once by the daemon.
	AdaptersDir string
	// LastExitFile is the exit code of the last daemon run under the restart loop; it tells a Stop (0) from a crash.
	LastExitFile string
}

// LayoutOf is the layout under `home`. It always uses "/" separators, as the installer and the launcher do.
func LayoutOf(home string) Layout {
	daemonDir := path.Join(home, "daemon")
	return Layout{
		Home:         home,
		NodeDir:      path.Join(home, "node"),
		DaemonDir:    daemonDir,
		CurrentLink:  path.Join(daemonDir, "current"),
		PreviousFile: path.Join(daemonDir, "previous"),
		RejectedFile: path.Join(daemonDir, "rejected"),
		CrashesFile:  path.Join(daemonDir, "crashes"),
		Launcher:     path.Join(home, "bin", "agentd"),
		LogFile:      path.Join(home, "logs", "daemon.log"),
		LoopPidFile:  path.Join(home, "run", "loop.pid"),
		AdaptersDir:  path.Join(home, "adapters"),
		LastExitFile: path.Join(home, "run", "last-exit"),
	}
}

// VersionDir is the directory of one installed daemon version.
func VersionDir(l Layout, version string) string {
	return path.Join(l.DaemonDir, version)
}

// InstalledVersion is the version a daemon binary belongs to when it is an installed one, `<home>/daemon/<version>/agentd`
// (the binary path already resolved with realpath); false for a source run, a test build or a copy elsewhere. Only an
// installed daemon updates itself: anything else would write into a home it was never installed in.
func InstalledVersion(realExe string, l Layout) (string, bool) {
	if path.Base(realExe) != "agentd" {
		return "", false
	}
	dir := path.Dir(realExe)
	if path.Dir(dir) != l.DaemonDir {
		return "", false
	}
	return path.Base(dir), true
}
