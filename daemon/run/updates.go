package run

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/update"
)

// releaseCheckEvery is how often the release server is asked for a newer daemon.
const releaseCheckEvery = 6 * time.Hour

// minReleaseCheck is the shortest interval AGENTD_RELEASE_CHECK_MS may set.
const minReleaseCheck = 50 * time.Millisecond

// releaseCheckInterval is $AGENTD_RELEASE_CHECK_MS (a test seam, at least 50 ms) or six hours.
func releaseCheckInterval(env home.Env) time.Duration {
	ms, err := strconv.ParseFloat(strings.TrimSpace(env["AGENTD_RELEASE_CHECK_MS"]), 64)
	if err != nil || ms != float64(int64(ms)) || time.Duration(ms)*time.Millisecond < minReleaseCheck {
		return releaseCheckEvery
	}
	return time.Duration(ms) * time.Millisecond
}

// installedLayout is the layout of the installed daemon this process is, or false for a source run, a test build or a
// copy elsewhere. An installed one also keeps the launcher beside it current.
func installedLayout(exe string, host home.Host, log *proc.Logger) (home.Layout, bool) {
	realHome, err := filepath.EvalSymlinks(home.AgentdHome(host))
	if err != nil {
		return home.Layout{}, false
	}
	l := home.LayoutOf(filepath.ToSlash(realHome))
	version, ok := home.InstalledVersion(filepath.ToSlash(exe), l)
	if !ok {
		return home.Layout{}, false
	}
	if version != home.Version {
		log.Warn(fmt.Sprintf("updates are off: %s is version %s, not %s", exe, home.Version, version))
		return home.Layout{}, false
	}
	if _, err := home.RefreshLauncher(filepath.Dir(exe), l); err != nil {
		return home.Layout{}, false
	}
	return l, true
}

// updates keeps this daemon on the version the release server offers, when it can update itself and the setting asks
// for it: it asks at start and every few hours, and hands each offer to the updater. Turned off, it asks nothing; a
// version already unpacked stays `current` and runs at the next start.
type updates struct {
	// updatable: this daemon can update itself at all: it is an installed daemon and there is a release server.
	updatable bool

	layout   home.Layout
	base     string
	interval time.Duration
	idle     func() bool
	exit     func(code int)
	log      *proc.Logger

	mu  sync.Mutex
	end func()
}

// noUpdates is the switch of a daemon that cannot update itself.
func noUpdates() *updates { return &updates{} }

func (u *updates) turn(on bool) {
	if !u.updatable {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if on && u.end != nil {
		return
	}
	u.haltLocked()
	if !on {
		u.log.Say("updates are off: turned off in the settings")
		return
	}
	updater := update.NewUpdater(update.UpdaterOptions{
		Layout:     u.layout,
		Server:     u.base,
		Running:    home.Version,
		ReleaseKey: home.ReleaseKey,
		Idle:       u.idle,
		Restart: func(version string) {
			u.log.Say("restarting on " + version)
			u.exit(home.ExitRestart)
		},
		Log: u.log,
	})
	watcher := update.NewWatcher(update.WatcherOptions{Base: u.base, Interval: u.interval, OnOffer: updater.Offer, Log: u.log})
	watcher.Start()
	u.end = func() {
		watcher.Stop()
		updater.Stop()
	}
}

// stop ends the checks, with the daemon.
func (u *updates) stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.haltLocked()
}

func (u *updates) haltLocked() {
	if u.end != nil {
		u.end()
		u.end = nil
	}
}
