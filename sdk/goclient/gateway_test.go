package goclient_test

// The client against a real gateway: daemon/gateway runs in this process with a fake consent answer and a fake agent
// (`cat`, or an ACP agent that is this test binary), the way daemon/gateway's own tests start one.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

const fakeAgentEnv = "GOCLIENT_FAKE_AGENT"

// TestMain makes this test binary the ACP agent of the tests that ask for one: the fake agent script runs it with
// GOCLIENT_FAKE_AGENT set.
func TestMain(m *testing.M) {
	if os.Getenv(fakeAgentEnv) == "1" {
		runFakeAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeAgent is an ACP agent that answers every prompt with "echo: <the prompt>", and lists the MCP servers its
// session/new was given in its first update.
type fakeAgent struct {
	acp.Agent
	conn *acp.AgentSideConnection
	mcp  []acp.McpServer
}

func (a *fakeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}, nil
}

func (a *fakeAgent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mcp = p.McpServers
	return acp.NewSessionResponse{SessionId: "fake-1"}, nil
}

func (a *fakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	text := ""
	for _, block := range p.Prompt {
		if block.Text != nil {
			text += block.Text.Text
		}
	}
	reply := "echo: " + text
	for _, s := range a.mcp {
		if s.Stdio != nil {
			reply += " [mcp " + s.Stdio.Name + "]"
		}
	}
	err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(reply)})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, err
}

func (a *fakeAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }

func runFakeAgent() {
	a := &fakeAgent{}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	select {
	case <-a.conn.Done():
	case <-stop:
	}
}

// ---- the rig: a real gateway ----

type noState struct{}

func (noState) Add(int)    {}
func (noState) Remove(int) {}

func noAdapter(agents.AdapterName) (agents.AdapterState, bool) { return agents.AdapterState{}, false }

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gc-") // a Unix socket path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func script(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(shortDir(t), "agent")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

// rig is a gateway whose dialog answers `answer`, with real sessions over the fake "claude" agent.
type rig struct {
	t        *testing.T
	gw       *gateway.Gateway
	port     int
	sessions *gateway.BrowserSessions
	bridge   string // the pages' agentd-mcp wrapper, as `opened` names it
	regFile  string

	mu      sync.Mutex
	reg     registry.Registry
	linked  map[string]bool
	asked   []consent.Question
	answer  consent.Answer
	revoked []string
}

// startRig runs `agent` (a command line) as the "claude" agent of every session.
func startRig(t *testing.T, agent string) *rig {
	t.Helper()
	dir := shortDir(t)
	binDir := filepath.Join(dir, "bin", "page")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relay, err := pagetools.Listen(filepath.Join(dir, "mcp.sock"))
	if err != nil {
		t.Fatal(err)
	}
	env := home.Env{"PATH": os.Getenv("PATH"), agents.SeamVariable("claude"): agent}
	find := func(program string) (string, bool) { return home.FindProgram(program, env) }
	hello := func() pagetools.Hello { return pagetools.NewHello("test", agents.AllStatuses(env, find, noAdapter)) }
	r := &rig{t: t, reg: registry.Empty(), linked: map[string]bool{}, answer: consent.AnswerAllow, bridge: filepath.Join(binDir, "agentd-mcp"), regFile: filepath.Join(dir, "registry.json")}
	r.sessions = gateway.NewBrowserSessions(gateway.SessionOptions{
		Fleet:         agents.FleetOptions{Cwd: dir, BinDir: binDir, Env: env, Adapter: noAdapter, State: noState{}},
		Hello:         hello,
		Relay:         relay,
		BridgeCommand: r.bridge,
	})
	deps := gateway.Deps{
		Version: "test",
		Agents:  func() []pagetools.AgentInfo { return hello().Agents },
		Registry: func() (registry.Registry, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.reg, nil
		},
		Ask: func(q consent.Question) consent.Decision {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.asked = append(r.asked, q)
			if r.answer != consent.AnswerAllow {
				return consent.Decision{Answer: r.answer}
			}
			return consent.Decision{Answer: r.answer, Folders: []string{"/work"}}
		},
		Linked: func(server string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.linked[server]
		},
		// The daemon's own pairing: it posts the code to <server>/agentd/pair and saves the app it gets a token from.
		ConnectServer: func(server, code string, folders []string) error {
			app, err := registry.Pair(context.Background(), http.DefaultClient, registry.MachineOf("host", "linux", "amd64"), server, code, r.regFile, folders)
			if err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reg = r.reg.AddApp(app)
			r.linked[app.Server] = true
			return nil
		},
		GrantBrowser: func(origin string, folders []string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reg = r.reg.AddBrowser(origin, time.Now(), folders)
			return nil
		},
		Revoke: func(origin string) error {
			r.mu.Lock()
			r.revoked = append(r.revoked, origin)
			r.reg = r.reg.RemoveOrigin(origin)
			delete(r.linked, origin)
			r.mu.Unlock()
			r.sessions.EndOrigin(origin)
			return nil
		},
		Upgrade: r.sessions.Upgrade,
	}
	own := func(consent.Connection) ([]int, error) { return []int{os.Getuid()}, nil }
	gw, err := gateway.Start(deps, 0, gateway.Options{Gate: consent.NewPeerGate(own, os.Getuid(), func(string) {})})
	if err != nil {
		t.Fatal(err)
	}
	r.gw, r.port = gw, gw.Port
	t.Cleanup(func() {
		r.sessions.Shutdown(2 * time.Second)
		_ = gw.Close()
	})
	return r
}

func (r *rig) daemon() goclient.Daemon { return goclient.Daemon{Port: r.port} }

func (r *rig) options() []goclient.Option { return []goclient.Option{goclient.WithPorts(r.port)} }

func (r *rig) questions() []consent.Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]consent.Question(nil), r.asked...)
}

func (r *rig) setAnswer(a consent.Answer) {
	r.mu.Lock()
	r.answer = a
	r.mu.Unlock()
}

const appOrigin = "http://localhost:5173"

func firstLine(t *testing.T, r io.Reader) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		return line
	case <-time.After(wait):
		t.Fatal("no line from the agent")
	}
	return ""
}

func localOf(t *testing.T, r *rig, mode goclient.Mode, origin string) *goclient.LocalDaemon {
	t.Helper()
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: origin, Mode: mode, Options: r.options(),
		StartPairing: func(context.Context) (string, error) { return "unused", nil }, CancelPairing: func() {}})
	if err != nil {
		t.Fatal(err)
	}
	return local
}

// ---- browser mode ----

func TestBrowserModeDetectConnectOpenWriteReadClose(t *testing.T) {
	r := startRig(t, "cat")
	local := localOf(t, r, goclient.ModeBrowser, appOrigin)

	// detect: a daemon is here, and this origin has no grant (so no agents are listed).
	local.Detect(bg)
	st := local.State()
	if !st.Found.Found || st.Found.Port != r.port || st.Found.Version != "test" || st.Found.Granted.Browser || st.Found.Agents != nil || !st.CanConnect {
		t.Fatalf("after Detect: %+v", st)
	}

	// connect: the dialog names the origin and the mode; Allow grants the browser, and the daemon lists its agents.
	if !local.Connect(bg) {
		t.Fatalf("Connect = false: %q", local.State().Error)
	}
	if q := r.questions(); len(q) != 1 || q[0].Origin != appOrigin || q[0].Mode != consent.ModeBrowser {
		t.Fatalf("the dialog asked %+v", q)
	}
	st = local.State()
	if !st.Found.Granted.Browser || st.CanConnect || st.Error != "" || len(st.Found.Agents) == 0 {
		t.Fatalf("after Connect: %+v", st)
	}
	var claude goclient.AgentInfo
	for _, a := range st.Found.Agents {
		if a.Name == "claude" {
			claude = a
		}
	}
	if claude != (goclient.AgentInfo{Name: "claude", Title: "Claude Code", Ready: true}) {
		t.Fatalf("claude = %+v", claude)
	}

	// open a session, write a line, read it back (`cat`), see it as a frame, close.
	s, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.Pid <= 0 || s.Cwd == "" {
		t.Fatalf("pid %d cwd %q", s.Pid, s.Cwd)
	}
	frames := make(chan goclient.Frame, 8)
	s.OnFrame(func(f goclient.Frame) { frames <- f })
	stdout := s.Stdout()
	if _, err := io.WriteString(s.Stdin(), `{"hello":"<world>"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := firstLine(t, stdout); got != `{"hello":"<world>"}`+"\n" {
		t.Fatalf("read back %q", got)
	}
	if f := <-frames; f.Kind != goclient.FrameOut || f.Data != `{"hello":"<world>"}` {
		t.Fatalf("frame = %+v", f)
	}
	if err := s.Write(`{"second":true}`); err != nil { // a page sends JSON-RPC lines: agentd refuses a line that is not one JSON object
		t.Fatal(err)
	}
	if got := firstLine(t, stdout); got != `{"second":true}`+"\n" {
		t.Fatalf("read back %q", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	waitUntil(t, "the agent to end", func() bool { return r.sessions.Size() == 0 })
}

func TestAnUnknownAgentIsRefusedWithTheDaemonsMessage(t *testing.T) {
	r := startRig(t, "cat")
	if res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin); !res.Granted {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
	_, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "rm -rf")
	if err == nil || !strings.Contains(err.Error(), "rm -rf") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnOriginWithoutAGrantCannotOpenASession(t *testing.T) {
	r := startRig(t, "cat")
	_, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err == nil || !strings.Contains(err.Error(), "cannot open a session") {
		t.Fatalf("error = %v", err)
	}
}

func TestDisconnectTakesTheGrantBackAndEndsTheOpenSession(t *testing.T) {
	r := startRig(t, "cat")
	local := localOf(t, r, goclient.ModeBrowser, appOrigin)
	local.Detect(bg)
	local.Connect(bg)
	s, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stdout := s.Stdout()
	local.Disconnect(bg)
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("the session stayed open after Disconnect")
	}
	if got := readAll(t, stdout); got != "" {
		t.Fatalf("stdout = %q", got)
	}
	if st := local.State(); !st.CanConnect || st.Found.Granted.Browser {
		t.Fatalf("after Disconnect: %+v", st)
	}
	if err := goclient.Disconnect(bg, r.daemon(), appOrigin); err != nil {
		t.Fatalf("a second Disconnect = %v", err)
	}
}

func TestADeniedDialogIsAReasonInPlainWordsAndTheCooldownFollows(t *testing.T) {
	r := startRig(t, "cat")
	r.setAnswer(consent.AnswerDeny)
	local := localOf(t, r, goclient.ModeBrowser, appOrigin)
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true after a deny")
	}
	if got := local.State().Error; got != goclient.ReasonText(goclient.ReasonDenied) {
		t.Fatalf("error = %q", got)
	}
	res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin)
	if res.Granted || res.Reason != goclient.ReasonCooldown {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
	if n := len(r.questions()); n != 1 {
		t.Fatalf("the dialog opened %d times", n)
	}
}

func TestADaemonWithoutADialogGrantsNothing(t *testing.T) {
	r := startRig(t, "cat")
	r.setAnswer(consent.AnswerUnavailable)
	res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin)
	if res.Granted || res.Reason != goclient.ReasonNoDialog {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
}

func TestTheGatewayRefusesAnOriginThatIsNotHttpsOrLoopback(t *testing.T) {
	r := startRig(t, "cat")
	for _, origin := range []string{"http://evil.example", "null", "", "ftp://localhost", appOrigin + "/path"} {
		if got := goclient.Detect(bg, origin, r.options()...); got.Found {
			t.Errorf("Detect(%q) found %+v", origin, got)
		}
		res := goclient.ConnectBrowser(bg, r.daemon(), origin)
		if res.Granted || res.Reason != goclient.ReasonOrigin {
			t.Errorf("ConnectBrowser(%q) = %+v", origin, res)
		}
	}
	if n := len(r.questions()); n != 0 {
		t.Fatalf("the dialog opened %d times", n)
	}
	if _, err := goclient.OpenSession(bg, r.daemon(), "http://evil.example", "claude"); err == nil {
		t.Fatal("a session for an origin the gateway does not trust")
	}
}

func TestDetectFindsTheGatewayOnTheFirstPortThatAnswersAmongSeveral(t *testing.T) {
	r := startRig(t, "cat")
	dead := freePort(t)
	got := goclient.Detect(bg, appOrigin, goclient.WithPorts(dead, r.port, dead), goclient.WithTimeout(time.Second))
	if !got.Found || got.Port != r.port {
		t.Fatalf("Detect = %+v", got)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// ---- server mode ----

// appServer is an app's server as far as the daemon sees it: <origin>/agentd/pair trades a code for a token.
type appServer struct {
	*httptest.Server
	mu      sync.Mutex
	issued  map[string]bool
	cancels int
	pairs   []map[string]any
}

func startAppServer(t *testing.T) *appServer {
	t.Helper()
	a := &appServer{issued: map[string]bool{}}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agentd/pair" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.pairs = append(a.pairs, body)
		code, _ := body["code"].(string)
		if !a.issued[code] {
			http.Error(w, "unknown code", http.StatusForbidden)
			return
		}
		delete(a.issued, code) // a code works once
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "token-for-" + code})
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *appServer) startPairing(context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	code := fmt.Sprintf("CODE-%d", len(a.pairs)+len(a.issued)+a.cancels+1)
	a.issued[code] = true
	return code, nil
}

func (a *appServer) cancelPairing() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels++
	clear(a.issued)
}

func TestServerModeDetectConnectPairsTheDaemonWithTheAppsServer(t *testing.T) {
	r := startRig(t, "cat")
	app := startAppServer(t)
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: app.URL, Options: r.options(), StartPairing: app.startPairing, CancelPairing: app.cancelPairing})
	if err != nil {
		t.Fatal(err)
	}
	local.Detect(bg)
	if st := local.State(); !st.Found.Found || st.Found.Granted.Server || !st.CanConnect {
		t.Fatalf("after Detect: %+v", st)
	}
	if !local.Connect(bg) {
		t.Fatalf("Connect = false: %q", local.State().Error)
	}
	if q := r.questions(); len(q) != 1 || q[0].Origin != app.URL || q[0].Mode != consent.ModeServer {
		t.Fatalf("the dialog asked %+v", q)
	}
	// The daemon dialed the app's own server at its origin with the code, and saved the app it got a token from.
	if len(app.pairs) != 1 || app.pairs[0]["code"] != "CODE-1" || app.pairs[0]["os"] != "linux" {
		t.Fatalf("the app's server was asked %v", app.pairs)
	}
	saved, err := registry.Load(r.regFile)
	if err != nil || len(saved.Apps) != 1 || saved.Apps[0].Server != app.URL || saved.Apps[0].Token != "token-for-CODE-1" {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	if st := local.State(); !st.Found.Granted.Server || st.CanConnect || st.Error != "" || app.cancels != 0 {
		t.Fatalf("after Connect: %+v (cancels %d)", st, app.cancels)
	}
}

func TestServerModeAnAppThatRefusesTheCodeIsPairingAndTheAppsPairingIsCancelled(t *testing.T) {
	r := startRig(t, "cat")
	app := startAppServer(t)
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: app.URL, Options: r.options(),
		StartPairing: func(context.Context) (string, error) { return "WRONG-CODE", nil }, CancelPairing: app.cancelPairing})
	if err != nil {
		t.Fatal(err)
	}
	local.Detect(bg)
	if local.Connect(bg) {
		t.Fatal("Connect = true")
	}
	if got := local.State().Error; got != goclient.ReasonText(goclient.ReasonPairing) || app.cancels != 1 {
		t.Fatalf("error %q, cancels %d", got, app.cancels)
	}
	if st := local.State(); st.Found.Granted.Server || !st.CanConnect {
		t.Fatalf("state = %+v", st)
	}
}

func TestServerModeTheRefusalCarriesTheDaemonsMessage(t *testing.T) {
	r := startRig(t, "cat")
	app := startAppServer(t)
	res := goclient.ConnectServer(bg, r.daemon(), app.URL, "NOT-ISSUED")
	if res.Granted || res.Reason != goclient.ReasonPairing || !strings.Contains(res.Message, "refused the pairing code") {
		t.Fatalf("ConnectServer = %+v", res)
	}
}

func TestServerModeDisconnectForgetsTheApp(t *testing.T) {
	r := startRig(t, "cat")
	app := startAppServer(t)
	local, _ := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: app.URL, Options: r.options(), StartPairing: app.startPairing, CancelPairing: app.cancelPairing})
	local.Detect(bg)
	if !local.Connect(bg) {
		t.Fatalf("Connect = false: %q", local.State().Error)
	}
	local.Disconnect(bg)
	if st := local.State(); st.Found.Granted.Server || !st.CanConnect {
		t.Fatalf("after Disconnect: %+v", st)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.revoked) != 1 || r.revoked[0] != app.URL {
		t.Fatalf("the daemon revoked %v", r.revoked)
	}
}

// ---- the app's tools: an MCP server the agent reaches through the daemon ----

// bridge is `agentd-mcp --page` as a test plays it: the hello with the token on the daemon's socket, then lines.
type bridge struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialBridge(t *testing.T, server goclient.McpServer) *bridge {
	t.Helper()
	env := map[string]string{}
	for _, e := range server.Env {
		env[e.Name] = e.Value
	}
	conn, err := net.Dial("unix", env[pagetools.McpSocketEnv])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, pagetools.BridgeHello(env[pagetools.McpTokenEnv])+"\n"); err != nil {
		t.Fatal(err)
	}
	b := &bridge{conn: conn, reader: bufio.NewReader(conn)}
	if answer := pagetools.ParseBridgeAnswer(b.line(t)); !answer.OK {
		t.Fatalf("the bridge was refused: %s", answer.Message)
	}
	return b
}

func (b *bridge) line(t *testing.T) string {
	t.Helper()
	_ = b.conn.SetReadDeadline(time.Now().Add(wait))
	line, err := b.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("the bridge read %q: %v", line, err)
	}
	return strings.TrimSuffix(line, "\n")
}

func (b *bridge) send(t *testing.T, m any) {
	t.Helper()
	raw, _ := json.Marshal(m)
	if _, err := io.WriteString(b.conn, string(raw)+"\n"); err != nil {
		t.Fatal(err)
	}
}

func TestThePagesToolsReachTheAgentThroughTheDaemonsBridge(t *testing.T) {
	r := startRig(t, "cat")
	if res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin); !res.Granted {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
	s, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	go serveTools(s.Mcp()) // taken before session/new, as the README says

	server, err := s.McpServer("page")
	if err != nil {
		t.Fatal(err)
	}
	if server.Name != "page" || server.Command != r.bridge || len(server.Env) != 2 {
		t.Fatalf("McpServer = %+v", server)
	}
	b := dialBridge(t, server) // what the agent's MCP client does with that entry
	for _, m := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "current_page"}},
	} {
		b.send(t, m)
	}
	for _, want := range []string{`"name":"page"`, `"name":"current_page"`, `/projects/42`} {
		if line := b.line(t); !strings.Contains(line, want) {
			t.Fatalf("the bridge read %q, want %s", line, want)
		}
	}
	// The agent's stdin stayed its own: nothing of the MCP lines went there.
	if err := s.Write(`{"on":"stdin"}`); err != nil {
		t.Fatal(err)
	}
	if got := firstLine(t, s.Stdout()); got != `{"on":"stdin"}`+"\n" {
		t.Fatalf("stdout = %q", got)
	}
}

// ---- the ACP library over a real session ----

// updates is the app's side of ACP: it collects the agent's messages.
type updates struct {
	acp.Client
	mu    sync.Mutex
	texts []string
	got   chan struct{}
}

func (u *updates) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

func (u *updates) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if m := n.Update.AgentMessageChunk; m != nil && m.Content.Text != nil {
		u.mu.Lock()
		u.texts = append(u.texts, m.Content.Text.Text)
		u.mu.Unlock()
		u.got <- struct{}{}
	}
	return nil
}

func TestTheACPLibraryTalksToARealAgentThroughASession(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := startRig(t, script(t, fakeAgentEnv+"=1 exec "+exe))
	if res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin); !res.Granted {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
	s, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	client := &updates{got: make(chan struct{}, 4)}
	conn := acp.NewClientSideConnection(client, s.Stdin(), s.Stdout())
	ctx, cancel := context.WithTimeout(bg, 20*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	go serveTools(s.Mcp())
	tools, err := s.McpServer("page")
	if err != nil {
		t.Fatal(err)
	}
	env := []acp.EnvVariable{}
	for _, e := range tools.Env {
		env = append(env, acp.EnvVariable{Name: e.Name, Value: e.Value})
	}
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: s.Cwd, McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: tools.Name, Command: tools.Command, Args: tools.Args, Env: env}}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Hello")}})
	if err != nil || res.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt = %+v, %v", res, err)
	}
	<-client.got
	if want := "echo: Hello [mcp page]"; len(client.texts) != 1 || client.texts[0] != want {
		t.Fatalf("the agent said %q, want %q", client.texts, want)
	}
}

func TestTheGoMCPSDKServesThePagesToolsOverMcp(t *testing.T) {
	r := startRig(t, "cat")
	if res := goclient.ConnectBrowser(bg, r.daemon(), appOrigin); !res.Granted {
		t.Fatalf("ConnectBrowser = %+v", res)
	}
	s, err := goclient.OpenSession(bg, r.daemon(), appOrigin, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	tools := mcp.NewServer(&mcp.Implementation{Name: "page", Version: "1"}, nil)
	mcp.AddTool(tools, &mcp.Tool{Name: "current_page", Description: "The page the user is on"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "/projects/42"}}}, nil, nil
		})
	stream := s.Mcp()
	go func() { _ = tools.Run(bg, &mcp.IOTransport{Reader: stream, Writer: stream}) }()

	server, err := s.McpServer("page")
	if err != nil {
		t.Fatal(err)
	}
	b := dialBridge(t, server)
	b.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agent", "version": "1"}}})
	if line := b.line(t); !strings.Contains(line, `"name":"page"`) {
		t.Fatalf("initialize answered %q", line)
	}
	b.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	b.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if line := b.line(t); !strings.Contains(line, `"name":"current_page"`) {
		t.Fatalf("tools/list answered %q", line)
	}
	b.send(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "current_page", "arguments": map[string]any{}}})
	if line := b.line(t); !strings.Contains(line, "/projects/42") {
		t.Fatalf("tools/call answered %q", line)
	}
}
