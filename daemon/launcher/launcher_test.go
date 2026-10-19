// Package launcher tests daemon/launcher.sh: the real launcher under a real sh, running fake `agentd` shell scripts as
// the installed versions of a temporary AGENTD_HOME. The exit codes are the protocol: 0 stays stopped, 75 starts again
// now, anything else is a crash.
package launcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	launcherSource = "../launcher.sh"
	exitWait       = 30 * time.Second
)

// agentdHome is a temporary AGENTD_HOME with the launcher at bin/agentd, the way install.sh leaves it.
type agentdHome struct {
	t      *testing.T
	root   string
	daemon string
}

func newHome(t *testing.T) *agentdHome {
	t.Helper()
	h := &agentdHome{t: t, root: t.TempDir()}
	h.daemon = filepath.Join(h.root, "daemon")
	if err := os.MkdirAll(filepath.Join(h.root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(launcherSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "bin", "agentd"), text, 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

// version installs a version whose `agentd` is the shell script `script`; with current it becomes `current`.
func (h *agentdHome) version(name, script string, current bool) *agentdHome {
	h.t.Helper()
	dir := filepath.Join(h.daemon, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agentd"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if current {
		h.point(name)
	}
	return h
}

// point makes `current` point at a version.
func (h *agentdHome) point(name string) {
	h.t.Helper()
	link := h.file("current")
	_ = os.Remove(link)
	if err := os.Symlink(name, link); err != nil {
		h.t.Fatal(err)
	}
}

// file is a file of the daemon directory.
func (h *agentdHome) file(name string) string { return filepath.Join(h.daemon, name) }

func (h *agentdHome) write(path, text string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

// read is the trimmed text of a file of the daemon directory.
func (h *agentdHome) read(name string) string { return readTrimmed(h.t, h.file(name)) }

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(text))
}

// current is the version `current` points at.
func (h *agentdHome) current() string {
	h.t.Helper()
	target, err := os.Readlink(h.file("current"))
	if err != nil {
		h.t.Fatal(err)
	}
	return target
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// output is what a launcher writes, read while it is written.
type output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// run is a launcher process.
type run struct {
	t      *testing.T
	cmd    *exec.Cmd
	out    *output
	exited chan struct{}
	code   int
}

// start runs bin/agentd with args in its own process group. PATH holds nothing but the system's directories, so a
// Node.js is only there when the home carries one.
func (h *agentdHome) start(args []string, env ...string) *run {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.root, "bin", "agentd"), args...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + h.root, "AGENTD_HOME=" + h.root}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &output{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	r := &run{t: h.t, cmd: cmd, out: out, exited: make(chan struct{})}
	h.t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	go func() {
		_ = cmd.Wait()
		r.code = cmd.ProcessState.ExitCode()
		close(r.exited)
	}()
	return r
}

func (r *run) pid() int { return r.cmd.Process.Pid }

// wait is the exit code of the launcher.
func (r *run) wait() int {
	r.t.Helper()
	select {
	case <-r.exited:
		return r.code
	case <-time.After(exitWait):
		_ = syscall.Kill(-r.pid(), syscall.SIGKILL)
		r.t.Fatalf("the launcher is still running after %s; output:\n%s", exitWait, r.out)
		return -1
	}
}

func (r *run) terminate() { _ = syscall.Kill(r.pid(), syscall.SIGTERM) }

func (r *run) says(text string) bool { return strings.Contains(r.out.String(), text) }

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(exitWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", exitWait, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exits(code int) string { return fmt.Sprintf("exit %d", code) }

// printsArgs prints the arguments of the daemon as a JSON array, then exits with code.
func printsArgs(code int) string {
	return fmt.Sprintf(`printf '['; sep=; for a in "$@"; do printf '%%s"%%s"' "$sep" "$a"; sep=,; done; printf ']\n'; exit %d`, code)
}

// lines is the number of lines of a file; 0 when it does not exist.
func lines(path string) int {
	text, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(text), "\n")
}

func TestNotSupervised(t *testing.T) {
	t.Parallel()
	t.Run("pair, version, service and mcp run as they are, arguments untouched", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", printsArgs(7), true)
		args := []string{"pair", "--app", "http://x y", "--code", "A-1"}
		r := h.start(args)
		if code := r.wait(); code != 7 {
			t.Fatalf("exit code %d, want 7; output:\n%s", code, r.out)
		}
		var got []string
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.out.String())), &got); err != nil {
			t.Fatalf("output %q: %v", r.out, err)
		}
		if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
			t.Errorf("the daemon got %q, want %q", got, args)
		}
	})

	t.Run("no daemon installed: a message and exit 1", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		r := h.start([]string{"run"})
		if code := r.wait(); code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
		if !r.says("no daemon is installed") || !r.says(filepath.Join(h.daemon, "current", "agentd")+" is missing") {
			t.Errorf("output: %q", r.out)
		}
	})

	t.Run("a Node.js under <home>/node comes first on PATH, for the adapters", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", `exec node "$@"`, true)
		h.write(filepath.Join(h.root, "node", "bin", "node"), "#!/bin/sh\necho private-node \"$@\"\n")
		r := h.start([]string{"version"})
		if code := r.wait(); code != 0 {
			t.Fatalf("exit code %d; output:\n%s", code, r.out)
		}
		if !r.says("private-node version") {
			t.Errorf("output: %q", r.out)
		}
	})

	t.Run("without that Node.js the PATH is the one the launcher got", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", `echo "$PATH"`, true)
		r := h.start([]string{"version"})
		r.wait()
		if got := strings.TrimSpace(r.out.String()); got != "/usr/bin:/bin" {
			t.Errorf("PATH is %q", got)
		}
	})
}

func TestOneSupervisedCycle(t *testing.T) {
	t.Parallel()
	t.Run("the exit code of the daemon is the exit code of the launcher", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", exits(3), true)
		for range 2 {
			if code := h.start([]string{"run"}).wait(); code != 3 {
				t.Errorf("exit code %d, want 3", code)
			}
		}
	})

	t.Run("the arguments reach the daemon, with or without the word run", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", printsArgs(0), true)
		for _, args := range [][]string{{"run", "--dir", "/w"}, {"--app", "http://s"}} {
			r := h.start(args)
			r.wait()
			want, _ := json.Marshal(args)
			if got := strings.TrimSpace(r.out.String()); got != string(want) {
				t.Errorf("the daemon got %s, want %s", got, want)
			}
		}
	})

	t.Run("two quick crashes of a new version in a row roll back to the previous one, which is not tried again", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", "sleep 100", false).version("2.0.0", exits(1), true)
		h.write(h.file("previous"), "1.0.0\n")
		if code := h.start([]string{"run"}).wait(); code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
		if got := h.read("crashes"); got != "2.0.0 1" {
			t.Errorf("crashes is %q", got)
		}
		if h.current() != "2.0.0" {
			t.Errorf("current is %s after one crash", h.current())
		}
		second := h.start([]string{"run"})
		if code := second.wait(); code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
		if h.current() != "1.0.0" {
			t.Errorf("current is %s, want 1.0.0", h.current())
		}
		if got := h.read("rejected"); got != "2.0.0" {
			t.Errorf("rejected is %q", got)
		}
		if exists(h.file("previous")) || exists(h.file("crashes")) {
			t.Error("previous or crashes is left after the rollback")
		}
		if !second.says("back on 1.0.0") {
			t.Errorf("output: %q", second.out)
		}
	})

	t.Run("a crash of another version starts the count again", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", exits(1), true).version("2.0.0", exits(1), false)
		h.write(h.file("previous"), "2.0.0\n")
		h.start([]string{"run"}).wait()
		if got := h.read("crashes"); got != "1.0.0 1" {
			t.Errorf("crashes is %q", got)
		}
		h.point("2.0.0")
		h.start([]string{"run"}).wait()
		if got := h.read("crashes"); got != "2.0.0 1" {
			t.Errorf("crashes is %q", got)
		}
		if h.current() != "2.0.0" {
			t.Errorf("current is %s", h.current())
		}
	})

	t.Run("a clean exit, an update exit and a daemon that ran long enough clear the count", func(t *testing.T) {
		t.Parallel()
		// 75 starts the daemon again at once: the second run (the marker is there) ends the cycle with 0
		updateExit := `if [ -e "$0.ran" ]; then exit 0; fi; touch "$0.ran"; exit 75`
		for name, tc := range map[string]struct {
			script string
			env    []string
			code   int
		}{
			"a clean exit":                    {exits(0), nil, 0},
			"an update exit":                  {updateExit, nil, 0},
			"a crash after the quick seconds": {exits(1), []string{"AGENTD_QUICK_SECONDS=0"}, 1},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h := newHome(t).version("1.0.0", tc.script, true).version("0.9.0", "", false)
				h.write(h.file("crashes"), "1.0.0 1\n")
				h.write(h.file("previous"), "0.9.0\n")
				if code := h.start([]string{"run"}, tc.env...).wait(); code != tc.code {
					t.Fatalf("exit code %d, want %d", code, tc.code)
				}
				if exists(h.file("crashes")) {
					t.Error("the count is kept")
				}
				if h.current() != "1.0.0" {
					t.Errorf("current is %s", h.current())
				}
			})
		}
	})

	t.Run("nothing to roll back to: the count is kept, current stays", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", exits(1), true)
		h.start([]string{"run"}).wait()
		h.start([]string{"run"}).wait()
		if got := h.read("crashes"); got != "1.0.0 2" {
			t.Errorf("crashes is %q", got)
		}
		if h.current() != "1.0.0" {
			t.Errorf("current is %s", h.current())
		}
		if exists(h.file("rejected")) {
			t.Error("a version was rejected")
		}
	})

	t.Run("a previous that is not a plain version, or is a version that is not installed, is never followed", func(t *testing.T) {
		t.Parallel()
		for name, previous := range map[string]string{
			"a path out of the directory": "../../etc",
			"an entry of the directory":   "current",
			"the same in capitals":        "CURRENT",
			"a version that is not there": "0.1.0",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h := newHome(t).version("1.0.0", exits(1), true)
				h.write(h.file("previous"), previous+"\n")
				h.start([]string{"run"}).wait()
				h.start([]string{"run"}).wait()
				if h.current() != "1.0.0" {
					t.Errorf("current is %s", h.current())
				}
				if exists(h.file("rejected")) {
					t.Error("a version was rejected")
				}
			})
		}
	})

	t.Run("a previous without an agentd is never followed", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", exits(1), true)
		h.write(filepath.Join(h.daemon, "0.9.0", "daemon.js"), "// the old bundle\n")
		h.write(h.file("previous"), "0.9.0\n")
		h.start([]string{"run"}).wait()
		h.start([]string{"run"}).wait()
		if h.current() != "1.0.0" {
			t.Errorf("current is %s", h.current())
		}
	})

	t.Run("exit 75: the updated daemon starts at once, in the same launcher", func(t *testing.T) {
		t.Parallel()
		// the daemon reaches its home through $0 (<home>/daemon/current/agentd) and points current at 2.0.0
		switchTo2 := `ln -sfn 2.0.0 "$(dirname "$(dirname "$0")")/current"; exit 75`
		h := newHome(t).version("1.0.0", switchTo2, true).version("2.0.0", `echo "now running 2.0.0"; exit 0`, false)
		r := h.start([]string{"run"})
		if code := r.wait(); code != 0 {
			t.Fatalf("exit code %d; output:\n%s", code, r.out)
		}
		if !r.says("now running 2.0.0") || !r.says("starting the daemon again at once (2.0.0)") {
			t.Errorf("output: %q", r.out)
		}
	})

	t.Run("SIGTERM to the launcher ends the daemon, and the launcher exits 0", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", `trap 'exit 0' TERM; echo "up $$"; while :; do sleep 1; done`, true)
		r := h.start([]string{"run"})
		var pid int
		waitFor(t, "the daemon to start", func() bool {
			_, rest, ok := strings.Cut(r.out.String(), "up ")
			if !ok || !strings.Contains(rest, "\n") {
				return false
			}
			pid, _ = strconv.Atoi(strings.TrimSpace(rest))
			return pid > 0
		})
		r.terminate()
		if code := r.wait(); code != 0 {
			t.Fatalf("exit code %d; output:\n%s", code, r.out)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("the daemon (pid %d) is still there", pid)
		}
	})
}

func TestRestartLoop(t *testing.T) {
	t.Parallel()
	loop := "AGENTD_RESTART=loop"

	t.Run("restarts after every crash with a growing delay; SIGTERM ends the loop and removes the pid file", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		runs := filepath.Join(h.root, "runs")
		h.version("1.0.0", fmt.Sprintf(`echo x >>%q; exit 1`, runs), true)
		r := h.start([]string{"run"}, loop)
		waitFor(t, "three runs", func() bool { return lines(runs) >= 3 })
		if !r.says("starting it again in 1 s") || !r.says("starting it again in 2 s") {
			t.Errorf("output: %q", r.out)
		}
		if got := readTrimmed(t, filepath.Join(h.root, "run", "loop.pid")); got != strconv.Itoa(r.pid()) {
			t.Errorf("loop.pid is %q, the launcher is %d", got, r.pid())
		}
		r.terminate()
		if code := r.wait(); code != 0 {
			t.Errorf("exit code %d, want 0", code)
		}
		if exists(filepath.Join(h.root, "run", "loop.pid")) {
			t.Error("loop.pid is left")
		}
		if got := readTrimmed(t, filepath.Join(h.root, "run", "last-exit")); got != "0" {
			t.Errorf("last-exit is %q, want 0 for a stop the launcher was told", got)
		}
	})

	t.Run("kill -9 of the daemon brings it back", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		pidFile := filepath.Join(h.root, "pid")
		h.version("1.0.0", fmt.Sprintf(`echo $$ >%q; while :; do sleep 1; done`, pidFile), true)
		r := h.start([]string{"run"}, loop)
		var first int
		waitFor(t, "the first daemon", func() bool {
			first, _ = strconv.Atoi(readOr(pidFile))
			return first > 0
		})
		if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "a new daemon", func() bool {
			now, _ := strconv.Atoi(readOr(pidFile))
			return now > 0 && now != first
		})
		r.terminate()
		r.wait()
	})

	t.Run("exit 0 (a Stop, a revoked token) ends the loop and is remembered; the daemon runs as managed", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", `echo "managed=$AGENTD_MANAGED"; exit 0`, true)
		r := h.start([]string{"run"}, loop)
		if code := r.wait(); code != 0 {
			t.Fatalf("exit code %d; output:\n%s", code, r.out)
		}
		if !r.says("managed=1") || !r.says("it stays stopped") {
			t.Errorf("output: %q", r.out)
		}
		if got := readTrimmed(t, filepath.Join(h.root, "run", "last-exit")); got != "0" {
			t.Errorf("last-exit is %q", got)
		}
		if exists(filepath.Join(h.root, "run", "loop.pid")) {
			t.Error("loop.pid is left")
		}
	})

	t.Run("a crash is remembered in last-exit", func(t *testing.T) {
		t.Parallel()
		h := newHome(t).version("1.0.0", exits(3), true)
		r := h.start([]string{"run"}, loop)
		waitFor(t, "last-exit", func() bool { return readOr(filepath.Join(h.root, "run", "last-exit")) != "" })
		if got := strings.TrimSpace(readOr(filepath.Join(h.root, "run", "last-exit"))); got != "3" {
			t.Errorf("last-exit is %q, want 3", got)
		}
		r.terminate()
		r.wait()
	})

	t.Run("exit 75 restarts at once in the loop too, with no delay", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		runs := filepath.Join(h.root, "runs")
		h.version("1.0.0", fmt.Sprintf(`echo x >>%q; if [ "$(wc -l <%q)" -ge 3 ]; then exit 0; fi; exit 75`, runs, runs), true)
		r := h.start([]string{"run"}, loop)
		if code := r.wait(); code != 0 {
			t.Fatalf("exit code %d; output:\n%s", code, r.out)
		}
		if got := lines(runs); got != 3 {
			t.Errorf("the daemon ran %d times, want 3", got)
		}
		if r.says("starting it again in") {
			t.Errorf("a delay before a restart: %q", r.out)
		}
	})

	t.Run("a daemon that fails after it started is started again, and the exit is told", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		runs := filepath.Join(h.root, "runs")
		h.version("1.0.0", fmt.Sprintf(`echo x >>%q; sleep 0.1; exit 1`, runs), true)
		r := h.start([]string{"run"}, loop)
		waitFor(t, "two runs", func() bool { return lines(runs) >= 2 })
		if !r.says("daemon 1.0.0 exited with 1") {
			t.Errorf("output: %q", r.out)
		}
		r.terminate()
		r.wait()
	})
}

// readOr is the text of a file, or "" when it cannot be read.
func readOr(path string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(text))
}
