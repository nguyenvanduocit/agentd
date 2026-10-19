package run

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/update"
)

func TestMakeTempDirsIsAgentdAndSixCharactersWithABinInsideAndMode0700(t *testing.T) {
	root, bin, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if !regexp.MustCompile(`^agentd-[0-9A-Za-z]{6}$`).MatchString(filepath.Base(root)) {
		t.Fatalf("root %q is not agentd- and six characters", root)
	}
	if filepath.Dir(root) != filepath.Clean(os.TempDir()) || bin != filepath.Join(root, "bin") {
		t.Fatalf("root %q, bin %q", root, bin)
	}
	for _, dir := range []string{root, bin} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, %v", dir, info, err)
		}
	}
	other, _, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	if other == root {
		t.Fatal("two runs got one directory")
	}
}

func TestTheNextStartRemovesTheDirectoryOfADeadDaemon(t *testing.T) {
	root, bin, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	file := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(file, []byte(`{"daemon":{"pid":2147483000,"lstart":"x"},"bin":"`+bin+`","groups":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proc.NewSessionState(file, filepath.Join(t.TempDir(), "bin"), proc.NewLogger(&syncBuffer{}, &syncBuffer{})).Start()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v): the shape is not the one proc.SessionState cleans", root, err)
	}
}

func TestWorkDir(t *testing.T) {
	cwd, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty := ""
	for _, c := range []struct {
		name string
		dir  *string
		want string
		fail string
	}{
		{"where the daemon was started", nil, cwd, ""},
		{"an empty --dir is where it was started", &empty, cwd, ""},
		{"--dir", &dir, dir, ""},
		{"a relative --dir is made absolute", ptr("."), cwd, ""},
		{"a missing directory", ptr(filepath.Join(dir, "none")), "", "is not a directory"},
		{"a file", &file, "", "is not a directory"},
	} {
		got, err := workDir(c.dir)
		if c.fail != "" {
			if err == nil || !strings.Contains(err.Error(), c.fail) {
				t.Fatalf("%s: err = %v, want %q", c.name, err, c.fail)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%s: %q, %v, want %q", c.name, got, err, c.want)
		}
	}
}

func ptr(s string) *string { return &s }

func TestReleaseCheckInterval(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"":      6 * time.Hour,
		"50":    50 * time.Millisecond,
		"49":    6 * time.Hour,
		"0":     6 * time.Hour,
		"-100":  6 * time.Hour,
		"1.5":   6 * time.Hour,
		"soon":  6 * time.Hour,
		"250":   250 * time.Millisecond,
		"1e3":   time.Second,
		" 100 ": 100 * time.Millisecond,
	} {
		if got := releaseCheckInterval(home.Env{"AGENTD_RELEASE_CHECK_MS": value}); got != want {
			t.Errorf("AGENTD_RELEASE_CHECK_MS=%q: %s, want %s", value, got, want)
		}
	}
	if got := releaseCheckInterval(home.Env{}); got != 6*time.Hour {
		t.Errorf("unset: %s", got)
	}
}

func TestAnUpdateSwitchOfADaemonThatCannotUpdateTurnsNothing(t *testing.T) {
	u := noUpdates()
	u.turn(true)
	u.turn(false)
	u.stop()
	if u.updatable {
		t.Fatal("updatable")
	}
}

func TestOnlyAnInstalledDaemonUpdatesItself(t *testing.T) {
	box := &sandbox{root: t.TempDir()}
	host := home.Host{Platform: "linux", Env: home.Env{"AGENTD_HOME": box.root}, Home: box.root}
	log := proc.NewLogger(&syncBuffer{}, &syncBuffer{})
	// a source run, a test build and a copy elsewhere
	for _, exe := range []string{filepath.Join(box.root, "agentd"), filepath.Join(box.root, "elsewhere", "agentd")} {
		if _, ok := installedLayout(exe, host, log); ok {
			t.Errorf("%s counts as installed", exe)
		}
	}
}

func TestANotInstalledDaemonSaysItsUpdatesAreOff(t *testing.T) {
	r := startRig(t, sleepAgent, nil)
	waitFor(t, "the line", func() bool {
		return r.d.says("updates are off: this daemon is not installed under ~/.agentd/daemon (the installer does that)")
	})
}

func TestADaemonWithABrokenRegistryStopsWithAMessageNamingTheFile(t *testing.T) {
	box := newSandbox(t)
	if err := os.MkdirAll(filepath.Dir(box.configFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(box.configFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, box, nil, nil)
	if code := d.exitCode(t, waitLimit); code != 1 || !d.says("daemon.json") {
		t.Fatalf("exit code %d; output:\n%s", code, d.out)
	}
}

func TestRunningForAnAppThatIsNotPairedSaysHowToPairAndExits1(t *testing.T) {
	box := newSandbox(t)
	d := startDaemon(t, box, []string{"--app", "https://nowhere.example"}, nil)
	if code := d.exitCode(t, waitLimit); code != 1 {
		t.Fatalf("exit code %d; output:\n%s", code, d.out)
	}
	if !d.says("not paired with https://nowhere.example") || !d.says("agentd pair --app") {
		t.Fatalf("output:\n%s", d.out)
	}
}

func TestTheDaemonRemovesItsTemporaryDirectoryOnTheWayOut(t *testing.T) {
	tmp := t.TempDir()
	r := startRig(t, sleepAgent, map[string]string{"TMPDIR": tmp})
	entries, err := filepath.Glob(filepath.Join(tmp, "agentd-??????"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary directories: %v (%v)", entries, err)
	}
	r.d.signal(syscall.SIGTERM)
	r.d.exitCode(t, waitLimit)
	if _, err := os.Stat(entries[0]); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v)", entries[0], err)
	}
}

func TestTurningUpdatesOnAsksTheReleaseServerAndTurningThemOffStopsAsking(t *testing.T) {
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == strings.TrimPrefix(update.LatestURL("http://x"), "http://x") {
			asked.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	out := &syncBuffer{}
	u := &updates{
		updatable: true,
		layout:    home.LayoutOf(t.TempDir()),
		base:      server.URL,
		interval:  50 * time.Millisecond,
		idle:      func() bool { return true },
		exit:      func(int) { t.Error("exit called") },
		log:       proc.NewLogger(out, out),
	}
	t.Cleanup(u.stop)
	u.turn(true)
	waitFor(t, "the release server to be asked again and again", func() bool { return asked.Load() >= 3 })
	u.turn(false)
	if !strings.Contains(out.String(), "updates are off: turned off in the settings") {
		t.Fatalf("output: %s", out)
	}
	time.Sleep(200 * time.Millisecond) // a request that was on its way
	then := asked.Load()
	time.Sleep(300 * time.Millisecond)
	if now := asked.Load(); now != then {
		t.Fatalf("asked %d more times after the updates were turned off", now-then)
	}
}
