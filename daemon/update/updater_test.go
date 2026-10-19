// The Updater against real files, a real tarball and a local HTTP server: no daemon process.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// testPlatforms are the platforms a release tarball carries a daemon for; the fixture's updater runs on testPlatform.
var testPlatforms = []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"}

const testPlatform = "linux-amd64"

type tarOpts struct {
	claims    string   // package.json's version; empty means the real one
	platforms []string // the platforms that get a binary; nil means all of testPlatforms
	code      string   // the body of every binary; empty means "// <version> <platform>"
	extra     func(tw *tar.Writer)
}

type built struct {
	bytes  []byte
	sha256 string
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// tarball is a release tarball in npm's layout holding version `version`: package/agentd-<os>-<arch> per platform,
// package/package.json, package/launcher.sh and package/tray/.
func tarball(t testing.TB, version string, o tarOpts) built {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name string, mode int64, body string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "package/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	platforms := o.platforms
	if platforms == nil {
		platforms = testPlatforms
	}
	for _, platform := range platforms {
		code := o.code
		if code == "" {
			code = "// " + version + " " + platform + "\n"
		}
		add("package/"+BinaryName(platform), 0o755, code)
	}
	add("package/launcher.sh", 0o755, "#!/bin/sh\n")
	add("package/tray/agentd-tray", 0o755, "tray")
	claims := o.claims
	if claims == "" {
		claims = version
	}
	add("package/package.json", 0o644, fmt.Sprintf(`{"version":%q}`, claims))
	if o.extra != nil {
		o.extra(tw)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return built{bytes: buf.Bytes(), sha256: sum(buf.Bytes())}
}

type fixture struct {
	t        *testing.T
	layout   home.Layout
	origin   string
	key      keyPair
	mu       sync.Mutex
	hits     []string
	restarts []string
	serve    map[string]http.HandlerFunc
	busy     atomic.Bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	const running = "1.0.0"
	l := home.LayoutOf(filepath.Join(t.TempDir(), "mc"))
	if err := os.MkdirAll(filepath.Join(l.DaemonDir, running), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.DaemonDir, running, DefaultEntry), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(running, l.CurrentLink); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, layout: l, key: newKeyPair(t), serve: map[string]http.HandlerFunc{}}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.Path)
		h := f.serve[r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	f.origin = s.URL
	return f
}

func (f *fixture) serveBytes(version string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serve[TarballPath(version)] = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b) }
}

func (f *fixture) serveFunc(version string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serve[TarballPath(version)] = h
}

func (f *fixture) hitList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func (f *fixture) restartList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restarts...)
}

func (f *fixture) updater(edit ...func(*UpdaterOptions)) (*Updater, *logCapture) {
	f.t.Helper()
	log := &logCapture{}
	o := UpdaterOptions{
		Layout: f.layout, Server: f.origin, Running: "1.0.0", ReleaseKey: f.key.key, Platform: testPlatform,
		Idle:      func() bool { return !f.busy.Load() },
		Restart:   func(v string) { f.mu.Lock(); f.restarts = append(f.restarts, v); f.mu.Unlock() },
		IdleCheck: 40 * time.Millisecond,
		Retry:     &home.BackoffOptions{Base: 60 * time.Millisecond, Max: 200 * time.Millisecond},
		Log:       log,
	}
	for _, e := range edit {
		e(&o)
	}
	u := NewUpdater(o)
	f.t.Cleanup(func() { u.Stop(); u.wg.Wait() })
	return u, log
}

func slowRetry(o *UpdaterOptions) {
	o.Retry = &home.BackoffOptions{Base: time.Minute, Max: time.Minute}
}

func (f *fixture) offerFor(version, sha string) Offer {
	return Offer{Version: version, URL: TarballPath(version), SHA256: sha, Signature: f.key.sign(version, sha)}
}

func (f *fixture) current() string {
	target, err := os.Readlink(f.layout.CurrentLink)
	if err != nil {
		f.t.Fatal(err)
	}
	return target
}

func (f *fixture) entries() []string {
	des, err := os.ReadDir(f.layout.DaemonDir)
	if err != nil {
		f.t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	slices.Sort(names)
	return names
}

func (f *fixture) read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func assertEntries(t *testing.T, f *fixture, want ...string) {
	t.Helper()
	if got := f.entries(); !slices.Equal(got, want) {
		t.Errorf("the daemon directory holds %q, want %q", got, want)
	}
}

func assertNoRestart(t *testing.T, f *fixture) {
	t.Helper()
	if got := f.restartList(); len(got) != 0 {
		t.Errorf("restarts: %q", got)
	}
}

func TestUpdater(t *testing.T) {
	t.Run("downloads, checks, unpacks, switches current, and asks for a restart once idle", func(t *testing.T) {
		f := newFixture(t)
		b := tarball(t, "2.0.0", tarOpts{})
		f.serveBytes("2.0.0", b.bytes)
		u, log := f.updater()
		u.Offer(f.offerFor("2.0.0", b.sha256))
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		if got := f.restartList(); !slices.Equal(got, []string{"2.0.0"}) {
			t.Errorf("restarts: %q", got)
		}
		if f.current() != "2.0.0" {
			t.Errorf("current is %s", f.current())
		}
		if got := strings.TrimSpace(f.read(f.layout.PreviousFile)); got != "1.0.0" {
			t.Errorf("previous is %q", got)
		}
		assertEntries(t, f, "1.0.0", "2.0.0", "current", "previous")
		if got := f.read(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd")); got != "// 2.0.0 linux-amd64\n" {
			t.Errorf("agentd is %q", got)
		}
		info, err := os.Stat(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd"))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("agentd is not mode 0755: %v %v", info, err)
		}
		left, _ := filepath.Glob(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd-*"))
		if len(left) != 0 {
			t.Errorf("the other platforms' binaries stay: %q", left)
		}
		for _, name := range []string{"launcher.sh", "package.json", "tray/agentd-tray"} {
			if _, err := os.Stat(filepath.Join(f.layout.DaemonDir, "2.0.0", name)); err != nil {
				t.Errorf("the installed version lost %s: %v", name, err)
			}
		}
		if len(log.said) != 1 || !strings.Contains(log.said[0], "updated to 2.0.0") {
			t.Errorf("said %q", log.said)
		}
	})

	t.Run("a busy daemon is not restarted until it is idle", func(t *testing.T) {
		f := newFixture(t)
		f.busy.Store(true)
		b := tarball(t, "2.0.0", tarOpts{})
		f.serveBytes("2.0.0", b.bytes)
		u, _ := f.updater()
		u.Offer(f.offerFor("2.0.0", b.sha256))
		waitFor(t, "current switched", func() bool { return f.current() == "2.0.0" }, 5*time.Second)
		time.Sleep(300 * time.Millisecond)
		assertNoRestart(t, f)
		f.busy.Store(false)
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		if got := f.restartList(); !slices.Equal(got, []string{"2.0.0"}) {
			t.Errorf("restarts: %q", got)
		}
	})

	t.Run("a wrong sha256 leaves everything as it was, and a retry that works succeeds", func(t *testing.T) {
		f := newFixture(t)
		b := tarball(t, "2.0.0", tarOpts{})
		var good atomic.Bool
		f.serveFunc("2.0.0", func(w http.ResponseWriter, _ *http.Request) {
			if good.Load() {
				_, _ = w.Write(b.bytes)
				return
			}
			_, _ = w.Write([]byte("tampered"))
		})
		u, log := f.updater()
		u.Offer(f.offerFor("2.0.0", b.sha256))
		waitFor(t, "two attempts", func() bool { return len(f.hitList()) >= 2 }, 5*time.Second)
		if f.current() != "1.0.0" {
			t.Errorf("current is %s", f.current())
		}
		assertEntries(t, f, "1.0.0", "current")
		assertNoRestart(t, f)
		if w := log.warned(); len(w) == 0 || !strings.Contains(w[0], "sha256 is") {
			t.Errorf("warnings: %q", w)
		}
		good.Store(true)
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		if f.current() != "2.0.0" {
			t.Errorf("current is %s", f.current())
		}
		for _, name := range f.entries() {
			if strings.HasPrefix(name, ".") {
				t.Errorf("leftover %s", name)
			}
		}
	})

	for name, respond := range map[string]http.HandlerFunc{
		"a server error": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) },
		"a redirect (never followed)": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
		},
	} {
		t.Run(name+" changes nothing", func(t *testing.T) {
			f := newFixture(t)
			f.serveFunc("2.0.0", respond)
			u, _ := f.updater(slowRetry)
			u.Offer(f.offerFor("2.0.0", strings.Repeat("a", 64)))
			waitFor(t, "an attempt", func() bool { return len(f.hitList()) >= 1 }, 5*time.Second)
			time.Sleep(250 * time.Millisecond)
			if f.current() != "1.0.0" {
				t.Errorf("current is %s", f.current())
			}
			assertEntries(t, f, "1.0.0", "current")
		})
	}

	t.Run("a tarball without a daemon for this platform, or whose package.json names another version, is not installed", func(t *testing.T) {
		for name, bad := range map[string]built{
			"no binary for this platform": tarball(t, "2.0.0", tarOpts{platforms: []string{"darwin-arm64", "linux-arm64"}}),
			"no binary at all":            tarball(t, "2.0.0", tarOpts{platforms: []string{}}),
			"another version":             tarball(t, "2.0.0", tarOpts{claims: "9.9.9"}),
		} {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.serveBytes("2.0.0", bad.bytes)
				u, _ := f.updater(slowRetry)
				u.Offer(f.offerFor("2.0.0", bad.sha256))
				waitFor(t, "an attempt", func() bool { return len(f.hitList()) >= 1 }, 5*time.Second)
				time.Sleep(250 * time.Millisecond)
				if f.current() != "1.0.0" {
					t.Errorf("current is %s", f.current())
				}
				assertEntries(t, f, "1.0.0", "current")
			})
		}
	})

	t.Run("a tarball with a symlink, a hard link, a path with .., or an absolute path is not installed", func(t *testing.T) {
		wants := map[string]string{"symlink": "symlink", "hard link": "symlink", "traversal": "path traversal", "absolute": "absolute path"}
		for name, extra := range map[string]func(*tar.Writer){
			"symlink": func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Name: "package/evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
			},
			"hard link": func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Name: "package/evil", Typeflag: tar.TypeLink, Linkname: "package/agentd-linux-amd64"})
			},
			"traversal": func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Name: "package/../../evil", Typeflag: tar.TypeReg})
			},
			"absolute": func(tw *tar.Writer) { _ = tw.WriteHeader(&tar.Header{Name: "/tmp/evil", Typeflag: tar.TypeReg}) },
		} {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				bad := tarball(t, "2.0.0", tarOpts{extra: extra})
				f.serveBytes("2.0.0", bad.bytes)
				u, log := f.updater(slowRetry)
				u.Offer(f.offerFor("2.0.0", bad.sha256))
				waitFor(t, "a warning", func() bool { return len(log.warned()) > 0 }, 5*time.Second)
				if w := log.warned()[0]; !strings.Contains(w, wants[name]) {
					t.Errorf("warning %q does not say %q", w, wants[name])
				}
				if f.current() != "1.0.0" {
					t.Errorf("current is %s", f.current())
				}
				assertEntries(t, f, "1.0.0", "current")
				if _, err := os.Lstat(filepath.Join(filepath.Dir(f.layout.Home), "evil")); err == nil {
					t.Error("a file escaped the unpack directory")
				}
			})
		}
	})

	t.Run("a tarball without a daemon for this platform says so", func(t *testing.T) {
		f := newFixture(t)
		bad := tarball(t, "2.0.0", tarOpts{platforms: []string{"darwin-arm64"}})
		f.serveBytes("2.0.0", bad.bytes)
		u, log := f.updater(slowRetry)
		u.Offer(f.offerFor("2.0.0", bad.sha256))
		waitFor(t, "a warning", func() bool { return len(log.warned()) > 0 }, 5*time.Second)
		if w := log.warned()[0]; !strings.Contains(w, "no daemon for linux-amd64 (agentd-linux-amd64)") {
			t.Errorf("warning %q", w)
		}
	})

	t.Run("a tarball made by tar and gzip, as build-daemon.sh packs it, is installed for the running platform", func(t *testing.T) {
		if _, err := exec.LookPath("tar"); err != nil {
			t.Skip("no tar")
		}
		stage := t.TempDir()
		pkg := filepath.Join(stage, "package")
		if err := os.Mkdir(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		own := BinaryName(runtime.GOOS + "-" + runtime.GOARCH)
		for _, name := range []string{own, "agentd-plan9-mips"} {
			if err := os.WriteFile(filepath.Join(pkg, name), []byte("#!/bin/sh\n# "+name+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"2.0.0"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(stage, "x.tgz")
		if b, err := exec.Command("sh", "-c", `tar -cf - -C "$1" package | gzip -n > "$2"`, "sh", stage, out).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		raw := readAll(t, out)

		f := newFixture(t)
		if err := os.WriteFile(filepath.Join(f.layout.DaemonDir, "1.0.0", "agentd"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
		f.serveBytes("2.0.0", raw)
		u, _ := f.updater(func(o *UpdaterOptions) { o.Platform = "" }) // the default is this process's platform
		u.Offer(f.offerFor("2.0.0", sum(raw)))
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		info, err := os.Stat(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd"))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("the entry is %v, %v", info, err)
		}
		if got := f.read(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd")); got != "#!/bin/sh\n# "+own+"\n" {
			t.Errorf("agentd is %q", got)
		}
		if _, err := os.Stat(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd-plan9-mips")); err == nil {
			t.Error("another platform's binary stays")
		}
	})

	t.Run("a version recorded as rejected is not downloaded; another one is", func(t *testing.T) {
		f := newFixture(t)
		if err := os.WriteFile(f.layout.RejectedFile, []byte("2.0.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		two, three := tarball(t, "2.0.0", tarOpts{}), tarball(t, "3.0.0", tarOpts{})
		f.serveBytes("2.0.0", two.bytes)
		f.serveBytes("3.0.0", three.bytes)
		u, log := f.updater()
		u.Offer(f.offerFor("2.0.0", two.sha256))
		time.Sleep(200 * time.Millisecond)
		if got := f.hitList(); len(got) != 0 {
			t.Errorf("requests: %q", got)
		}
		u.Offer(f.offerFor("3.0.0", three.sha256))
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		if got := f.restartList(); !slices.Equal(got, []string{"3.0.0"}) {
			t.Errorf("restarts: %q", got)
		}
		if _, err := os.Stat(f.layout.RejectedFile); err == nil {
			t.Error("the rejected record is still there")
		}
		if w := log.warned(); len(w) != 1 || !strings.Contains(w[0], "crashed at start twice") {
			t.Errorf("warnings: %q", w)
		}
	})

	t.Run("an invalid offer (another host, a path outside the tarball names) is refused without a request", func(t *testing.T) {
		f := newFixture(t)
		u, log := f.updater()
		bad := f.offerFor("2.0.0", strings.Repeat("a", 64))
		bad.URL = "http://evil.example/x.tgz"
		u.Offer(bad)
		u.Offer(f.offerFor("../x", strings.Repeat("a", 64)))
		time.Sleep(200 * time.Millisecond)
		if got := f.hitList(); len(got) != 0 {
			t.Errorf("requests: %q", got)
		}
		assertEntries(t, f, "1.0.0", "current")
		if w := log.warned(); len(w) == 0 || !strings.Contains(w[0], "the server's offer is wrong") {
			t.Errorf("warnings: %q", w)
		}
	})

	t.Run("the server going back to the running version cancels a staged update", func(t *testing.T) {
		f := newFixture(t)
		f.busy.Store(true)
		b := tarball(t, "2.0.0", tarOpts{})
		f.serveBytes("2.0.0", b.bytes)
		u, _ := f.updater()
		u.Offer(f.offerFor("2.0.0", b.sha256))
		waitFor(t, "current switched", func() bool { return f.current() == "2.0.0" }, 5*time.Second)
		u.Offer(Offer{Version: "1.0.0"})
		waitFor(t, "current back", func() bool { return f.current() == "1.0.0" }, 5*time.Second)
		f.busy.Store(false)
		time.Sleep(200 * time.Millisecond)
		assertNoRestart(t, f)
	})

	t.Run("an older version directory beyond the previous one is removed", func(t *testing.T) {
		f := newFixture(t)
		if err := os.Mkdir(filepath.Join(f.layout.DaemonDir, "0.5.0"), 0o755); err != nil {
			t.Fatal(err)
		}
		b := tarball(t, "2.0.0", tarOpts{})
		f.serveBytes("2.0.0", b.bytes)
		u, _ := f.updater()
		u.Offer(f.offerFor("2.0.0", b.sha256))
		waitFor(t, "the restart", func() bool { return len(f.restartList()) > 0 }, 5*time.Second)
		assertEntries(t, f, "1.0.0", "2.0.0", "current", "previous")
	})

	t.Run("an offer that arrives while one is worked on is taken after it", func(t *testing.T) {
		f := newFixture(t)
		two, three := tarball(t, "2.0.0", tarOpts{}), tarball(t, "3.0.0", tarOpts{})
		release := make(chan struct{})
		f.serveFunc("2.0.0", func(w http.ResponseWriter, _ *http.Request) { <-release; _, _ = w.Write(two.bytes) })
		f.serveBytes("3.0.0", three.bytes)
		u, _ := f.updater()
		u.Offer(f.offerFor("2.0.0", two.sha256))
		waitFor(t, "the first request", func() bool { return len(f.hitList()) == 1 }, 5*time.Second)
		u.Offer(f.offerFor("3.0.0", three.sha256))
		close(release)
		waitFor(t, "current 3.0.0", func() bool { return f.current() == "3.0.0" }, 5*time.Second)
	})

	t.Run("stop ends a retry that waits", func(t *testing.T) {
		f := newFixture(t)
		f.serveFunc("2.0.0", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
		u, _ := f.updater()
		u.Offer(f.offerFor("2.0.0", strings.Repeat("a", 64)))
		waitFor(t, "an attempt", func() bool { return len(f.hitList()) >= 1 }, 5*time.Second)
		u.Stop()
		u.wg.Wait()
		seen := len(f.hitList())
		time.Sleep(300 * time.Millisecond)
		if got := len(f.hitList()); got != seen {
			t.Errorf("%d requests after stop", got-seen)
		}
	})

	t.Run("signature", func(t *testing.T) {
		// refused offers one offer to a new updater, waits until it has refused (nothing staged, no restart) and returns what was fetched.
		refused := func(t *testing.T, f *fixture, offer Offer, edit ...func(*UpdaterOptions)) []string {
			t.Helper()
			u, log := f.updater(append([]func(*UpdaterOptions){slowRetry}, edit...)...)
			u.Offer(offer)
			waitFor(t, "a warning", func() bool { return len(log.warned()) > 0 }, 5*time.Second)
			time.Sleep(100 * time.Millisecond)
			if f.current() != "1.0.0" {
				t.Errorf("current is %s", f.current())
			}
			assertEntries(t, f, "1.0.0", "current")
			assertNoRestart(t, f)
			return f.hitList()
		}
		noRequests := func(t *testing.T, got []string) {
			t.Helper()
			if len(got) != 0 {
				t.Errorf("requests: %q", got)
			}
		}

		t.Run("a tampered tarball, with the sha256 the server changed to match it, is refused before it is downloaded", func(t *testing.T) {
			f := newFixture(t)
			real := tarball(t, "2.0.0", tarOpts{})
			evil := tarball(t, "2.0.0", tarOpts{code: "// evil\n"})
			f.serveBytes("2.0.0", evil.bytes)
			offer := f.offerFor("2.0.0", real.sha256)
			offer.SHA256 = evil.sha256
			noRequests(t, refused(t, f, offer))
		})
		t.Run("a signature of another version is refused", func(t *testing.T) {
			f := newFixture(t)
			b := tarball(t, "2.0.1", tarOpts{})
			f.serveBytes("2.0.1", b.bytes)
			offer := f.offerFor("2.0.1", b.sha256)
			offer.Signature = f.key.sign("2.0.0", b.sha256)
			noRequests(t, refused(t, f, offer))
		})
		t.Run("an offer without a signature is refused without a request", func(t *testing.T) {
			f := newFixture(t)
			b := tarball(t, "2.0.0", tarOpts{})
			f.serveBytes("2.0.0", b.bytes)
			offer := f.offerFor("2.0.0", b.sha256)
			offer.Signature = ""
			noRequests(t, refused(t, f, offer))
		})
		t.Run("a signature by another key is refused", func(t *testing.T) {
			f := newFixture(t)
			b := tarball(t, "2.0.0", tarOpts{})
			f.serveBytes("2.0.0", b.bytes)
			offer := f.offerFor("2.0.0", b.sha256)
			offer.Signature = newKeyPair(t).sign("2.0.0", b.sha256)
			noRequests(t, refused(t, f, offer))
		})
		t.Run("a daemon without a release key refuses a correctly signed update", func(t *testing.T) {
			f := newFixture(t)
			b := tarball(t, "2.0.0", tarOpts{})
			f.serveBytes("2.0.0", b.bytes)
			noRequests(t, refused(t, f, f.offerFor("2.0.0", b.sha256), func(o *UpdaterOptions) { o.ReleaseKey = "" }))
		})
		t.Run("a version already on disk is not made current from an unsigned offer", func(t *testing.T) {
			f := newFixture(t)
			if err := os.Mkdir(filepath.Join(f.layout.DaemonDir, "2.0.0"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.layout.DaemonDir, "2.0.0", "agentd"), nil, 0o755); err != nil {
				t.Fatal(err)
			}
			u, log := f.updater(slowRetry)
			offer := f.offerFor("2.0.0", strings.Repeat("a", 64))
			offer.Signature = f.key.sign("2.0.0", strings.Repeat("b", 64))
			u.Offer(offer)
			waitFor(t, "a warning", func() bool { return len(log.warned()) > 0 }, 5*time.Second)
			time.Sleep(100 * time.Millisecond)
			if f.current() != "1.0.0" {
				t.Errorf("current is %s", f.current())
			}
			assertNoRestart(t, f)
		})
	})
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
