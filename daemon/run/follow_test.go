package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

var grantMade = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// followRig is a registryFollower over a registry file in a temp dir, with open pages at `pages`; what it ends and what
// it says are recorded.
type followRig struct {
	f     *registryFollower
	file  string
	log   *bytes.Buffer
	ended []string
	now   time.Time
}

func newFollowRig(t *testing.T, pages ...string) *followRig {
	t.Helper()
	rig := &followRig{file: filepath.Join(t.TempDir(), "daemon.json"), log: &bytes.Buffer{}, now: grantMade.Add(time.Hour)}
	rig.f = &registryFollower{
		file:    rig.file,
		log:     proc.NewLogger(rig.log, rig.log),
		now:     func() time.Time { return rig.now },
		servers: func() []string { return nil },
		pages:   func() []string { return pages },
		endPage: func(origin string) { rig.ended = append(rig.ended, origin) },
		endApp:  func(string) {},
	}
	return rig
}

func (r *followRig) save(t *testing.T, reg registry.Registry) {
	t.Helper()
	if err := registry.Save(r.file, reg); err != nil {
		t.Fatal(err)
	}
}

func TestTheSocketsOfAPageWhoseGrantLapsedAreEndedAndTheLogSaysWhy(t *testing.T) {
	rig := newFollowRig(t, pageOrigin)
	rig.save(t, registry.Empty().AddBrowser(pageOrigin, grantMade, nil))

	rig.f.check()
	if len(rig.ended) != 0 {
		t.Fatalf("ended %v while the grant is live", rig.ended)
	}

	rig.now = grantMade.Add(registry.BrowserGrantTTL + time.Minute)
	rig.f.check()
	if len(rig.ended) != 1 || rig.ended[0] != pageOrigin {
		t.Fatalf("ended %v, want the page of the lapsed grant", rig.ended)
	}
	if want := pageOrigin + " access lapsed after 30 days: ending its pages' sessions"; !strings.Contains(rig.log.String(), want) {
		t.Errorf("log = %q, want it to say %q", rig.log.String(), want)
	}
}

func TestThePagesOfAnOriginThatWasRemovedAreStillEndedWithTheOldLine(t *testing.T) {
	rig := newFollowRig(t, pageOrigin)
	rig.save(t, registry.Empty())
	rig.f.check()
	if len(rig.ended) != 1 || !strings.Contains(rig.log.String(), pageOrigin+" was disconnected: ending its pages' sessions") {
		t.Errorf("ended %v, log %q", rig.ended, rig.log.String())
	}
}

func TestADamagedRegistryIsToldOncePerProblemAndAgainWhenItComesBack(t *testing.T) {
	rig := newFollowRig(t)
	damage := func() {
		t.Helper()
		if err := os.WriteFile(rig.file, []byte("{oops"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	damage()
	for range 5 {
		rig.f.check()
	}
	if got := strings.Count(rig.log.String(), "is damaged"); got != 1 {
		t.Fatalf("told %d times in 5 checks, want once:\n%s", got, rig.log.String())
	}
	if strings.Contains(rig.log.String(), "cannot read") {
		t.Errorf("the line wraps Load's message, which names the file already:\n%s", rig.log.String())
	}

	rig.save(t, registry.Empty())
	rig.f.check()
	damage()
	rig.f.check()
	rig.f.check()
	if got := strings.Count(rig.log.String(), "is damaged"); got != 2 {
		t.Fatalf("told %d times after it was readable once and damaged again, want 2:\n%s", got, rig.log.String())
	}
}

func TestADifferentProblemIsToldAtOnceAndNotAgain(t *testing.T) {
	rig := newFollowRig(t)
	if err := os.WriteFile(rig.file, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig.f.check()
	if err := os.Remove(rig.file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rig.file, 0o700); err != nil { // reading a directory fails in words of its own
		t.Fatal(err)
	}
	rig.f.check()
	rig.f.check()
	if lines := strings.Count(strings.TrimSpace(rig.log.String()), "\n") + 1; lines != 2 {
		t.Fatalf("%d lines, want one for each problem:\n%s", lines, rig.log.String())
	}
}
