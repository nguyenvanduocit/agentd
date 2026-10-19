package agents

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// frame is one frame a sink received.
type frame struct {
	ID   string
	Kind FrameKind
	Data string
	Code int
}

// recordingSink keeps the frames it is sent. Its backlog is nil unless a test sets one.
type recordingSink struct {
	mu      sync.Mutex
	frames  []frame
	backlog <-chan struct{}
}

func (s *recordingSink) Send(id string, kind FrameKind, data string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, frame{id, kind, data, code})
}

func (s *recordingSink) Backlog() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backlog
}

func (s *recordingSink) setBacklog(ch <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backlog = ch
}

func (s *recordingSink) all() []frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.frames)
}

// data is what the frames of one kind carried, in order.
func (s *recordingSink) data(kind FrameKind) []string {
	var out []string
	for _, f := range s.all() {
		if f.Kind == kind {
			out = append(out, f.Data)
		}
	}
	return out
}

func (s *recordingSink) exit() (frame, bool) {
	for _, f := range s.all() {
		if f.Kind == FrameExit {
			return f, true
		}
	}
	return frame{}, false
}

// alive reports whether a process exists.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// systemPath is the PATH of the test process.
func systemPath() string {
	return os.Getenv("PATH")
}

// fakeNpm writes an `npm` that stands in for the real one: it installs a package named $FAKE_NPM_PKG whose bin is a
// script (run with `sh`, which the tests give the fleet as its Node). It fails like npm does for a missing
// `file:` spec, can hold in a child process that says its pid ($FAKE_NPM_PIDFILE, sleeping $FAKE_NPM_SLEEP seconds),
// and logs each run to $FAKE_NPM_LOG. It returns the directory to put first on PATH.
func fakeNpm(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
prefix=""
spec=""
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) prefix="$2"; shift 2;;
    --loglevel) shift 2;;
    install|--no-audit|--no-fund) shift;;
    *) spec="$1"; shift;;
  esac
done
[ -n "$FAKE_NPM_LOG" ] && echo "$spec" >> "$FAKE_NPM_LOG"
case "$spec" in
  file:*)
    if [ ! -f "${spec#file:}" ]; then
      echo "npm error code ENOENT" >&2
      echo "npm error enoent could not read ${spec#file:}" >&2
      exit 1
    fi;;
esac
if [ -n "$FAKE_NPM_PIDFILE" ]; then
  sh -c 'echo $$ > "$1"; exec sleep "$2"' sh "$FAKE_NPM_PIDFILE" "$FAKE_NPM_SLEEP"
fi
pkg="$prefix/node_modules/$FAKE_NPM_PKG"
mkdir -p "$pkg/dist"
printf '{"name":"%s","version":"9.9.9","bin":{"adapter":"dist/index.js"}}' "$FAKE_NPM_PKG" > "$pkg/package.json"
printf 'echo "fake adapter up"\ncat\n' > "$pkg/dist/index.js"
`
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// npmEnv is the environment of a store whose npm is the fake one, installing a fake claude adapter.
func npmEnv(t *testing.T, spec string, extra home.Env) home.Env {
	t.Helper()
	env := home.Env{
		"PATH":                fakeNpm(t) + string(os.PathListSeparator) + systemPath(),
		"FAKE_NPM_PKG":        Adapters[Claude].Pkg,
		SpecVariable(Claude):  spec,
		SeamVariable("codex"): "true",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// specFile is a `file:` spec whose file exists (the fake npm only checks that).
func specFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "fake.tgz")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return "file:" + file
}
