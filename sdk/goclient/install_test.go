package goclient_test

import (
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

const releases = "https://agentd.example.com/releases"

func TestInstallCommandInstallsPairsAndStartsFromTheLatestReleaseForThisAppWithThisCode(t *testing.T) {
	got, ok := goclient.InstallCommand(goclient.InstallOptions{App: "https://mind.example.com", Code: "K7QX-29PD"})
	want := "curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash -s -- --app https://mind.example.com --code K7QX-29PD"
	if !ok || got != want {
		t.Fatalf("command = %q, %v", got, ok)
	}
	if goclient.AgentdReleases != "https://github.com/nguyenvanduocit/agentd/releases" {
		t.Fatalf("AgentdReleases = %q", goclient.AgentdReleases)
	}
}

func TestInstallCommandTakesTheReleasesPageOfAnotherBuild(t *testing.T) {
	got, ok := goclient.InstallCommand(goclient.InstallOptions{Agentd: releases, App: "https://mind.example.com", Code: "K7QX-29PD"})
	want := "curl -fsSL https://agentd.example.com/releases/latest/download/install.sh | bash -s -- --app https://mind.example.com --code K7QX-29PD"
	if !ok || got != want {
		t.Fatalf("command = %q, %v", got, ok)
	}
}

func TestInstallCommandTakesAnOriginWithAPortAnIPv6LiteralAndPlainHTTPOnLoopback(t *testing.T) {
	got, ok := goclient.InstallCommand(goclient.InstallOptions{Agentd: "http://127.0.0.1:7100", App: "http://localhost:5173", Code: "ABCD-1234"})
	want := "curl -fsSL http://127.0.0.1:7100/latest/download/install.sh | bash -s -- --app http://localhost:5173 --code ABCD-1234"
	if !ok || got != want {
		t.Fatalf("command = %q, %v", got, ok)
	}
	got, ok = goclient.InstallCommand(goclient.InstallOptions{Agentd: releases, App: "http://[::1]:3000", Code: "ABCD-1234"})
	if !ok || !strings.Contains(got, "--app http://[::1]:3000") {
		t.Fatalf("command = %q, %v", got, ok)
	}
}

func TestInstallCommandIsRefusedWhenAnythingWouldNotBeSafeToPasteIntoAShell(t *testing.T) {
	good := goclient.InstallOptions{Agentd: releases, App: "https://mind.example.com", Code: "K7QX-29PD"}
	with := func(change func(*goclient.InstallOptions)) goclient.InstallOptions {
		o := good
		change(&o)
		return o
	}
	for name, o := range map[string]goclient.InstallOptions{
		"app with a path":         with(func(o *goclient.InstallOptions) { o.App = "https://mind.example.com/path" }),
		"app with a shell":        with(func(o *goclient.InstallOptions) { o.App = "https://mind.example.com'; rm -rf ~; '" }),
		"app without a scheme":    with(func(o *goclient.InstallOptions) { o.App = "mind.example.com" }),
		"app with another scheme": with(func(o *goclient.InstallOptions) { o.App = "ftp://mind.example.com" }),
		"app with a newline":      with(func(o *goclient.InstallOptions) { o.App = "https://mind.example.com\n" }),
		"app empty":               with(func(o *goclient.InstallOptions) { o.App = "" }),
		"releases with a slash":   with(func(o *goclient.InstallOptions) { o.Agentd = "https://agentd.example.com/" }),
		"releases with ..":        with(func(o *goclient.InstallOptions) { o.Agentd = "https://agentd.example.com/a/../b" }),
		"releases with a query":   with(func(o *goclient.InstallOptions) { o.Agentd = "https://agentd.example.com/a?x=1" }),
		"releases with a quote":   with(func(o *goclient.InstallOptions) { o.Agentd = "https://agentd.example.com/a'b" }),
		"releases with a space":   with(func(o *goclient.InstallOptions) { o.Agentd = "https://agentd example" }),
		"code too short":          with(func(o *goclient.InstallOptions) { o.Code = "abc" }),
		"code with a shell":       with(func(o *goclient.InstallOptions) { o.Code = "K7QX-29PD; touch pwned" }),
		"code too long":           with(func(o *goclient.InstallOptions) { o.Code = strings.Repeat("x", 33) }),
		"code empty":              with(func(o *goclient.InstallOptions) { o.Code = "" }),
	} {
		if got, ok := goclient.InstallCommand(o); ok || got != "" {
			t.Errorf("%s: command %q", name, got)
		}
	}
}

func TestAnEmptyReleasesPageIsAgentdsOwn(t *testing.T) {
	got, ok := goclient.InstallCommand(goclient.InstallOptions{App: "https://mind.example.com", Code: "K7QX-29PD"})
	if !ok || !strings.HasPrefix(got, "curl -fsSL "+goclient.AgentdReleases+"/latest/") {
		t.Fatalf("command = %q, %v", got, ok)
	}
	if d, ok := goclient.DesktopDownloadsFrom(""); !ok || !strings.HasPrefix(d.Intel, goclient.AgentdReleases+"/latest/") {
		t.Fatalf("downloads = %+v, %v", d, ok)
	}
}

func TestDesktopDownloadsAreTheDiskImagesOfAgentdAppInAgentdsLatestReleaseOnePerProcessor(t *testing.T) {
	got, ok := goclient.DesktopDownloadsFrom(goclient.AgentdReleases)
	want := goclient.DesktopDownloads{
		AppleSilicon: "https://github.com/nguyenvanduocit/agentd/releases/latest/download/agentd-darwin-arm64.dmg",
		Intel:        "https://github.com/nguyenvanduocit/agentd/releases/latest/download/agentd-darwin-amd64.dmg",
	}
	if !ok || got != want {
		t.Fatalf("downloads = %+v, %v", got, ok)
	}
}

func TestDesktopDownloadsComeFromTheReleasesPageOfAnotherBuildInstead(t *testing.T) {
	got, ok := goclient.DesktopDownloadsFrom("http://127.0.0.1:7100")
	if !ok || got.Intel != "http://127.0.0.1:7100/latest/download/agentd-darwin-amd64.dmg" {
		t.Fatalf("downloads = %+v, %v", got, ok)
	}
}

func TestDesktopDownloadsAreRefusedForAReleasesPageThatIsNotAPlainHTTPURL(t *testing.T) {
	for _, agentd := range []string{"javascript:alert(1)", "https://agentd.example.com/a/../b", "https://agentd.example.com/a?x=1"} {
		if got, ok := goclient.DesktopDownloadsFrom(agentd); ok || got != (goclient.DesktopDownloads{}) {
			t.Errorf("%q: %+v", agentd, got)
		}
	}
}

func TestAgentdCommandsAreWhatAUserRunsOnTheMachineThatHasTheDaemon(t *testing.T) {
	want := goclient.Commands{Status: "agentd service status", Logs: "~/.agentd/bin/agentd service logs", Start: "agentd service start"}
	if got := goclient.AgentdCommands(); got != want {
		t.Fatalf("commands = %+v", got)
	}
}
