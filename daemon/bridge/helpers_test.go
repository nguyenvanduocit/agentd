package bridge

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

const wait = 5 * time.Second

// buffer is an io.Writer that a test may read while the bridge writes.
type buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", wait, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// run is one bridge process: its stdin is a pipe the test writes, its stdout and stderr are read back.
type run struct {
	stdin  *io.PipeWriter
	stdout *buffer
	stderr *buffer
	code   chan int
}

func start(t *testing.T, d Deps, main func(Deps) int) *run {
	t.Helper()
	in, w := io.Pipe()
	r := &run{stdin: w, stdout: &buffer{}, stderr: &buffer{}, code: make(chan int, 1)}
	d.Stdin, d.Stdout, d.Stderr = in, r.stdout, r.stderr
	t.Cleanup(func() { _ = w.Close() })
	go func() { r.code <- main(d) }()
	return r
}

func (r *run) send(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(r.stdin, text); err != nil {
		t.Fatal(err)
	}
}

func (r *run) exit(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(wait):
		t.Fatalf("the bridge did not exit; stderr: %q", r.stderr.String())
		return -1
	}
}

// shortDir is a temporary directory with a short path (a socket path is limited to about 100 bytes).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "br-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func hostWithEnv(env home.Env) home.Host {
	return home.Host{Platform: "linux", Env: env, Home: "/nonexistent"}
}

func configFile(t *testing.T, json string) *string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(file, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	return &file
}

// repeating is a reader of n copies of c with no newline: one line n bytes long.
type repeating struct{ left int64 }

func (r *repeating) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.left {
		n = r.left
	}
	copy(p, strings.Repeat("x", int(n)))
	r.left -= n
	return int(n), nil
}
