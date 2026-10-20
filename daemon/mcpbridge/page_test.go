package mcpbridge

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// page is the page end of a binding: it keeps the lines its bridge sent.
type page struct {
	mu    sync.Mutex
	lines []string
}

func (p *page) Send(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, line)
}
func (*page) Backlog() <-chan struct{} { return nil }

func (p *page) got() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

func relay(t *testing.T) (*pagetools.McpRelay, *page, *pagetools.Binding) {
	t.Helper()
	r, err := pagetools.Listen(shortDir(t) + "/mcp.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	p := &page{}
	return r, p, r.Bind(p)
}

func pageRun(t *testing.T, env home.Env) *run {
	t.Helper()
	return start(t, Deps{Host: hostWithEnv(env), CloseWait: 200 * time.Millisecond}, RunPageBridge)
}

func envOf(r *pagetools.McpRelay, b *pagetools.Binding) home.Env {
	return home.Env{pagetools.McpSocketEnv: r.Path(), pagetools.McpTokenEnv: b.Token()}
}

func TestPageBridgeRelaysBothWaysAndDropsEmptyLines(t *testing.T) {
	r, p, b := relay(t)
	run := pageRun(t, envOf(r, b))
	run.send(t, `{"id":1}`+"\n\n \n"+`{"id":2}`+"\n")
	waitFor(t, "the page's lines", func() bool { return len(p.got()) == 2 })
	if got := p.got(); got[0] != `{"id":1}` || got[1] != `{"id":2}` {
		t.Fatalf("the page got %q", got)
	}
	waitFor(t, "the bridge to be bound", func() bool { return b.ToBridge(`{"id":1,"result":{}}`) })
	b.ToBridge(`{"id":2,"result":{}}`)
	waitFor(t, "the page's answers on stdout", func() bool { return strings.Count(run.stdout.String(), "\n") >= 2 })
	if got := run.stdout.String(); !strings.HasSuffix(got, `{"id":2,"result":{}}`+"\n") || !strings.HasPrefix(got, `{"id":1,"result":{}}`+"\n") {
		t.Fatalf("stdout = %q", got)
	}
	_ = run.stdin.Close()
	if code := run.exit(t); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}

func TestPageBridgeWithATokenTheDaemonRefusesExitsWith1AndTheReason(t *testing.T) {
	r, p, b := relay(t)
	run := pageRun(t, home.Env{pagetools.McpSocketEnv: r.Path(), pagetools.McpTokenEnv: "not-" + b.Token()})
	if code := run.exit(t); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := run.stderr.String(); !strings.Contains(got, "agentd-mcp: the daemon refused the bridge (no open session has this token") {
		t.Fatalf("stderr = %q", got)
	}
	if len(p.got()) != 0 {
		t.Fatalf("the page got %q from a refused bridge", p.got())
	}
}

func TestPageBridgeWithoutItsEnvExitsWith1AndNamesBothVariables(t *testing.T) {
	for name, env := range map[string]home.Env{
		"no socket": {pagetools.McpTokenEnv: "t"},
		"no token":  {pagetools.McpSocketEnv: "/x"},
		"empty":     {pagetools.McpSocketEnv: "", pagetools.McpTokenEnv: ""},
		"nothing":   nil,
	} {
		run := pageRun(t, env)
		if code := run.exit(t); code != 1 {
			t.Errorf("%s: exit = %d, want 1", name, code)
		}
		if got := run.stderr.String(); !strings.Contains(got, "AGENTD_MCP_SOCKET and AGENTD_MCP_TOKEN") {
			t.Errorf("%s: stderr = %q", name, got)
		}
	}
}

func TestPageBridgeWithADaemonThatIsNotThereExitsWith1(t *testing.T) {
	run := pageRun(t, home.Env{pagetools.McpSocketEnv: shortDir(t) + "/none.sock", pagetools.McpTokenEnv: "t"})
	if code := run.exit(t); code != 1 || !strings.Contains(run.stderr.String(), "cannot reach the daemon at") {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}

func TestPageBridgeEndsWith1WhenTheDaemonEndsTheSession(t *testing.T) {
	r, p, b := relay(t)
	run := pageRun(t, envOf(r, b))
	run.send(t, "hello\n")
	waitFor(t, "the page's line", func() bool { return len(p.got()) == 1 })
	b.End()
	if code := run.exit(t); code != 1 || !strings.Contains(run.stderr.String(), "the daemon ended the bridge") {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}

func TestPageBridgeEndsWith1WhenTheRelayCloses(t *testing.T) {
	r, _, b := relay(t)
	run := pageRun(t, envOf(r, b))
	waitFor(t, "the bridge to be bound", func() bool { return b.ToBridge("x") })
	r.Close()
	if code := run.exit(t); code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}

func TestPageBridgeSendsALineOfItsClientThatReachedTheDaemonBeforeStdinEnded(t *testing.T) {
	r, p, b := relay(t)
	run := pageRun(t, envOf(r, b))
	run.send(t, "last\n")
	_ = run.stdin.Close()
	if code := run.exit(t); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
	waitFor(t, "the last line", func() bool { return len(p.got()) == 1 })
}

func TestPageBridgeEndsWith1OnALineOfItsClientOverTheCap(t *testing.T) {
	r, _, b := relay(t)
	in := &repeating{left: proc.MaxLineBytes + 1}
	var stdout, stderr buffer
	code := make(chan int, 1)
	go func() {
		code <- RunPageBridge(Deps{Stdin: in, Stdout: &stdout, Stderr: &stderr, Host: hostWithEnv(envOf(r, b))})
	}()
	select {
	case c := <-code:
		if c != 1 || !strings.Contains(stderr.String(), "the client sent a line longer than 32 MiB") {
			t.Fatalf("exit = %d, stderr = %q", c, stderr.String())
		}
	case <-time.After(wait * 3):
		t.Fatal("the bridge did not exit")
	}
}

func TestPageBridgeEndsWith1OnALineOfThePageOverTheCap(t *testing.T) {
	r, _, b := relay(t)
	run := pageRun(t, envOf(r, b))
	waitFor(t, "the bridge to be bound", func() bool { return b.ToBridge("ready") })
	go b.ToBridge(strings.Repeat("x", proc.MaxLineBytes+1))
	if code := run.exit(t); code != 1 || !strings.Contains(run.stderr.String(), "the page sent a line longer than 32 MiB") {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}

func TestPageBridgeExitsWith0AfterCloseWaitWhenTheDaemonKeepsItsEndOpenAfterStdinEnded(t *testing.T) {
	path := shortDir(t) + "/silent.sock"
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, pagetools.BridgeOK()+"\n")
		_, _ = io.Copy(io.Discard, c) // reads until the bridge closes; never closes its own end first
	}()
	run := pageRun(t, home.Env{pagetools.McpSocketEnv: path, pagetools.McpTokenEnv: "t"})
	_ = run.stdin.Close()
	if code := run.exit(t); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, run.stderr.String())
	}
}
