package ui

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The daemon as agentd.app runs it: the app hands over a channel (AGENTD_UI_FD) and shows the daemon's state; the daemon
// starts no tray program of its own, does what the app chooses, and ends when the app goes.

func appDeps(t *testing.T, conn net.Conn, gone func()) (MenuDeps, string) {
	t.Helper()
	log, _ := testLog()
	file := t.TempDir() + "/daemon.json"
	writeRegistry(t, file, nil, []string{page})
	deps := MenuDeps{
		RegistryFile: file,
		Hello:        func() Hello { return Hello{Version: "0.0.0-test", Agents: []pagetools.AgentInfo{claude}} },
		Linked:       func(string) bool { return true },
		Sessions:     func() int { return 0 },
		GatewayPort:  47615,
		Stop:         func(int) {},
		Restart:      func() {},
		Disconnect: func(origin string) error {
			_, err := registry.Update(file, func(r registry.Registry) registry.Registry {
				kept := []registry.BrowserGrant{}
				for _, b := range r.Browsers {
					if b.Origin != origin {
						kept = append(kept, b)
					}
				}
				r.Browsers = kept
				return r
			})
			return err
		},
		Updates:    func() bool { return true },
		SetUpdates: func(bool) error { return nil },
		Log:        log,
		Refresh:    20 * time.Millisecond,
	}
	deps.Open = func(onAction func(Action)) Link { return NewAppLink(conn, onAction, gone, log) }
	return deps, file
}

func TestAppModeSendsItsStateToTheApp(t *testing.T) {
	conn, app := newPair(t)
	deps, _ := appDeps(t, conn, func() {})
	end := StartMenu(deps)
	defer end()
	waitFor(t, "a state on the channel", 2*time.Second, func() bool { return app.got.hasState(func(State) bool { return true }) })
	state, _ := parseState(app.got.all()[0])
	if len(state.Pages) != 1 || state.Pages[0] != page || state.Version == "" {
		t.Fatalf("got %+v", state)
	}
}

func TestAppModeDoesWhatTheAppChooses(t *testing.T) {
	conn, app := newPair(t)
	deps, file := appDeps(t, conn, func() {})
	end := StartMenu(deps)
	defer end()
	waitFor(t, "a state on the channel", 2*time.Second, func() bool { return app.got.hasState(func(State) bool { return true }) })
	app.choose(t, `{"t":"action","id":"disconnect:`+page+`"}`)
	waitFor(t, "the page disconnected", 2*time.Second, func() bool {
		b, _ := os.ReadFile(file)
		return !strings.Contains(string(b), page)
	})
	waitFor(t, "a state without the page", 2*time.Second, func() bool { return app.got.hasState(func(s State) bool { return len(s.Pages) == 0 }) })
}

func TestAppModeEndsWhenTheAppGoes(t *testing.T) {
	conn, app := newPair(t)
	gone := make(chan struct{}, 1)
	deps, _ := appDeps(t, conn, func() { gone <- struct{}{} })
	end := StartMenu(deps)
	defer end()
	waitFor(t, "a state on the channel", 2*time.Second, func() bool { return app.got.hasState(func(State) bool { return true }) })
	_ = app.conn.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("the daemon was not told that the app went")
	}
}

func TestAppModeSettingsAreSavedBesideTheRegistry(t *testing.T) {
	conn, app := newPair(t)
	deps, file := appDeps(t, conn, func() {})
	store := home.OpenSettings(home.SettingsFile(file), nil)
	deps.Updates = func() bool { return store.Get().Updates }
	deps.SetUpdates = func(on bool) error { _, err := store.Change(home.SettingsPatch{Updates: &on}); return err }
	end := StartMenu(deps)
	defer end()
	app.choose(t, `{"t":"action","id":"updates:off"}`)
	waitFor(t, "a state with updates off", 2*time.Second, func() bool { return app.got.hasState(func(s State) bool { return !s.Updates }) })
	b, err := os.ReadFile(home.SettingsFile(file))
	if err != nil || !strings.Contains(string(b), `"updates": false`) {
		t.Fatalf("settings file: %q, %v", b, err)
	}
}
