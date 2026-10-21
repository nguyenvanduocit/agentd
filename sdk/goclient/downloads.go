package goclient

import (
	"regexp"
	"strings"
)

// A releases page outside this pattern is refused instead of linked.
var safeReleases = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?(/[A-Za-z0-9._-]+)*$`)

// AgentdReleases is where agentd publishes its releases: the desktop app and the SDKs are files of the latest one.
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
