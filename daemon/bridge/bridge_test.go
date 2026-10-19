package bridge

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/link"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	serverA = "https://a.example.com"
	serverB = "https://b.example.com"
)

func twoApps(t *testing.T) *string {
	t.Helper()
	return configFile(t, `{"apps":[{"server":"`+serverA+`","token":"token-a"},{"server":"`+serverB+`","token":"token-b"}],"browsers":[]}`)
}

func oneApp(t *testing.T) *string {
	t.Helper()
	return configFile(t, `{"apps":[{"server":"`+serverA+`","token":"token-a"}],"browsers":[]}`)
}

// fakeServer stands in for the link to an app's server: it records what the bridge sends and lets a test play the
// link's events and the server's mcpOut calls.
type fakeServer struct {
	mu      sync.Mutex
	opts    link.Options
	handler link.ClientHandler
	events  link.Events
	lines   []string
	started bool
	closed  []time.Duration
	mcpErr  error
	connErr error
	onClose func()
}

func (f *fakeServer) connect(o link.Options, h link.ClientHandler, ev link.Events) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connErr != nil {
		return nil, f.connErr
	}
	f.opts, f.handler, f.events = o, h, ev
	return f, nil
}

func (f *fakeServer) Start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
}

func (f *fakeServer) Close(grace time.Duration) {
	f.mu.Lock()
	f.closed = append(f.closed, grace)
	hook := f.onClose
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (f *fakeServer) Mcp(line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mcpErr != nil {
		return f.mcpErr
	}
	f.lines = append(f.lines, line)
	return nil
}

func (f *fakeServer) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func (f *fakeServer) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closed)
}

func (f *fakeServer) ev() link.Events {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events
}

func (f *fakeServer) startedNow() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func session(t *testing.T, f *fakeServer, cmd cli.MCP, closeWait time.Duration) *run {
	t.Helper()
	return start(t, Deps{Host: hostWithEnv(nil), Connect: f.connect, CloseWait: closeWait},
		func(d Deps) int { return RunBridge(cmd, d) })
}

func TestTheBridgeConnectsToTheAppWithItsTokenAsAnMcpBridgeOfTheSession(t *testing.T) {
	f := &fakeServer{}
	session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	want := link.Options{Server: serverA, Auth: map[string]string{"role": "mcp", "token": "token-a", "session": "s1"}, MaxRetries: 3}
	if !reflect.DeepEqual(f.opts, want) {
		t.Fatalf("options = %+v, want %+v", f.opts, want)
	}
}

func TestTheAppIsTheNamedOneNotTheOther(t *testing.T) {
	f := &fakeServer{}
	app := serverB
	session(t, f, cli.MCP{Session: "s1", App: &app, Config: twoApps(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	if f.opts.Server != serverB || f.opts.Auth["token"] != "token-b" {
		t.Fatalf("connected to %s with %v, want the app b", f.opts.Server, f.opts.Auth)
	}
}

func TestSeveralAppsAndNoneNamedIsAnErrorNotAGuess(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: twoApps(t)}, 0)
	if code := r.exit(t); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "several apps are paired: pass --app <url>") || f.startedNow() {
		t.Fatalf("stderr = %q, started = %v", r.stderr.String(), f.startedNow())
	}
}

func TestNothingPairedIsAnError(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: configFile(t, `{"apps":[],"browsers":[]}`)}, 0)
	if code := r.exit(t); code != 1 || !strings.Contains(r.stderr.String(), "not paired") {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestARegistryThatCannotBeReadIsAnErrorWithItsFileName(t *testing.T) {
	f := &fakeServer{}
	file := configFile(t, `not json`)
	r := session(t, f, cli.MCP{Session: "s1", Config: file}, 0)
	if code := r.exit(t); code != 1 || !strings.Contains(r.stderr.String(), *file) {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestAServerThatIsNotAnOriginIsAnError(t *testing.T) {
	f := &fakeServer{connErr: errors.New("the server is not an http(s) origin")}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	if code := r.exit(t); code != 1 || !strings.Contains(r.stderr.String(), "not an http(s) origin") {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestLinesGoToTheServerInOrderAndAnEmptyLineIsNotForwarded(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnConnect()
	r.send(t, `{"id":1}`+"\n\n  \n"+`{"id":`)
	r.send(t, `2}`+"\r\n")
	waitFor(t, "both lines", func() bool { return len(f.got()) == 2 })
	if got := f.got(); !reflect.DeepEqual(got, []string{`{"id":1}`, `{"id":2}`}) {
		t.Fatalf("the server got %q", got)
	}
}

func TestLinesSentBeforeTheSocketIsUpWaitAndGoOutOnConnect(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	r.send(t, "a\nb\n")
	time.Sleep(50 * time.Millisecond)
	if got := f.got(); len(got) != 0 {
		t.Fatalf("lines went out before the connection: %q", got)
	}
	f.ev().OnConnect()
	waitFor(t, "the held lines", func() bool { return len(f.got()) == 2 })
	if got := f.got(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("the server got %q", got)
	}
}

func TestTheServersLinesAreWrittenToStdoutOnePerLine(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	for _, line := range []string{`{"id":1,"result":{}}`, `{"id":2}`} {
		if err := f.handler.HandleMcpOut(context.Background(), line); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.stdout.String(); got != "{\"id\":1,\"result\":{}}\n{\"id\":2}\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestTheEndOfStdinAfterAnAnswerClosesTheLinkAndExitsWith0(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnConnect()
	r.send(t, `{"id":1}`+"\n")
	_ = f.handler.HandleMcpOut(context.Background(), `{"id":1,"result":{}}`)
	_ = r.stdin.Close()
	if code := r.exit(t); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
	if got := f.got(); !reflect.DeepEqual(got, []string{`{"id":1}`}) {
		t.Fatalf("the server got %q", got)
	}
	// the link's own close is reported as "io client disconnect" and is not a failure
	f.ev().OnDisconnect("io client disconnect")
	if strings.Contains(r.stderr.String(), "connection ended") || f.closes() == 0 {
		t.Fatalf("stderr = %q, closes = %d", r.stderr.String(), f.closes())
	}
}

func TestLinesQueuedBeforeTheConnectionAreHandedOverBeforeTheBridgeExitsAfterStdinEnded(t *testing.T) {
	f := &fakeServer{}
	var atClose []string
	f.onClose = func() { atClose = f.got() }
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	r.send(t, "early\n")
	_ = r.stdin.Close()
	time.Sleep(50 * time.Millisecond)
	f.ev().OnConnect()
	if code := r.exit(t); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !reflect.DeepEqual(atClose, []string{"early"}) {
		t.Fatalf("the server had %q when the link closed, want the early line", atClose)
	}
}

func TestStdinEndedAndNeverConnectedExitsWith0AfterTheCloseWait(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 100*time.Millisecond)
	waitFor(t, "the link to start", f.startedNow)
	_ = r.stdin.Close()
	if code := r.exit(t); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestAServerThatRefusesTheTokenEndsTheBridgeWith1AndSaysWhy(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnRejected("unauthorized")
	if code := r.exit(t); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if want := "agentd-mcp: " + serverA + " refused the bridge (unauthorized)"; !strings.Contains(r.stderr.String(), want) {
		t.Fatalf("stderr = %q, want %q", r.stderr.String(), want)
	}
}

func TestATroubleIsWarnedAndTheLinkKeepsTrying(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnTrouble("unavailable")
	waitFor(t, "the warning", func() bool { return strings.Contains(r.stderr.String(), "cannot reach "+serverA+" (unavailable)") })
	select {
	case code := <-r.code:
		t.Fatalf("the bridge exited with %d on a trouble the link retries", code)
	case <-time.After(50 * time.Millisecond):
	}
	f.ev().OnGiveUp()
	if code := r.exit(t); code != 1 {
		t.Fatalf("exit = %d, want 1 once the link gave up", code)
	}
}

func TestTheServerEndingTheConnectionEndsTheBridgeWith1(t *testing.T) {
	f := &fakeServer{}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnConnect()
	f.ev().OnDisconnect("io server disconnect")
	if code := r.exit(t); code != 1 || !strings.Contains(r.stderr.String(), "the server connection ended (io server disconnect)") {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestASendThatFailsEndsTheBridgeWith1(t *testing.T) {
	f := &fakeServer{mcpErr: errors.New("socket has been disconnected")}
	r := session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnConnect()
	r.send(t, "x\n")
	if code := r.exit(t); code != 1 || !strings.Contains(r.stderr.String(), "cannot send to "+serverA) {
		t.Fatalf("exit = %d, stderr = %q", code, r.stderr.String())
	}
}

func TestALineOverTheCapEndsTheBridgeWith1AfterTheLinesBeforeIt(t *testing.T) {
	f := &fakeServer{}
	in := io.MultiReader(strings.NewReader("first\n"), &repeating{left: proc.MaxLineBytes + 1})
	var stdout, stderr buffer
	code := make(chan int, 1)
	go func() {
		code <- RunBridge(cli.MCP{Session: "s1", Config: oneApp(t)}, Deps{Stdin: in, Stdout: &stdout, Stderr: &stderr, Host: hostWithEnv(nil), Connect: f.connect})
	}()
	waitFor(t, "the link to start", f.startedNow)
	f.ev().OnConnect()
	select {
	case c := <-code:
		if c != 1 || !strings.Contains(stderr.String(), "the client sent a line longer than 32 MiB") {
			t.Fatalf("exit = %d, stderr = %q", c, stderr.String())
		}
	case <-time.After(wait):
		t.Fatal("the bridge did not exit")
	}
}

func TestTheBridgeAnswersNoCallOfADaemon(t *testing.T) {
	f := &fakeServer{}
	session(t, f, cli.MCP{Session: "s1", Config: oneApp(t)}, 0)
	waitFor(t, "the link to start", f.startedNow)
	if _, err := f.handler.HandleOpen(context.Background(), "s", "claude"); err == nil {
		t.Fatal("a bridge opened an agent")
	}
	if err := f.handler.HandleStop(context.Background()); err == nil {
		t.Fatal("a bridge stopped the daemon")
	}
}
