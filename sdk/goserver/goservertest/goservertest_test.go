package goservertest

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// startApp is an app server as a developer writes it: a goserver.Server mounted on a web server that also serves
// Socket.IO. withSocketIO false leaves the /socket.io/ handler out, the mistake that makes a daemon pair and then loop
// on "bad handshake".
func startApp(t *testing.T, withSocketIO bool) App {
	t.Helper()
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(goserver.MaxMessageBytes)
	io := socket.NewServer(nil, opts)
	t.Cleanup(func() { io.Close(nil) })
	mux := http.NewServeMux()
	h.Mount(io, mux)
	if withSocketIO {
		mux.Handle("/socket.io/", io.ServeHandler(nil))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return App{URL: srv.URL, Server: h, User: "dev"}
}

// ask opens the agent on the daemon, sends initialize and returns the first line it answers.
func ask(t *testing.T, app App, d *Daemon) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := app.Server.Open(ctx, app.User, d.ID(), "claude")
	if err != nil {
		t.Fatalf("open claude: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(conn).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		return s
	case <-ctx.Done():
		t.Fatal("the agent did not answer initialize")
		return ""
	}
}

// descendants is the pids below pid, read from ps.
func descendants(t *testing.T, pid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	children := map[int][]int{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		p, _ := strconv.Atoi(f[0])
		pp, _ := strconv.Atoi(f[1])
		children[pp] = append(children[pp], p)
	}
	var all []int
	queue := []int{pid}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, c := range children[next] {
			all = append(all, c)
			queue = append(queue, c)
		}
	}
	return all
}

func running(pid int) bool { return processExists(pid) }

func skipWithoutDaemon(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("agentd does not run on Windows")
	}
	if testing.Short() {
		t.Skip("-short: StartDaemon compiles the daemon the first time")
	}
}

func TestStartDaemonBringsARealDaemonOnlineAndLeavesNothingBehind(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)

	var pid int
	var below []int
	t.Run("a daemon", func(t *testing.T) {
		d := StartDaemon(t, app)
		pid = d.PID()

		info := d.Info()
		if info.State != "online" || info.ID != d.ID() || d.ID() == "" {
			t.Fatalf("info = %+v", info)
		}
		var claude *goserver.Agent
		for i := range info.Agents {
			if info.Agents[i].Name == "claude" {
				claude = &info.Agents[i]
			}
		}
		if claude == nil || !claude.Ready {
			t.Fatalf("agents = %+v, want claude ready", info.Agents)
		}

		if got := ask(t, app, d); !strings.Contains(got, `"protocolVersion":1`) {
			t.Fatalf("the agent answered %q, want its answer to initialize", got)
		}
		below = descendants(t, pid)

		d.Stop()
		if st := d.Info().State; st != "offline" {
			t.Fatalf("after Stop the app sees %q, want offline", st)
		}
		if running(pid) {
			t.Fatalf("the daemon, pid %d, still runs after Stop", pid)
		}
	})

	// The subtest's cleanups have run: nothing may be left of what it started.
	if pid == 0 {
		t.Fatal("no daemon was started")
	}
	if running(pid) {
		t.Errorf("the daemon, pid %d, outlived the test", pid)
	}
	for _, p := range below {
		if running(p) {
			t.Errorf("pid %d, a process the daemon started, outlived the test", p)
		}
	}
}

func TestStartDaemonKeepsTheDaemonAwayFromTheDevelopersOwnConfig(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	before := fileTime(filepath.Join(realHome, ".config", "agentd", "daemon.json"))
	d := StartDaemon(t, app)
	if _, err := os.Stat(filepath.Join(d.Dir(), "config", "agentd", "daemon.json")); err != nil {
		t.Errorf("the daemon's own config is not under Dir(): %v", err)
	}
	if after := fileTime(filepath.Join(realHome, ".config", "agentd", "daemon.json")); after != before {
		t.Errorf("the developer's ~/.config/agentd/daemon.json changed (%v -> %v)", before, after)
	}
	if strings.HasPrefix(d.Dir(), realHome+string(filepath.Separator)+".agentd") {
		t.Errorf("Dir() %s is inside the developer's ~/.agentd", d.Dir())
	}
}

func fileTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func TestTwoDaemonsOfOneUserAreBothOnline(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)
	first := StartDaemon(t, app)
	second := StartDaemon(t, app, WithName("second"))
	if first.ID() == second.ID() {
		t.Fatalf("both daemons have the id %q", first.ID())
	}
	if second.Name() != "second" || second.Info().Name != "second" {
		t.Errorf("WithName: Name() = %q, the app lists %q", second.Name(), second.Info().Name)
	}
	if first.Info().State != "online" || second.Info().State != "online" {
		t.Errorf("states: %q and %q, want both online", first.Info().State, second.Info().State)
	}
	if n := len(app.Server.Online(app.User)); n != 2 {
		t.Errorf("the app has %d daemons online, want 2", n)
	}
	for _, d := range []*Daemon{first, second} {
		if got := ask(t, app, d); !strings.Contains(got, `"result"`) {
			t.Errorf("%s answered %q", d.Name(), got)
		}
	}
}

func TestRestartBringsTheSameDaemonBackOnline(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)
	d := StartDaemon(t, app)
	id, pid := d.ID(), d.PID()
	d.Stop()
	if st := d.Info().State; st != "offline" {
		t.Fatalf("after Stop: %q", st)
	}
	d.Restart()
	if d.ID() != id || d.Info().State != "online" {
		t.Errorf("after Restart: id %q (was %q), state %q", d.ID(), id, d.Info().State)
	}
	if d.PID() == pid {
		t.Errorf("Restart kept pid %d", pid)
	}
	if got := ask(t, app, d); !strings.Contains(got, `"result"`) {
		t.Errorf("answered %q", got)
	}
}

// recorder takes the place of the *testing.T of a failing StartDaemon, so that a test can read what a developer would
// read in their failure output.
type recorder struct {
	*testing.T
	msg string
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

// failureOf runs StartDaemon in a goroutine that its failing Fatalf ends, and returns what it said.
func failureOf(t *testing.T, app App, opts ...Option) string {
	t.Helper()
	r := &recorder{T: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartDaemon(r, app, opts...)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("StartDaemon neither returned nor failed within 60 s")
	}
	if r.msg == "" {
		t.Fatal("StartDaemon did not fail")
	}
	return r.msg
}

func TestAPairingTheAppRefusesFailsWithWhatThePairCommandSaid(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)
	msg := failureOf(t, app, WithPairingCode("000000"))
	for _, want := range []string{"agentd pair", "000000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, msg)
		}
	}
}

func TestAnAppThatDoesNotServeSocketIOFailsWithTheDaemonsLogAndAHint(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, false)
	msg := failureOf(t, app, WithTimeout(5*time.Second))
	if !strings.Contains(msg, "bad handshake") {
		t.Errorf("the failure does not carry the daemon's log:\n%s", msg)
	}
	if !strings.Contains(msg, "/socket.io/") {
		t.Errorf("the failure does not say what to serve:\n%s", msg)
	}
}

func TestFakeAgentIsABuiltProgramThatAnswersInitialize(t *testing.T) {
	skipWithoutDaemon(t)
	path := FakeAgent(t)
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":1}}` + "\n" + `{"id":8,"method":"nothing"}` + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != `{"jsonrpc":"2.0","id":7,"result":{"protocolVersion":1}}` {
		t.Errorf("fake agent printed %q", got)
	}
}

func TestWithAgentCommandReplacesTheFakeAgent(t *testing.T) {
	skipWithoutDaemon(t)
	app := startApp(t, true)
	script := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile read -r line; do echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"mine\":true}}'; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := StartDaemon(t, app, WithAgentCommand("claude", script))
	if got := ask(t, app, d); !strings.Contains(got, `"mine":true`) {
		t.Errorf("answered %q, want the script's answer", got)
	}
}

// runningDaemonsBelow is the daemons that run with a working directory below dir: the temporary directory of one test of
// this process, which no other process (another go test of this package, in parallel) shares.
func runningDaemonsBelow(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("ps", "-Ao", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, " run --dir ") && strings.Contains(line, dir) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

func TestAFailedStartDaemonLeavesNoDaemonBehind(t *testing.T) {
	skipWithoutDaemon(t)
	var below string
	t.Run("inner", func(t *testing.T) {
		below = filepath.Dir(t.TempDir()) // the directory every t.TempDir() of this test lives in, unique to this run
		app := startApp(t, false)         // no /socket.io/: the daemon pairs, runs, and never comes online
		if msg := failureOf(t, app, WithTimeout(2*time.Second)); msg == "" {
			t.Fatal("no failure")
		}
	})
	// The inner test's cleanups have run.
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := runningDaemonsBelow(t, below)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the failed StartDaemon left daemons running:\n%s", strings.Join(left, "\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestPrebuiltProgramsInGOSERVERTEST_BINAreUsedWithoutBuilding(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"agentd", "fake-agent"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(binDirEnv, dir)
	got, err := prepare()
	if err != nil || got.agentd != filepath.Join(dir, "agentd") || got.fakeAgent != filepath.Join(dir, "fake-agent") {
		t.Fatalf("prepare() = %+v, %v", got, err)
	}

	t.Setenv(binDirEnv, t.TempDir()) // a directory without the programs
	if _, err := prepare(); err == nil || !strings.Contains(err.Error(), binDirEnv) {
		t.Errorf("a GOSERVERTEST_BIN without the programs: %v, want an error that names the variable", err)
	}
}

func TestWithoutGoOnThePathTheFailureSaysWhatToDo(t *testing.T) {
	t.Setenv(binDirEnv, "")
	t.Setenv("PATH", t.TempDir())
	_, err := prepare()
	if err == nil {
		t.Fatal("prepare() found go on an empty PATH")
	}
	for _, want := range []string{"`go` is not on PATH", binDirEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

func TestQuoteArgQuotesWhatTheDaemonsCommandLineWouldSplit(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/fake-agent":       "/tmp/fake-agent",
		"/tmp/with space/agent": "'/tmp/with space/agent'",
		"/tmp/it's":             `'/tmp/it'\''s'`,
	} {
		if got := quoteArg(in); got != want {
			t.Errorf("quoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}
