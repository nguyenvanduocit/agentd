package run

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The daemon as agentd.app runs it: the app starts it with a channel on fd 3 (AGENTD_UI_FD) and shows its state; the
// daemon starts no tray program of its own, does what the app chooses, and ends when the app goes.

const pageOrigin = "http://localhost:5173"

// appChannel is the app's end of the channel, and the lines the daemon sent on it.
type appChannel struct {
	conn net.Conn
	mu   sync.Mutex
	got  []map[string]any
}

// startWithApp starts `agentd run` with its UI channel on fd 3, as agentd.app does.
func startWithApp(t *testing.T, box *sandbox, env map[string]string) (*daemonProc, *appChannel) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	theirs := os.NewFile(uintptr(fds[1]), "daemon-end")
	mine := os.NewFile(uintptr(fds[0]), "app-end")
	conn, err := net.FileConn(mine)
	_ = mine.Close()
	if err != nil {
		t.Fatal(err)
	}
	merged := map[string]string{"AGENTD_UI_FD": "3"}
	for k, v := range env {
		merged[k] = v
	}
	d := startWith(t, box, []string{"run"}, merged, []*os.File{theirs})
	t.Cleanup(d.stop)
	_ = theirs.Close()
	app := &appChannel{conn: conn}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		lines := bufio.NewScanner(conn)
		lines.Buffer(make([]byte, 0, 64*1024), 10<<20)
		for lines.Scan() {
			var m map[string]any
			if json.Unmarshal(lines.Bytes(), &m) == nil {
				app.mu.Lock()
				app.got = append(app.got, m)
				app.mu.Unlock()
			}
		}
	}()
	return d, app
}

// state is the first state line that pred accepts.
func (a *appChannel) state(t *testing.T, what string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	var found map[string]any
	waitFor(t, what, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, m := range a.got {
			if m["t"] == "state" && pred(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func (a *appChannel) send(t *testing.T, line string) {
	t.Helper()
	if _, err := a.conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func pagesOf(m map[string]any) []any {
	pages, _ := m["pages"].([]any)
	return pages
}

func startAppMode(t *testing.T) (*sandbox, *daemonProc, *appChannel, string) {
	t.Helper()
	box := newSandbox(t)
	box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: "2026-10-19T00:00:00.000Z"}})
	marker := filepath.Join(box.root, "tray-started")
	d, app := startWithApp(t, box, map[string]string{"AGENTD_TRAY": sh("touch " + marker + "; sleep 60")})
	return box, d, app, marker
}

func TestAppModeSendsItsStateOnTheDescriptorAndStartsNoTrayProgram(t *testing.T) {
	_, _, app, marker := startAppMode(t)
	state := app.state(t, "a state on fd 3", func(map[string]any) bool { return true })
	pages := pagesOf(state)
	if len(pages) != 1 || pages[0] != pageOrigin {
		t.Fatalf("pages = %v", pages)
	}
	if v, _ := state["version"].(string); v != testVersion {
		t.Fatalf("version = %v", state["version"])
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the daemon started a tray program of its own")
	}
}

func TestAppModeDoesWhatTheAppChooses(t *testing.T) {
	box, _, app, _ := startAppMode(t)
	app.state(t, "a state on fd 3", func(map[string]any) bool { return true })
	app.send(t, `{"t":"action","id":"disconnect:`+pageOrigin+`"}`)
	waitFor(t, "the page disconnected", func() bool {
		data, err := os.ReadFile(box.configFile())
		return err == nil && !strings.Contains(string(data), pageOrigin)
	})
	after := app.state(t, "a state without the page", func(m map[string]any) bool { return len(pagesOf(m)) == 0 })
	if len(pagesOf(after)) != 0 {
		t.Fatalf("pages = %v", pagesOf(after))
	}
}

func TestAppModeEndsWhenTheAppGoes(t *testing.T) {
	_, d, app, _ := startAppMode(t)
	app.state(t, "a state on fd 3", func(map[string]any) bool { return true })
	_ = app.conn.Close()
	if code := d.exitCode(t, 15*time.Second); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !d.says("agentd.app closed its channel: stopping") {
		t.Fatalf("output:\n%s", d.out)
	}
}

func TestATrayProgramOfItsOwnStartsWhenThereIsNoAppAndEndsWithTheDaemon(t *testing.T) {
	box := newSandbox(t)
	started, ended := filepath.Join(box.root, "tray-started"), filepath.Join(box.root, "tray-ended")
	program := filepath.Join(box.root, "tray")
	script := "#!/bin/sh\ntouch " + started + "\ncat > /dev/null\ntouch " + ended + "\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	d := startDaemon(t, box, []string{"run"}, map[string]string{"AGENTD_TRAY": program})
	waitFor(t, "the tray program to start", func() bool { _, err := os.Stat(started); return err == nil })
	d.stop()
	if d.code != 0 {
		t.Fatalf("exit code %d", d.code)
	}
	// its stdin closes with the daemon
	waitFor(t, "the tray program to end", func() bool { _, err := os.Stat(ended); return err == nil })
}
