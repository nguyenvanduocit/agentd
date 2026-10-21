package run

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// One home, one daemon. A second `agentd run` on a home that a daemon already holds says so, starts nothing, writes
// nothing and exits with 0: the exit codes are the protocol with agentd.app (home/exit.go), and 0 is the one that stays
// stopped. Any other code would be started again at once and counted as a crash.

// theMessage is what the second daemon prints, with the timestamp every line of a daemon's log starts with.
func theMessage(holderPid int, kind string) *regexp.Regexp {
	return regexp.MustCompile(`^\S+ agentd is already running on this computer \(pid ` + fmt.Sprint(holderPid) + `, started ` + kind +
		`, (just now|\d+ seconds? ago)\): not starting a second one\. Use that one, or quit it first\.$`)
}

// snapshot is the name, size, mode and modification time of everything under dir.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file that went away while the walk ran
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = fmt.Sprintf("%d %v %d", info.Size(), info.Mode(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// answers is whether the daemon's gateway answers a page.
func (g *gatewayDaemon) answers() bool {
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/v1/status", g.port), nil)
	req.Header.Set("Origin", "http://127.0.0.1:5199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode == 200 && strings.Contains(string(body), `"agentd"`)
}

// startedAt waits for the daemon to be fully up: its gateway answers and it has recorded where it listens.
func settled(t *testing.T, g *gatewayDaemon) {
	t.Helper()
	waitFor(t, "the first daemon to answer", g.answers)
	waitFor(t, "acp.json", func() bool {
		_, err := os.Stat(filepath.Join(g.box.agentdHome(), "acp", "acp.json"))
		return err == nil
	})
}

func TestASecondDaemonOnTheSameHomeSaysSoAndLeavesEverythingAlone(t *testing.T) {
	box := newSandbox(t)
	first := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	settled(t, first)
	before := snapshot(t, box.root)

	second := startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent})
	// 0, and not another code: see the comment at the top of this file.
	if code := second.exitCode(t, 5*time.Second); code != 0 {
		t.Fatalf("the second daemon exited with %d, want 0; output:\n%s", code, second.out)
	}

	out := strings.TrimRight(second.out.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("the second daemon printed more than one line:\n%s", out)
	}
	if !theMessage(first.d.pid(), "from a terminal").MatchString(out) {
		t.Errorf("the second daemon said %q", out)
	}
	if second.says("local gateway on") {
		t.Error("the second daemon opened a gateway")
	}

	// The first goes on as if nothing happened.
	if first.d.hasExited() {
		t.Fatalf("the first daemon exited; output:\n%s", first.d.out)
	}
	if !first.answers() {
		t.Error("the first daemon's gateway stopped answering")
	}
	if after := snapshot(t, box.root); fmt.Sprint(after) != fmt.Sprint(before) {
		for name, was := range before {
			if now, ok := after[name]; !ok || now != was {
				t.Errorf("the second daemon changed %s: %q -> %q", name, was, now)
			}
		}
		for name := range after {
			if _, ok := before[name]; !ok {
				t.Errorf("the second daemon made %s", name)
			}
		}
	}
}

func TestTheLockRecordNamesTheDaemonThatHoldsIt(t *testing.T) {
	box := newSandbox(t)
	first := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	settled(t, first)

	var rec home.LockRecord
	waitFor(t, "the lock record", func() bool {
		text, err := os.ReadFile(filepath.Join(box.agentdHome(), "run", "daemon.lock"))
		return err == nil && json.Unmarshal(text, &rec) == nil && rec.PID != 0
	})
	if rec.PID != first.d.pid() || rec.Kind != home.KindTerminal || time.Since(rec.StartedAt) > time.Minute {
		t.Errorf("record = %+v, want pid %d started from a terminal a moment ago", rec, first.d.pid())
	}
	if st, err := os.Stat(filepath.Join(box.agentdHome(), "run", "daemon.lock")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode: %v, %v; want 0600", st, err)
	}
}

func TestTheKindOfADaemonComesFromItsUI(t *testing.T) {
	someUI := func(func(ui.Action)) ui.Link { return nil }
	if got := instanceKind(Deps{}); got != home.KindTerminal {
		t.Errorf("no UI: %q, want %q", got, home.KindTerminal)
	}
	if got := instanceKind(Deps{UI: someUI}); got != home.KindApp {
		t.Errorf("a UI: %q, want %q", got, home.KindApp)
	}
}

func TestAKilledDaemonLeavesNoLockBehind(t *testing.T) {
	box := newSandbox(t)
	first := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	settled(t, first)
	// kill -9: nothing runs on the way out, so a lock that depended on cleanup would stay.
	_ = syscall.Kill(first.d.pid(), syscall.SIGKILL)
	first.d.exitCode(t, 5*time.Second)

	second := startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent})
	g := waitForGateway(t, second, box)
	if second.says("already running") || !g.answers() {
		t.Fatalf("the daemon after a kill -9 did not start; output:\n%s", second.out)
	}
}

func TestADaemonThatEndsGivesTheHomeBack(t *testing.T) {
	box := newSandbox(t)
	first := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	settled(t, first)
	first.d.signal(syscall.SIGTERM)
	if code := first.d.exitCode(t, 10*time.Second); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	second := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	settled(t, second)
}

// The app starts a new engine as soon as the old one has exited with 75 (a restart): the lock must be free by then, or
// every restart would end in "already running".
func TestARestartThroughExit75FindsTheLockFree(t *testing.T) {
	r := startRig(t, sleepAgent, map[string]string{"AGENTD_MANAGED": "1"})
	r.app.restart(t)
	if code := r.d.exitCode(t, waitLimit); code != 75 {
		t.Fatalf("exit code %d, want 75; output:\n%s", code, r.d.out)
	}
	next := startDaemon(t, r.box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent, "AGENTD_MANAGED": "1"})
	g := waitForGateway(t, next, r.box)
	if next.says("already running") || !g.answers() {
		t.Fatalf("the engine after a restart did not start; output:\n%s", next.out)
	}
}

func TestTwoHomesTwoDaemons(t *testing.T) {
	one, other := newSandbox(t), newSandbox(t)
	a := waitForGateway(t, startDaemon(t, one, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), one)
	b := waitForGateway(t, startDaemon(t, other, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), other)
	waitFor(t, "both to answer", func() bool { return a.answers() && b.answers() })
	if a.port == b.port {
		t.Errorf("both daemons listen on %d", a.port)
	}
	if a.d.says("already running") || b.d.says("already running") {
		t.Error("a daemon of another home was taken for a second one")
	}
}

// A home where the lock cannot be made (here run/ is a file) is not a reason to refuse to serve: the daemon says it
// runs without the lock and goes on.
func TestADaemonThatCannotMakeTheLockStillServes(t *testing.T) {
	box := newSandbox(t)
	if err := os.MkdirAll(box.agentdHome(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box.agentdHome(), "run"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := waitForGateway(t, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent}), box)
	waitFor(t, "the gateway", g.answers)
	if !g.d.says("single-instance lock") {
		t.Errorf("the daemon did not say it runs without the lock; output:\n%s", g.d.out)
	}
}
