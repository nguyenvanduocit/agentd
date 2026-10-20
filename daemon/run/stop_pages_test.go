package run

import (
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// Who the daemon stays up for when the last app has left, and the words the log gets (the end-to-end case, a real
// program connected over /acp, is in acp_stay_test.go).
func TestWhoTheDaemonStaysUpForWhenTheLastAppHasLeft(t *testing.T) {
	for _, c := range []struct {
		pages bool
		acp   int
		want  string
	}{
		{false, 0, ""},
		{true, 0, "the web pages that may connect"},
		{false, 1, "the programs connected over /acp"},
		{false, 5, "the programs connected over /acp"},
		{true, 2, "the web pages that may connect and the programs connected over /acp"},
	} {
		if got := stayUpNote(c.pages, c.acp); got != c.want {
			t.Errorf("stayUpNote(%v, %d) = %q, want %q", c.pages, c.acp, got, c.want)
		}
	}
}

// The last app leaving ends the daemon, unless a page may still come and ask: a grant that has lapsed is not a page
// that may.
func TestTheLastAppLeavingStopsTheDaemonUnlessAPageGrantIsLive(t *testing.T) {
	const site = "https://pages.example.com"
	now := time.Now()
	for _, c := range []struct {
		name string
		reg  registry.Registry
		stop bool
	}{
		{"no grant", registry.Empty(), true},
		{"a live page grant", registry.Empty().AddBrowser(site, now.Add(-time.Hour), nil), false},
		{"a page grant that lapsed", registry.Empty().AddBrowser(site, now.Add(-registry.BrowserGrantTTL-time.Hour), nil), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "daemon.json")
			if err := registry.Save(file, c.reg); err != nil {
				t.Fatal(err)
			}
			d := &daemon{Deps: Deps{Log: proc.NewLogger(io.Discard, io.Discard)}, file: file, stopCh: make(chan int, 1)}
			d.stopUnlessPagesMayConnect(0)
			select {
			case <-d.stopCh:
				if !c.stop {
					t.Error("the daemon stopped with a live page grant")
				}
			default:
				if c.stop {
					t.Error("the daemon stayed up for nothing that may connect")
				}
			}
		})
	}
}
