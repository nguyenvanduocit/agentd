package goclient

import (
	"regexp"
	"strings"
)

// The command is pasted into a shell: an origin, a releases page or a code outside these patterns is refused instead of
// quoted. The app's origin pattern is the one agentd's installer checks (daemon/install.sh.in).
var (
	safeOrigin   = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?$`)
	safeReleases = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?(/[A-Za-z0-9._-]+)*$`)
	safeCode     = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)
)

// AgentdReleases is where agentd publishes its releases: the desktop app, the installer, the daemon and the SDKs are
// files of the latest one.
const AgentdReleases = "https://github.com/nguyenvanduocit/agentd/releases"

// DesktopDownloads are the disk images of agentd.app, the desktop app for macOS: one per processor.
type DesktopDownloads struct {
	AppleSilicon string
	Intel        string
}

func safeReleasesPage(releases string) bool {
	return safeReleases.MatchString(releases) && !containsDotDot(releases)
}

func containsDotDot(url string) bool {
	for _, part := range strings.Split(url, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// DesktopDownloadsFrom is where the user downloads agentd.app from the latest release of the releases page `agentd`
// (empty: AgentdReleases; a fork or a development server names its own). Opened, it runs agentd, and the app finds it
// and connects it as it finds an installed one. False while the releases page is not a safe http(s) URL.
func DesktopDownloadsFrom(agentd string) (DesktopDownloads, bool) {
	releases := agentd
	if releases == "" {
		releases = AgentdReleases
	}
	if !safeReleasesPage(releases) {
		return DesktopDownloads{}, false
	}
	return DesktopDownloads{
		AppleSilicon: releases + "/latest/download/agentd-darwin-arm64.dmg",
		Intel:        releases + "/latest/download/agentd-darwin-amd64.dmg",
	}, true
}

// InstallOptions say what the install command is for.
type InstallOptions struct {
	// Agentd is the releases page the installer comes from (empty: AgentdReleases; a fork or a development server names
	// its own).
	Agentd string
	// App is the origin of the app the daemon will pair with: the one that minted Code.
	App string
	// Code is the one-time pairing code the app's server gave.
	Code string
}

// InstallCommand is the one command that installs agentd on the user's machine, pairs it with the app and starts it.
// False while the origins or the code are not safe to put in a command.
func InstallCommand(o InstallOptions) (string, bool) {
	releases := o.Agentd
	if releases == "" {
		releases = AgentdReleases
	}
	if !safeReleasesPage(releases) || !safeOrigin.MatchString(o.App) || !safeCode.MatchString(o.Code) {
		return "", false
	}
	return "curl -fsSL " + releases + "/latest/download/install.sh | bash -s -- --app " + o.App + " --code " + o.Code, true
}

// Commands are what a user runs on the machine that has the daemon.
type Commands struct {
	// Status is the service's state.
	Status string
	// Logs is where its log is read.
	Logs string
	// Start starts it again.
	Start string
}

// AgentdCommands are what a user of an install from the terminal (install.sh) runs on their machine. The installer
// puts the launcher at ~/.agentd/bin/agentd and nothing on PATH, so each command names it. The desktop app has no
// command line: its user opens the app. Since 0.7.0 all three use the full path.
func AgentdCommands() Commands {
	const agentd = "~/.agentd/bin/agentd"
	return Commands{Status: agentd + " service status", Logs: agentd + " service logs", Start: agentd + " service start"}
}
