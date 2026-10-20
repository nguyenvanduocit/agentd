//go:build unix

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/internal/releaseserver"
)

// The update tests above build the old and the new version from ONE tree, and run the updater inside the test. What a
// user's Mac meets is different: the daemon of the LAST RELEASE, with its own updater and its own launcher, finds a build
// of the next tree on the release server, verifies it, unpacks it, exits 75, and is started again by its own launcher;
// the new engine then replaces the launcher by rename. If a release breaks that chain the fix cannot be delivered
// through it (the launcher that rolls a bad version back is itself refreshed by the new version), and every installed
// daemon needs a reinstall by hand. So this test installs the previous release the way a user does, from the release it
// built with ITS OWN scripts, and lets it update to this tree.
//
// The previous release is the newest tag x.y.z reachable from HEAD, taken out with `git archive`. Without git or the
// tag the test skips on a developer's machine and fails when CI is set: a shallow clone must not skip it silently.
// PIPELINE_PREVIOUS_TREE (a directory) and PIPELINE_PREVIOUS_VERSION name another tree to start from instead.

// previousRelease is the tree of the last release.
type previousRelease struct {
	tag, version, dir string
}

var plainVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// cannotCompare ends the test for want of a previous release: a skip, or a failure when CI is set.
func cannotCompare(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("CI is set, so the previous release must be reachable (fetch the tags: actions/checkout fetch-depth: 0): "+format, args...)
	}
	t.Skipf("no previous release to update from: "+format, args...)
}

func previousReleaseTree(t *testing.T) previousRelease {
	t.Helper()
	if dir := os.Getenv("PIPELINE_PREVIOUS_TREE"); dir != "" {
		version := os.Getenv("PIPELINE_PREVIOUS_VERSION")
		if !plainVersion.MatchString(version) {
			t.Fatalf("PIPELINE_PREVIOUS_TREE needs PIPELINE_PREVIOUS_VERSION=x.y.z, got %q", version)
		}
		return previousRelease{tag: "v" + version, version: version, dir: dir}
	}
	if _, err := exec.LookPath("git"); err != nil {
		cannotCompare(t, "git is not installed")
	}
	out, err := exec.Command("git", "-C", rootDir, "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*.[0-9]*.[0-9]*").Output()
	if err != nil {
		cannotCompare(t, "no release tag is reachable from HEAD (a clone without tags?): %v", err)
	}
	tag := strings.TrimSpace(string(out))
	version := strings.TrimPrefix(tag, "v")
	if !plainVersion.MatchString(version) {
		cannotCompare(t, "the tag %q is not vX.Y.Z", tag)
	}
	dir := t.TempDir()
	archive := exec.Command("git", "-C", rootDir, "archive", "--format=tar", tag)
	untar := exec.Command("tar", "-x", "-C", dir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	untar.Stdin = pipe
	var archiveErr, untarErr bytes.Buffer
	archive.Stderr, untar.Stderr = &archiveErr, &untarErr
	if err := untar.Start(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Start(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Wait(); err != nil {
		_ = untar.Wait()
		cannotCompare(t, "git archive %s failed (objects missing in a shallow clone?): %v %s", tag, err, archiveErr.String())
	}
	if err := untar.Wait(); err != nil {
		t.Fatalf("tar: %v %s", err, untarErr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts", "build-daemon.sh")); err != nil {
		t.Fatalf("the tree of %s has no scripts/build-daemon.sh: %v", tag, err)
	}
	return previousRelease{tag: tag, version: version, dir: dir}
}

// versionsAround are the version one patch above and one below v (below: "" when there is none).
func versionsAround(v string) (newer, older string) {
	m := plainVersion.FindStringSubmatch(v)
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	newer = fmt.Sprintf("%d.%d.%d", major, minor, patch+1)
	switch {
	case patch > 0:
		older = fmt.Sprintf("%d.%d.%d", major, minor, patch-1)
	case minor > 0:
		older = fmt.Sprintf("%d.%d.9", major, minor-1)
	case major > 0:
		older = fmt.Sprintf("%d.9.9", major-1)
	}
	return newer, older
}

// feed is a release server of its own: the releases a build-daemon.sh run writes into dist, served as GitHub would, and
// the tarball downloads counted.
type feed struct {
	dist   string
	server *httptest.Server
	inner  atomic.Pointer[http.Handler]

	mu       sync.Mutex
	tarballs map[string]int // downloads by file name
}

func newFeed(t *testing.T) *feed {
	t.Helper()
	f := &feed{dist: t.TempDir(), tarballs: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := f.inner.Load()
		if h == nil {
			http.Error(w, "nothing is published yet", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(req.URL.Path, ".tgz") {
			f.mu.Lock()
			f.tarballs[filepath.Base(req.URL.Path)]++
			f.mu.Unlock()
		}
		(*h).ServeHTTP(w, req)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *feed) downloads(version string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tarballs["agentd-"+version+".tgz"]
}

// publish builds `version` with the build-daemon.sh of `tree`, signed with key, with this feed as its releases page, and
// makes it the latest release of the feed.
func (f *feed) publish(t *testing.T, tree, version, key string) {
	t.Helper()
	out, code := run(t, cleanEnv("VERSION="+version, "RELEASE_URL="+f.server.URL, "DAEMON_SIGNING_KEY="+key, "TRAY_DIR="+trayDir(t)),
		"bash", filepath.Join(tree, "scripts", "build-daemon.sh"), f.dist)
	if code != 0 {
		t.Fatalf("%s/scripts/build-daemon.sh %s exited %d:\n%s", tree, version, code, out)
	}
	if f.inner.Load() == nil {
		h, err := releaseserver.Handler(releaseserver.Config{Dist: f.dist})
		if err != nil {
			t.Fatal(err)
		}
		f.inner.Store(&h)
	}
}

// strangerKey is a signing key that no daemon built here trusts.
func strangerKey(t *testing.T) string {
	t.Helper()
	signingKey(t) // builds the signer
	key := filepath.Join(t.TempDir(), "stranger.key")
	if out, err := exec.Command(signer.bin, "keygen", key).CombinedOutput(); err != nil {
		t.Fatalf("sign-daemon keygen: %v\n%s", err, out)
	}
	return key
}

var (
	fakeAgentOnce sync.Once
	fakeAgentBin  string
	fakeAgentErr  error
)

// fakeAgent is the built cmd/fake-agent: an ACP agent that answers `initialize`.
func fakeAgent(t *testing.T) string {
	t.Helper()
	fakeAgentOnce.Do(func() {
		dir := filepath.Join(workDir, "fake-agent")
		if fakeAgentErr = os.MkdirAll(dir, 0o755); fakeAgentErr != nil {
			return
		}
		fakeAgentBin = filepath.Join(dir, "fake-agent")
		cmd := exec.Command("go", "build", "-o", fakeAgentBin, "./cmd/fake-agent")
		cmd.Dir = rootDir
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeAgentErr = fmt.Errorf("go build ./cmd/fake-agent: %w\n%s", err, out)
		}
	})
	if fakeAgentErr != nil {
		t.Fatal(fakeAgentErr)
	}
	return fakeAgentBin
}

// installVersion runs the install command of one release of a feed, as its page shows it, with the saved pairing.
func (m *machine) installVersion(t *testing.T, origin, version string, args ...string) (string, int) {
	t.Helper()
	script := `set -o pipefail; curl -fsSL "$0/download/v$1/install.sh" | bash -s -- "${@:2}"`
	return run(t, m.env(), "bash", append([]string{"-c", script, origin, version}, args...)...)
}

// launched is a launcher the test started: the installed ~/.agentd/bin/agentd in its restart loop, in a process group of
// its own so that everything it starts ends with it.
type launched struct {
	t    *testing.T
	m    *machine
	cmd  *exec.Cmd
	log  string
	done chan struct{}
	once sync.Once
}

func startLauncher(t *testing.T, m *machine, extraEnv ...string) *launched {
	t.Helper()
	l := &launched{t: t, m: m, log: filepath.Join(filepath.Dir(m.home), "launcher.log"), done: make(chan struct{})}
	file, err := os.Create(l.log)
	if err != nil {
		t.Fatal(err)
	}
	env := append(m.env(),
		"AGENTD_AGENT_CLAUDE="+fakeAgent(t), "AGENTD_RESTART=loop", "AGENTD_RELEASE_CHECK_MS=500", "AGENTD_LOCAL_PORT=0",
		"AGENTD_ADAPTERS=off", "AGENTD_TRAY=off")
	l.cmd = exec.Command(filepath.Join(m.agentdHome, "bin", "agentd"), "run")
	l.cmd.Dir = m.home
	l.cmd.Env = append(env, extraEnv...)
	l.cmd.Stdout, l.cmd.Stderr = file, file
	l.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := l.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = l.cmd.Wait()
		_ = file.Close()
		close(l.done)
	}()
	t.Cleanup(l.stop)
	return l
}

// stop ends the launcher and everything it started: SIGTERM to the group (the launcher forwards it and exits), then
// SIGKILL if the group is still there after 15 s.
func (l *launched) stop() {
	l.once.Do(func() {
		pgid := l.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-l.done:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-l.done
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // anything the daemon started that outlived it
	})
}

func (l *launched) text() string {
	data, _ := os.ReadFile(l.log)
	return string(data)
}

// waitFor waits until the launcher's output matches re, or fails with the output.
func (l *launched) waitFor(re string, limit time.Duration) string {
	l.t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if m := rx.FindString(l.text()); m != "" {
			return m
		}
		select {
		case <-l.done:
			l.t.Fatalf("the launcher ended while the test waited for /%s/:\n%s", re, l.text())
		case <-time.After(50 * time.Millisecond):
		}
	}
	l.t.Fatalf("no /%s/ in %s; the launcher said:\n%s", re, limit, l.text())
	return ""
}

var gatewayLine = regexp.MustCompile(`local gateway on 127\.0\.0\.1:(\d+)`)

// port is the gateway port of the newest daemon process in the log.
func (l *launched) port() int {
	l.t.Helper()
	all := gatewayLine.FindAllStringSubmatch(l.text(), -1)
	if len(all) == 0 {
		l.t.Fatalf("no gateway line in the launcher's output:\n%s", l.text())
	}
	port, _ := strconv.Atoi(all[len(all)-1][1])
	return port
}

// gatewayVersion is the version the gateway of port says in /v1/status (a page's request: it carries an Origin).
func gatewayVersion(t *testing.T, port int) string {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/v1/status", port), nil)
	req.Header.Set("Origin", "http://127.0.0.1:5199")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var status struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &status) != nil || status.Name != "agentd" {
		t.Fatalf("/v1/status: %s %s", resp.Status, body)
	}
	return status.Version
}

// agentTurn runs one ACP turn through the standard endpoint of a daemon of this tree: initialize, answered by the fake
// agent, over /acp/claude with the machine's local token.
func agentTurn(t *testing.T, m *machine, port int) {
	t.Helper()
	token := strings.TrimSpace(string(mustRead(t, filepath.Join(m.agentdHome, "acp", "token"))))
	header := http.Header{"Authorization": {"Bearer " + token}}
	conn, resp, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/acp/claude", port), header)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dial /acp/claude: %v %s", err, status)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.ID != 1 || answer.Result.ProtocolVersion != 1 {
		t.Fatalf("the agent's answer to initialize: %s", data)
	}
}

// nothingOf is true when no process has the machine's directory in its command line (the launcher, the daemon, the
// agent): it polls for a few seconds, as a process takes a moment to go.
func nothingOf(t *testing.T, m *machine) bool {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "pgrep", "-f", filepath.Dir(m.home)).Run()
		cancel()
		var exit *exec.ExitError
		if err != nil && asExit(err, &exit) && exit.ExitCode() == 1 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func asExit(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

// installOld installs the previous release on a new machine the way its page tells a user.
func installOld(t *testing.T, feedA *feed, prev previousRelease) *machine {
	t.Helper()
	m := newMachine(t)
	m.pair(t)
	// pair leaves "{}", which is enough for install.sh to see a pairing but is not a registry a daemon can read
	// (registry.Parse wants the "apps" list): a daemon started on it says the file is damaged and exits 1.
	registry := filepath.Join(m.config, "agentd", "daemon.json")
	if err := os.WriteFile(registry, []byte("{\"apps\":[],\"browsers\":[]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := m.installVersion(t, feedA.server.URL, prev.version, "--no-service"); code != 0 {
		t.Fatalf("install.sh of %s exited %d:\n%s", prev.tag, code, out)
	}
	return m
}

// plantStaleLauncher replaces ~/.agentd/bin/agentd with a launcher that differs from the one of this tree, and returns
// it. The tag and this tree may ship the same launcher.sh, and every engine puts the launcher it ships beside itself
// in place when it starts: without a planted difference, "the new engine refreshed the launcher" could not be seen,
// and the planting must come after the OLD engine started (which would put its own launcher back first). It is
// written by rename, as the engine does it: the running launcher keeps the file it has open.
func plantStaleLauncher(t *testing.T, m *machine) []byte {
	t.Helper()
	file := filepath.Join(m.agentdHome, "bin", "agentd")
	stale := append(slices.Clone(mustRead(t, file)), []byte("\n# a launcher older than the next version's: the engine of the next version replaces this file\n")...)
	if bytes.Equal(stale, mustRead(t, filepath.Join(rootDir, "daemon", "launcher.sh"))) {
		t.Fatal("the planted launcher equals this tree's launcher.sh: the refresh could not be seen")
	}
	tmp := file + ".planted"
	if err := os.WriteFile(tmp, stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
	return stale
}

func linkOf(t *testing.T, m *machine, name string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(m.agentdHome, "daemon", name))
	if err != nil {
		t.Fatalf("daemon/%s: %v", name, err)
	}
	return filepath.Base(target)
}

func exists(file string) bool {
	_, err := os.Stat(file)
	return err == nil
}

// The daemon of the last release updates itself to a build of this tree, with its own updater and its own launcher; it
// refuses a build a stranger signed and one older than itself.
func TestTheLastReleaseUpdatesToThisTree(t *testing.T) {
	needPipeline(t)
	hostEngine(t)
	prev := previousReleaseTree(t)
	newer, older := versionsAround(prev.version)
	key, _ := signingKey(t)
	t.Logf("previous release %s (tree %s), next build %s", prev.tag, prev.dir, newer)

	// The previous release, built by its own tree's scripts and published on feed A (its baked releases page).
	feedA := newFeed(t)
	feedA.publish(t, prev.dir, prev.version, key)

	t.Run("a newer build of this tree signed with the key is taken, and the launcher is refreshed", func(t *testing.T) {
		m := installOld(t, feedA, prev)
		d := startLauncher(t, m)
		d.waitFor(`agentd `+regexp.QuoteMeta(prev.version)+` \(pid`, 60*time.Second)
		if got := gatewayVersion(t, d.port()); got != prev.version {
			t.Fatalf("before the update the gateway says %q, want %s", got, prev.version)
		}
		stale := plantStaleLauncher(t, m) // only the NEW engine can put this tree's launcher back

		feedA.publish(t, rootDir, newer, key) // the next release appears on the release server

		d.waitFor(`restarting on `+regexp.QuoteMeta(newer), 90*time.Second)
		// The new engine either comes up, or the old launcher rolls it back after two quick crashes: say which, at once.
		if got := d.waitFor(`agentd `+regexp.QuoteMeta(newer)+` \(pid|crashed at start twice: back on \S+`, 90*time.Second); strings.Contains(got, "back on") {
			t.Fatalf("the old launcher rolled %s back (%s); the daemon of %s cannot update to this tree:\n%s", newer, got, prev.tag, d.text())
		}
		port := d.port()

		daemonDir := filepath.Join(m.agentdHome, "daemon")
		if got := linkOf(t, m, "current"); got != newer {
			t.Errorf("current -> %q, want %s", got, newer)
		}
		if got := strings.TrimSpace(string(mustRead(t, filepath.Join(daemonDir, "previous")))); got != prev.version {
			t.Errorf("previous is %q, want %s", got, prev.version)
		}
		for _, name := range []string{"rejected", "crashes"} {
			if exists(filepath.Join(daemonDir, name)) {
				t.Errorf("daemon/%s exists: the new version crashed and the launcher rolled it back\n%s", name, d.text())
			}
		}
		if n := feedA.downloads(newer); n != 1 {
			t.Errorf("the tarball of %s was downloaded %d times, want 1", newer, n)
		}
		launcher := mustRead(t, filepath.Join(m.agentdHome, "bin", "agentd"))
		if want := mustRead(t, filepath.Join(rootDir, "daemon", "launcher.sh")); !bytes.Equal(launcher, want) {
			t.Error("~/.agentd/bin/agentd is not this tree's launcher.sh: the new engine did not refresh the launcher")
		}
		if bytes.Equal(launcher, stale) {
			t.Error("the stale launcher is still in place")
		}
		if v, code := m.launcher(t, "version"); code != 0 || strings.TrimSpace(v) != newer {
			t.Errorf("launcher version after the update: exit %d, %q, want %s", code, v, newer)
		}
		if got := gatewayVersion(t, port); got != newer {
			t.Errorf("after the update the gateway says %q, want %s", got, newer)
		}
		agentTurn(t, m, port)

		d.stop()
		if !nothingOf(t, m) {
			t.Error("a process of the machine is still running after the launcher was stopped")
		}
	})

	refusal := func(t *testing.T, build func(f *feed), wantLog string, version string) {
		m := installOld(t, feedA, prev)
		offered := newFeed(t)
		build(offered)
		d := startLauncher(t, m, "AGENTD_RELEASE_URL="+offered.server.URL)
		d.waitFor(`agentd `+regexp.QuoteMeta(prev.version)+` \(pid`, 60*time.Second)
		d.waitFor(wantLog, 45*time.Second)
		time.Sleep(3 * time.Second) // the watcher asks every 500 ms: a taken offer would have restarted the daemon by now
		text := d.text()
		if strings.Contains(text, "restarting on") || strings.Contains(text, "updated to") {
			t.Errorf("the daemon took the offer:\n%s", text)
		}
		if got := linkOf(t, m, "current"); got != prev.version {
			t.Errorf("current -> %q, want %s", got, prev.version)
		}
		if n := offered.downloads(version); n != 0 {
			t.Errorf("the tarball of %s was downloaded %d times: the offer was refused only after the download", version, n)
		}
		if got := gatewayVersion(t, d.port()); got != prev.version {
			t.Errorf("the gateway says %q, want %s", got, prev.version)
		}
		d.stop()
		if !nothingOf(t, m) {
			t.Error("a process of the machine is still running after the launcher was stopped")
		}
	}

	t.Run("a build signed with a stranger's key is refused before anything is downloaded", func(t *testing.T) {
		refusal(t, func(f *feed) { f.publish(t, rootDir, newer, strangerKey(t)) },
			`not updating to `+regexp.QuoteMeta(newer)+`: the signature does not match`, newer)
	})

	t.Run("a build older than the running version is refused", func(t *testing.T) {
		if older == "" {
			t.Skip("no version is older than " + prev.version)
		}
		refusal(t, func(f *feed) { f.publish(t, rootDir, older, key) },
			`not updating: the server's offer is wrong \(version `+regexp.QuoteMeta(older)+` is older than the running `+regexp.QuoteMeta(prev.version)+`\)`, older)
	})
}
