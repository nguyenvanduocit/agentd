package goservertest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// tb is what StartDaemon asks of the test: the part of testing.TB it uses, so that a failure of StartDaemon can be
// observed by this package's own tests. *testing.T and *testing.B satisfy it.
type tb interface {
	Helper()
	Cleanup(func())
	TempDir() string
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Skip(args ...any)
}

// App is the app server under test, as the daemon sees it.
type App struct {
	// URL is the origin the daemon dials: the URL of the web server that serves goserver (httptest.Server.URL), with
	// no path. The daemon accepts https, or http on a loopback address.
	URL string
	// Server is the goserver.Server mounted on that web server. StartDaemon asks it for a pairing code and watches it
	// to see the daemon come online.
	Server *goserver.Server
	// User is the id of the user the daemon is paired for.
	User string
}

// Option changes how StartDaemon runs the daemon.
type Option func(*config)

type config struct {
	name    string
	code    string
	timeout time.Duration
	env     map[string]string
	agents  map[string]string
}

// WithName renames the daemon in the app once it is online (goserver Rename), so that a test with two daemons can tell
// them apart. Without it the daemon has the name the app gave it from its host name.
func WithName(name string) Option { return func(c *config) { c.name = name } }

// WithPairingCode pairs with this code instead of asking Server.For(User).StartPairing() for one: for an app that
// hands out its codes through its own API.
func WithPairingCode(code string) Option { return func(c *config) { c.code = code } }

// WithTimeout is how long StartDaemon waits for the daemon to come online (default 30 s).
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithEnv sets an environment variable of the daemon, after the ones StartDaemon sets (so it can replace them).
func WithEnv(key, value string) Option { return func(c *config) { c.env[key] = value } }

// WithAgentCommand replaces the program that runs when the app opens the agent "claude", "codex" or "grok" with this
// command line (a program and its arguments; quote an argument that holds a space). It stands for the real agent: the
// daemon counts the agent as ready. Without it all three run the FakeAgent.
func WithAgentCommand(agent, commandLine string) Option {
	return func(c *config) { c.agents[strings.ToLower(agent)] = commandLine }
}

// Daemon is a real agentd, paired with the app and online. It stops when the test ends.
type Daemon struct {
	t    tb
	app  App
	cfg  config
	bin  tools
	dir  string
	log  *logBuffer
	id   string
	name string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{} // closed when cmd has exited
}

// StartDaemon pairs a real agentd with the app and returns when the app sees it online. The daemon is built from the
// agentd module your app depends on (the first time per version: see the package documentation for the cost), runs
// as a child of the test with a home, config and state directory of its own and a port of its own, and runs the
// FakeAgent in place of Claude, Codex and Grok. It never reads or writes your real ~/.config/agentd or ~/.agentd, never
// installs a service, and is stopped, with its process group, when the test ends.
//
// A failure ends the test with a message that says what was tried and carries the daemon's own log, because a daemon
// that does not come online says why only there.
func StartDaemon(t testing.TB, app App, opts ...Option) *Daemon { return start(t, app, opts...) }

func start(t tb, app App, opts ...Option) *Daemon {
	t.Helper()
	if !supported {
		t.Skip("goservertest: agentd does not run on Windows; run these tests in WSL")
		return nil
	}
	if app.Server == nil || app.URL == "" || app.User == "" {
		t.Fatalf("goservertest: App needs URL, Server and User (got URL %q, Server set %v, User %q)", app.URL, app.Server != nil, app.User)
		return nil
	}
	cfg := config{timeout: 30 * time.Second, env: map[string]string{}, agents: map[string]string{}}
	for _, o := range opts {
		o(&cfg)
	}
	bin := binaries(t)
	d := &Daemon{t: t, app: app, cfg: cfg, bin: bin, dir: t.TempDir(), log: &logBuffer{}}
	for _, sub := range []string{"home", "agentd", "config", "state", "work"} {
		if err := os.MkdirAll(filepath.Join(d.dir, sub), 0o755); err != nil {
			t.Fatalf("goservertest: %v", err)
			return nil
		}
	}
	t.Cleanup(d.kill)

	d.pair()
	d.launch()
	d.awaitOnline()
	if cfg.name != "" {
		if _, err := app.Server.For(app.User).Rename(d.id, cfg.name); err != nil {
			t.Fatalf("goservertest: renaming the daemon to %q: %v", cfg.name, err)
			return nil
		}
		d.name = cfg.name
	}
	return d
}

// ID is the daemon's id in the app: what goserver's Open, Revoke and the others take.
func (d *Daemon) ID() string { return d.id }

// Name is the name the app lists the daemon under.
func (d *Daemon) Name() string { return d.Info().Name }

// Info is the app's current view of the daemon (goserver List): its state ("online" or "offline"), agents, version.
func (d *Daemon) Info() goserver.Info {
	d.t.Helper()
	info, ok := d.lookup()
	if !ok {
		d.t.Fatalf("goservertest: the app does not list the daemon %s any more (revoked?)", d.id)
	}
	return info
}

// Dir is the daemon's private directory: its HOME, config and state, and the working directory of its agents. It is
// removed with the test's temporary files.
func (d *Daemon) Dir() string { return d.dir }

// PID is the process id of the running daemon, which leads a process group of its own; 0 when it is stopped.
func (d *Daemon) PID() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil || d.cmd.Process == nil {
		return 0
	}
	return d.cmd.Process.Pid
}

// Log is everything the daemon has printed so far (its stdout and stderr).
func (d *Daemon) Log() string { return d.log.String() }

// Stop ends the daemon the way a service manager would (SIGTERM, which makes it end its agents and leave), waits for
// it to exit, and returns once the app lists it as offline: a test of how the app behaves with a daemon gone can go
// on from there. Stop twice is fine.
func (d *Daemon) Stop() {
	d.t.Helper()
	d.terminate(10 * time.Second)
	if !d.waitFor("offline", 10*time.Second) {
		d.t.Fatalf("goservertest: 10 s after Stop the app still lists the daemon as %q (the agentd process has exited: %v).\n%s",
			d.Info().State, d.exited(), d.failureTail())
	}
}

// exited is whether the daemon process has ended (or was never started).
func (d *Daemon) exited() bool {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Restart starts the daemon again with the same home, so that it is the same paired daemon, and returns when it is
// online. It stops the daemon first if it runs.
func (d *Daemon) Restart() {
	d.t.Helper()
	d.Stop()
	d.log.Write([]byte("--- goservertest: restart ---\n"))
	d.launch()
	d.awaitOnline()
}

// lookup finds the daemon in the app's list.
func (d *Daemon) lookup() (goserver.Info, bool) {
	list, err := d.app.Server.For(d.app.User).List()
	if err != nil {
		return goserver.Info{}, false
	}
	for _, info := range list.Daemons {
		if info.ID == d.id {
			return info, true
		}
	}
	return goserver.Info{}, false
}

// pair runs `agentd pair` against the app with a fresh code, as the install command a user is shown would.
func (d *Daemon) pair() {
	d.t.Helper()
	me := d.app.Server.For(d.app.User)
	code := d.cfg.code
	if code == "" {
		list, err := me.StartPairing()
		if err != nil || list.Pairing.Code == "" {
			d.t.Fatalf("goservertest: asking the app for a pairing code (Server.For(%q).StartPairing()): %v (code %q)", d.app.User, err, list.Pairing.Code)
			return
		}
		code = list.Pairing.Code
	}
	before := map[string]bool{}
	if list, err := me.List(); err == nil {
		for _, info := range list.Daemons {
			before[info.ID] = true
		}
	}
	cmd := exec.Command(d.bin.agentd, "pair", "--app", d.app.URL, "--code", code)
	cmd.Env = d.env()
	cmd.Dir = filepath.Join(d.dir, "work")
	out, err := cmd.CombinedOutput()
	if err != nil {
		d.t.Fatalf("goservertest: `agentd pair --app %s --code %s` failed: %v\n%s\n"+
			"hint: pairing is a POST to %s/agentd/pair on your app, which goserver's Mount registers; the code is one-time and expires",
			d.app.URL, code, err, strings.TrimSpace(string(out)), d.app.URL)
		return
	}
	d.log.Write([]byte("--- agentd pair ---\n" + string(out)))
	list, err := me.List()
	if err != nil {
		d.t.Fatalf("goservertest: listing the user's daemons after pairing: %v", err)
		return
	}
	for _, info := range list.Daemons {
		if !before[info.ID] {
			d.id = info.ID
			return
		}
	}
	d.t.Fatalf("goservertest: `agentd pair` succeeded but the app lists no new daemon for user %q (%d listed)", d.app.User, len(list.Daemons))
}

// launch starts `agentd run` in a process group of its own.
func (d *Daemon) launch() {
	d.t.Helper()
	cmd := exec.Command(d.bin.agentd, "run", "--dir", filepath.Join(d.dir, "work"))
	cmd.Env = d.env()
	cmd.Dir = filepath.Join(d.dir, "work")
	cmd.Stdout, cmd.Stderr = d.log, d.log
	// A program the daemon started can keep its output pipe open after the daemon is gone; do not wait for it for ever.
	cmd.WaitDelay = 3 * time.Second
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		d.t.Fatalf("goservertest: starting %s: %v", d.bin.agentd, err)
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	d.mu.Lock()
	d.cmd, d.done = cmd, done
	d.mu.Unlock()
}

// awaitOnline waits until the app lists the daemon as online, or fails with the daemon's log.
func (d *Daemon) awaitOnline() {
	d.t.Helper()
	deadline := time.Now().Add(d.cfg.timeout)
	for {
		info, ok := d.lookup()
		switch {
		case ok && info.Incompatible != "":
			d.t.Fatalf("goservertest: the daemon connected but the app takes it for incompatible: %s\n%s", info.Incompatible, d.failureTail())
			return
		case ok && info.State == "online":
			return
		}
		d.mu.Lock()
		done := d.done
		d.mu.Unlock()
		select {
		case <-done:
			d.t.Fatalf("goservertest: agentd exited before it was online.\n%s", d.failureTail())
			return
		default:
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("goservertest: the daemon %s is not online in the app after %s.\napp: %s, user: %q\n%s%s",
				d.id, d.cfg.timeout, d.app.URL, d.app.User, d.hint(), d.failureTail())
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitFor is whether the app lists the daemon in this state within the time.
func (d *Daemon) waitFor(state string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if info, ok := d.lookup(); ok && info.State == state {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hint says what usually is wrong when the log shows it.
func (d *Daemon) hint() string {
	log := d.log.String()
	switch {
	case strings.Contains(log, "bad handshake"):
		return "hint: the app answers the daemon's Socket.IO handshake with an error. Serve the Socket.IO handler next to " +
			"goserver's Mount, for example mux.Handle(\"/socket.io/\", io.ServeHandler(nil)), and give the socket server " +
			"SetMaxHttpBufferSize(goserver.MaxMessageBytes) (examples/minimal-app/main.go does both).\n"
	case strings.Contains(log, "unauthorized"):
		return "hint: the app refused the daemon's token. Pairing and the socket must use the same Options.Tokens store.\n"
	}
	return ""
}

// failureTail is the end of the daemon's log, for a failure message.
func (d *Daemon) failureTail() string {
	lines := strings.Split(strings.TrimRight(d.log.String(), "\n"), "\n")
	if len(lines) > 40 {
		lines = append([]string{fmt.Sprintf("... (%d earlier lines)", len(lines)-40)}, lines[len(lines)-40:]...)
	}
	return "--- agentd log ---\n" + strings.Join(lines, "\n")
}

// terminate ends the daemon: SIGTERM to its process group, SIGKILL if it has not gone after grace. It waits for it.
func (d *Daemon) terminate(grace time.Duration) {
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	select {
	case <-done:
		return
	default:
	}
	terminateGroup(pid)
	select {
	case <-done:
	case <-time.After(grace):
		killGroup(pid)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			d.t.Logf("goservertest: agentd pid %d did not exit after SIGKILL", pid)
		}
	}
	// Whatever else stayed in the daemon's group goes with it.
	killGroup(pid)
}

// kill is the cleanup of the test: it ends the daemon and never fails the test.
func (d *Daemon) kill() { d.terminate(10 * time.Second) }

// env is the daemon's environment: the test's, minus every AGENTD_* variable and the home and XDG variables, plus a
// home of its own.
func (d *Daemon) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "AGENTD_") || strings.HasPrefix(k, "XDG_") || k == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	fake := quoteArg(d.bin.fakeAgent)
	set := map[string]string{
		"HOME":                filepath.Join(d.dir, "home"),
		"AGENTD_HOME":         filepath.Join(d.dir, "agentd"),
		"XDG_CONFIG_HOME":     filepath.Join(d.dir, "config"),
		"XDG_STATE_HOME":      filepath.Join(d.dir, "state"),
		"AGENTD_LOCAL_PORT":   "0",
		"AGENTD_ADAPTERS":     "off",
		"AGENTD_TRAY":         "off",
		"AGENTD_RELEASE_URL":  "",
		"AGENTD_AGENT_CLAUDE": fake,
		"AGENTD_AGENT_CODEX":  fake,
		"AGENTD_AGENT_GROK":   fake,
	}
	for agent, line := range d.cfg.agents {
		set["AGENTD_AGENT_"+strings.ToUpper(agent)] = line
	}
	for k, v := range d.cfg.env {
		set[k] = v
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

// quoteArg quotes a program path for the daemon's command-line seam when it holds a space.
func quoteArg(s string) string {
	if !strings.ContainsAny(s, " \t'\"") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// FakeAgent is the path of the program that stands in for Claude, Codex and Grok in StartDaemon, built the first time.
// It reads one JSON-RPC message per line on its stdin; it answers `initialize` on its stdout with the protocolVersion it
// was sent (`{"jsonrpc":"2.0","id":<id>,"result":{"protocolVersion":<n>}}`) and ignores every other line. To test an
// agent that does more, give StartDaemon WithAgentCommand with a program of your own.
func FakeAgent(t testing.TB) string {
	t.Helper()
	return binaries(t).fakeAgent
}

// logBuffer is a writer the daemon's output goes to and a test reads from.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
