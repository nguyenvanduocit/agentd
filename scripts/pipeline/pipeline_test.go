// Package pipeline tests the release pipeline end to end, the way a user meets it: scripts/build-daemon.sh builds a
// release into a directory, the server package serves it as GitHub Releases would (the stand-in of cmd/agentd-server),
// and daemon/install.sh installs it in a temporary HOME. The files of this package are tests only.
//
// A release build compiles the engine for four platforms: `go test -short` skips these tests.
package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/update"
	"github.com/nguyenvanduocit/agentd/internal/releaseserver"
)

var (
	rootDir string // the repository
	workDir string // everything the tests build, removed when they end

	signer struct {
		once sync.Once
		bin  string // the built cmd/sign-daemon
		key  string // a development signing key, as DAEMON_SIGNING_KEY names it
		b64  string // its public key as the daemons bake it in
		err  error
	}

	releasesMu sync.Mutex
	releases   = map[string]*release{}
)

func TestMain(m *testing.M) {
	var err error
	if rootDir, err = filepath.Abs("../.."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if workDir, err = os.MkdirTemp("", "agentd-pipeline-"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	releasesMu.Lock()
	for _, r := range releases {
		r.server.Close()
	}
	releasesMu.Unlock()
	_ = os.RemoveAll(workDir)
	os.Exit(code)
}

// needPipeline skips a test that builds or installs a release when the machine cannot, or `-short` says no.
func needPipeline(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: a release build compiles the engine for four platforms")
	}
	for _, tool := range []string{"bash", "curl", "tar", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// hostEngine is the file of the tarball's package/ that holds this machine's engine; the test is skipped when the
// release has none for the machine.
func hostEngine(t *testing.T) string {
	t.Helper()
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
		t.Skipf("no engine is released for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return "agentd-" + runtime.GOOS + "-" + runtime.GOARCH
}

// cleanEnv is the inherited environment without the variables scripts/build-daemon.sh reads, plus extra (KEY=value).
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "RELEASE", "VERSION", "RELEASE_URL", "TRAY_DIR", "DAEMON_SIGNING_KEY":
		default:
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// run runs a command to its end and returns its combined output and exit code (-1: it did not start).
func run(t *testing.T, env []string, name string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = rootDir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("%s: %v", name, err)
	return "", -1
}

// signingKey builds cmd/sign-daemon once and makes the development key every release of these tests is signed with.
func signingKey(t *testing.T) (key, b64 string) {
	t.Helper()
	signer.once.Do(func() {
		dir := filepath.Join(workDir, "signer")
		if signer.err = os.MkdirAll(dir, 0o755); signer.err != nil {
			return
		}
		signer.bin = filepath.Join(dir, "sign-daemon")
		signer.key = filepath.Join(dir, "daemon-signing.key")
		for _, step := range [][]string{
			{"go", "build", "-o", signer.bin, "./cmd/sign-daemon"},
			{signer.bin, "keygen", signer.key},
		} {
			cmd := exec.Command(step[0], step[1:]...)
			cmd.Dir = rootDir
			if out, err := cmd.CombinedOutput(); err != nil {
				signer.err = fmt.Errorf("%v: %w\n%s", step, err, out)
				return
			}
		}
		out, err := exec.Command(signer.bin, "public-b64", signer.key).Output()
		signer.b64, signer.err = string(out), err
	})
	if signer.err != nil {
		t.Fatal(signer.err)
	}
	return signer.key, signer.b64
}

// release is one build of scripts/build-daemon.sh served by its own release server.
type release struct {
	version string
	dist    string // the build's output directory
	url     string // the releases page baked into the engines, the server's origin
	key     string // the public key baked in (base64 of the SPKI DER)
	tgz     string
	server  *httptest.Server

	mu        sync.Mutex
	downloads map[string]int // tarball requests by file name
	err       error
}

// trayDir is a TRAY_DIR with one fake tray program: the build packs it without building the real one.
func trayDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(workDir, "tray")
	if err := os.MkdirAll(filepath.Join(dir, "darwin-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "darwin-arm64", "agentd-tray"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildRelease builds `version` once per test run: signed with the development key, with the releases page of its own
// server baked in.
func buildRelease(t *testing.T, version string) *release {
	t.Helper()
	needPipeline(t)
	key, b64 := signingKey(t)
	tray := trayDir(t)
	releasesMu.Lock()
	r, ok := releases[version]
	if !ok {
		r = &release{version: version, key: b64, downloads: map[string]int{}}
		releases[version] = r
		r.start(t, key, tray)
	}
	releasesMu.Unlock()
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r
}

func (r *release) start(t *testing.T, key, tray string) {
	t.Helper()
	var inner atomic.Pointer[http.Handler]
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := inner.Load()
		if h == nil {
			http.Error(w, "not built yet", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(req.URL.Path, ".tgz") {
			r.mu.Lock()
			r.downloads[path.Base(req.URL.Path)]++
			r.mu.Unlock()
		}
		(*h).ServeHTTP(w, req)
	}))
	r.url = r.server.URL
	r.dist = filepath.Join(workDir, "dist-"+r.version)
	out, code := run(t, cleanEnv("VERSION="+r.version, "RELEASE_URL="+r.url, "DAEMON_SIGNING_KEY="+key, "TRAY_DIR="+tray),
		"bash", filepath.Join(rootDir, "scripts", "build-daemon.sh"), r.dist)
	if code != 0 {
		r.err = fmt.Errorf("build-daemon.sh %s exited %d:\n%s", r.version, code, out)
		return
	}
	h, err := releaseserver.Handler(releaseserver.Config{Dist: r.dist})
	if err != nil {
		r.err = err
		return
	}
	inner.Store(&h)
	r.tgz = filepath.Join(r.dist, "download", "v"+r.version, "agentd-"+r.version+".tgz")
}

// tarballName is the file name of the release's tarball.
func (r *release) tarballName() string { return "agentd-" + r.version + ".tgz" }

func (r *release) downloaded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.downloads[r.tarballName()]
}

// offer is the release's latest.json as the daemon reads it.
func (r *release) offer(t *testing.T) update.Offer {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(r.dist, "download", "v"+r.version, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	offer, ok := update.ParseLatest(string(text))
	if !ok {
		t.Fatalf("latest.json is not an offer: %s", text)
	}
	return offer
}

// entry is one file of a tarball.
type entry struct {
	mode fs.FileMode
	body []byte
	dir  bool
}

// readTarball lists the tarball by member name.
func readTarball(t *testing.T, file string) map[string]entry {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]entry{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[strings.TrimSuffix(h.Name, "/")] = entry{mode: fs.FileMode(h.Mode).Perm(), body: body, dir: h.Typeflag == tar.TypeDir}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustRead(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// tarGz makes a tarball whose package/ holds the given files (name -> body), all executable.
func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
