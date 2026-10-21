package goclient_test

import (
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

func TestAnEmptyReleasesPageIsAgentdsOwn(t *testing.T) {
	if goclient.AgentdReleases != "https://github.com/nguyenvanduocit/agentd/releases" {
		t.Fatalf("AgentdReleases = %q", goclient.AgentdReleases)
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
	for _, agentd := range []string{"javascript:alert(1)", "https://agentd.example.com/a/../b", "https://agentd.example.com/a?x=1", "https://agentd.example.com/", "https://agentd.example.com/a'b", "https://agentd example"} {
		if got, ok := goclient.DesktopDownloadsFrom(agentd); ok || got != (goclient.DesktopDownloads{}) {
			t.Errorf("%q: %+v", agentd, got)
		}
	}
}
