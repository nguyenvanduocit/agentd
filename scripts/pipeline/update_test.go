package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/update"
)

// log collects what the updater says.
type log struct {
	mu    sync.Mutex
	lines []string
}

func (l *log) Say(message string)  { l.add("say: " + message) }
func (l *log) Warn(message string) { l.add("warn: " + message) }
func (l *log) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}
func (l *log) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// installed is a machine with release r installed by install.sh and an updater of its daemon taking updates from `from`.
func installed(t *testing.T, r, from *release) (*machine, *update.Updater, chan string, *log) {
	t.Helper()
	m := newMachine(t)
	m.pair(t)
	if out, code := m.install(t, r.url, "--no-service"); code != 0 {
		t.Fatalf("install.sh exited %d:\n%s", code, out)
	}
	restarts := make(chan string, 1)
	say := &log{}
	u := update.NewUpdater(update.UpdaterOptions{
		Layout:     home.LayoutOf(m.agentdHome),
		Server:     from.url,
		Running:    r.version,
		ReleaseKey: r.key,
		Idle:       func() bool { return true },
		Restart:    func(version string) { restarts <- version },
		Log:        say,
	})
	t.Cleanup(u.Stop)
	return m, u, restarts, say
}

// A release built by build-daemon.sh is one the updater of the installed daemon takes from the release server: the
// offer is the latest.json the server serves, its signature verifies against the key baked into the first release, the
// tarball carries the engine of this machine, and the version directory ends up as install.sh lays it out.
func TestTheUpdaterTakesABuiltReleaseFromTheReleaseServer(t *testing.T) {
	one := buildRelease(t, "1.0.0")
	two := buildRelease(t, "2.0.0")
	engine := hostEngine(t)
	m, u, restarts, say := installed(t, one, two)

	u.Offer(two.offer(t))
	select {
	case version := <-restarts:
		if version != "2.0.0" {
			t.Fatalf("restart for %s", version)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("no restart in 60 s; the updater said:\n%s", say.text())
	}

	daemonDir := filepath.Join(m.agentdHome, "daemon")
	if link, err := os.Readlink(filepath.Join(daemonDir, "current")); err != nil || link != "2.0.0" {
		t.Errorf("current -> %q, %v", link, err)
	}
	if got := strings.TrimSpace(string(mustRead(t, filepath.Join(daemonDir, "previous")))); got != "1.0.0" {
		t.Errorf("previous is %q", got)
	}
	var names []string
	list, err := os.ReadDir(daemonDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		names = append(names, e.Name())
	}
	if want := []string{"1.0.0", "2.0.0", "current", "previous"}; !slices.Equal(names, want) {
		t.Errorf("the daemon directory holds %v, want %v", names, want)
	}
	newDir := filepath.Join(daemonDir, "2.0.0")
	if !bytes.Equal(mustRead(t, filepath.Join(newDir, "agentd")), readTarball(t, two.tgz)["package/"+engine].body) {
		t.Errorf("2.0.0/agentd is not %s of the tarball", engine)
	}
	inNewDir, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range inNewDir {
		files = append(files, e.Name())
	}
	if strings.Join(files, " ") != "agentd launcher.sh package.json tray" {
		t.Errorf("the updated version directory holds %v, as install.sh lays out", files)
	}
	if n := two.downloaded(); n != 1 {
		t.Errorf("the tarball of 2.0.0 was downloaded %d times", n)
	}
	// The launcher runs what `current` names: the updated engine.
	if v, code := m.launcher(t, "version"); code != 0 || strings.TrimSpace(v) != "2.0.0" {
		t.Errorf("launcher version after the update: exit %d, %q", code, v)
	}
}

// An offer the release key did not sign is not downloaded.
func TestTheUpdaterRefusesAnOfferTheReleaseKeyDidNotSign(t *testing.T) {
	one := buildRelease(t, "1.0.0")
	two := buildRelease(t, "2.0.0")
	hostEngine(t)
	m, u, restarts, say := installed(t, one, two)
	before := two.downloaded()

	offer := two.offer(t)
	offer.Signature = one.offer(t).Signature // the signature of another version
	u.Offer(offer)
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(say.text(), "the signature does not match the release key") {
		if time.Now().After(deadline) {
			t.Fatalf("no refusal in 30 s; the updater said:\n%s", say.text())
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case version := <-restarts:
		t.Fatalf("restart for %s", version)
	case <-time.After(300 * time.Millisecond):
	}
	if link, err := os.Readlink(filepath.Join(m.agentdHome, "daemon", "current")); err != nil || link != "1.0.0" {
		t.Errorf("current -> %q, %v", link, err)
	}
	if two.downloaded() != before {
		t.Error("an unsigned offer's tarball was downloaded")
	}
	if _, err := os.Stat(filepath.Join(m.agentdHome, "daemon", "2.0.0")); err == nil {
		t.Error("2.0.0 was unpacked")
	}
}
