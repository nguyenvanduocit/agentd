package run

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The daemon under test is the real binary: built once per run into a temporary directory, with a version of its own,
// and started as a process with a home, a config and a state directory of its own.

const testVersion = "0.0.0-itest"

var binaries struct{ agentd, fakeAgent, helper string }

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "agentd-run-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binaries.agentd = filepath.Join(dir, "agentd")
	binaries.fakeAgent = filepath.Join(dir, "fake-agent")
	binaries.helper = filepath.Join(dir, "helper")
	ldflags := "-X github.com/nguyenvanduocit/agentd/daemon/home.Version=" + testVersion
	builds := [][]string{
		{"build", "-ldflags", ldflags, "-o", binaries.agentd, "../../cmd/agentd"},
		{"build", "-o", binaries.fakeAgent, "../../cmd/fake-agent"},
		{"build", "-o", binaries.helper, "./testdata/helper"},
	}
	for _, args := range builds {
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go %s: %v\n%s", strings.Join(args, " "), err, out)
			return 1
		}
	}
	return m.Run()
}

const waitLimit = 20 * time.Second

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitForWithin(t, waitLimit, what, cond)
}

func waitForWithin(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// groupAlive reports whether any process of the group is left.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}

// killGroupAtEnd kills what a failed test left running.
func killGroupAtEnd(t *testing.T, pgid int) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
}

// nodeOS and nodeArch are the names a daemon tells its apps (Node's: the names the servers have always received).
func nodeOS() string { return runtime.GOOS }

func nodeArch() string {
	if runtime.GOARCH == "amd64" {
		return "x64"
	}
	return runtime.GOARCH
}

// syncBuffer is the output of a daemon, read while it is written.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sandbox is a temporary home for one daemon: its config, state and working directories.
type sandbox struct {
	root   string
	config string
	state  string
	work   string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	// Short names: whatever lies under the root may hold a socket.
	root, err := os.MkdirTemp("", "agentd-it-")
	if err != nil {
		t.Fatal(err)
	}
	// What the daemon names its working directory is the kernel's name for it.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	b := &sandbox{root: root, config: filepath.Join(root, "config"), state: filepath.Join(root, "state"), work: filepath.Join(root, "work")}
	if err := os.MkdirAll(b.work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return b
}

func (b *sandbox) configFile() string { return filepath.Join(b.config, "agentd", "daemon.json") }

func (b *sandbox) sessionsFile() string { return filepath.Join(b.state, "agentd", "sessions.json") }

// agentdHome is the daemon's own home in the sandbox (AGENTD_HOME): where its token and its ACP address are kept.
func (b *sandbox) agentdHome() string { return filepath.Join(b.root, "mc") }

// acpToken is the token the daemon made, once it has.
func (b *sandbox) acpToken(t *testing.T) string {
	t.Helper()
	var token string
	waitFor(t, "the daemon's ACP token", func() bool {
		var err error
		token, err = acpauth.Load(b.agentdHome())
		return err == nil
	})
	return token
}

// writeApps saves these apps (and browser grants), in this order.
func (b *sandbox) writeApps(t *testing.T, apps []registry.App, browsers []registry.BrowserGrant) {
	t.Helper()
	reg := registry.Empty()
	reg.Apps = append(reg.Apps, apps...)
	reg.Browsers = append(reg.Browsers, browsers...)
	if err := registry.Save(b.configFile(), reg); err != nil {
		t.Fatal(err)
	}
}

// writeServers saves apps at these origins, all with the test token.
func (b *sandbox) writeServers(t *testing.T, origins ...string) {
	t.Helper()
	apps := make([]registry.App, len(origins))
	for i, o := range origins {
		apps[i] = registry.App{Server: o, Token: testToken}
	}
	b.writeApps(t, apps, nil)
}

// unset in an environment removes the variable.
const unset = "\x00"

// environ is the environment of a process in the sandbox: this process's own, the sandbox's directories, the fake codex
// and no tray, with `extra` on top.
func (b *sandbox) environ(extra map[string]string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range map[string]string{
		"XDG_CONFIG_HOME":    b.config,
		"XDG_STATE_HOME":     b.state,
		"HOME":               b.root,
		"AGENTD_HOME":        filepath.Join(b.root, "mc"),
		"AGENTD_AGENT_CODEX": "true",
		"AGENTD_LOCAL_PORT":  "0",
		"AGENTD_TRAY":        "off",
	} {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		if v != unset {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// daemonProc is a running `agentd`; its output is collected.
type daemonProc struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	exited chan struct{}
	code   int
}

func (d *daemonProc) pid() int { return d.cmd.Process.Pid }

func (d *daemonProc) hasExited() bool {
	select {
	case <-d.exited:
		return true
	default:
		return false
	}
}

// exitCode waits for the daemon to end (it is killed after `limit`) and returns its exit code.
func (d *daemonProc) exitCode(t *testing.T, limit time.Duration) int {
	t.Helper()
	select {
	case <-d.exited:
		return d.code
	case <-time.After(limit):
		_ = syscall.Kill(-d.pid(), syscall.SIGKILL)
		t.Fatalf("the daemon is still running after %s; output:\n%s", limit, d.out)
		return -1
	}
}

// signal sends sig to the daemon alone.
func (d *daemonProc) signal(sig syscall.Signal) { _ = syscall.Kill(d.pid(), sig) }

// stop is SIGTERM and a wait; a daemon that ignores it is killed with its group.
func (d *daemonProc) stop() {
	if d.hasExited() {
		return
	}
	d.signal(syscall.SIGTERM)
	select {
	case <-d.exited:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-d.pid(), syscall.SIGKILL)
		<-d.exited
	}
}

func (d *daemonProc) says(text string) bool { return strings.Contains(d.out.String(), text) }

// startDaemon starts `agentd args...` in the sandbox's working directory, in a group of its own; the test ends it.
func startDaemon(t *testing.T, box *sandbox, args []string, env map[string]string) *daemonProc {
	t.Helper()
	d := startWith(t, box, args, env, nil)
	t.Cleanup(d.stop)
	return d
}

func startWith(t *testing.T, box *sandbox, args []string, env map[string]string, extraFiles []*os.File) *daemonProc {
	t.Helper()
	cmd := exec.Command(binaries.agentd, args...)
	cmd.Dir = box.work
	cmd.Env = box.environ(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = extraFiles
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemonProc{cmd: cmd, out: out, exited: make(chan struct{})}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	go func() {
		err := cmd.Wait()
		d.code = cmd.ProcessState.ExitCode()
		_ = err
		close(d.exited)
	}()
	return d
}

// runCommand runs `agentd args...` to the end in the sandbox and returns its exit code and output.
func runCommand(t *testing.T, box *sandbox, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binaries.agentd, args...)
	cmd.Dir = box.work
	cmd.Env = box.environ(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && cmd.ProcessState == nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

// sh is the agent command of a shell script.
func sh(script string) string { return "sh -c '" + script + "'" }

// rig is a fake app, a sandbox and a daemon connected to each other.
type rig struct {
	app *fakeApp
	box *sandbox
	d   *daemonProc
}

// startRig starts a daemon paired with a fresh app, whose "claude" agent is `claude`, and waits for its hello.
func startRig(t *testing.T, claude string, env map[string]string, args ...string) *rig {
	t.Helper()
	r := &rig{app: startApp(t), box: newSandbox(t)}
	r.box.writeServers(t, r.app.origin())
	r.start(t, claude, env, args...)
	r.app.waitHellos(t, 1)
	return r
}

// start starts a daemon in the rig's sandbox.
func (r *rig) start(t *testing.T, claude string, env map[string]string, args ...string) {
	t.Helper()
	merged := map[string]string{}
	if claude != "" {
		merged["AGENTD_AGENT_CLAUDE"] = claude
	}
	for k, v := range env {
		merged[k] = v
	}
	r.d = startDaemon(t, r.box, args, merged)
}

// waitGroupGone waits for the agent's group to be gone.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	waitFor(t, "the group to be gone", func() bool { return !groupAlive(pgid) })
}

// waitForNothing fails when cond holds within a second and a half: the daemon is to stay as it is.
func waitForNothing(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("%s: it did not", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
