package run

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// The local secret of the standard ACP endpoint, with a real daemon and the real `agentd acp`.

var upgradeHeaders = map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}

func TestARealDaemonMakesItsTokenInAPrivateFileAndKeepsItAcrossARestart(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := g.box.acpToken(t)
	if !acpauth.Valid(token) {
		t.Fatalf("the daemon's token %q is not a token", token)
	}
	for path, want := range map[string]os.FileMode{acpauth.Dir(g.box.agentdHome()): 0o700, acpauth.TokenFile(g.box.agentdHome()): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want mode %o", path, fi, err, want)
		}
	}

	g.d.stop()
	again := waitForGateway(t, startDaemon(t, g.box, nil, map[string]string{"AGENTD_LOCAL_PORT": "0", "AGENTD_AGENT_CLAUDE": sleepAgent}), g.box)
	if got := g.box.acpToken(t); got != token {
		t.Errorf("the token changed over a restart: an address a client was given must keep working")
	}
	ws, _ := dialACP(t, g.box, again.port, "/acp/claude", "")
	_ = ws.Close()
}

func TestARealDaemonRefusesAProgramWithoutTheTokenAndLogsNothingItSent(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := g.box.acpToken(t)
	wrong := strings.Repeat("Q", acpauth.TokenLen)
	for i := 0; i < 3; i++ {
		res := g.call(t, http.MethodGet, "/acp/claude?token="+wrong, callOptions{headers: upgradeHeaders})
		if res.status != http.StatusUnauthorized || res.body["reason"] != "denied" {
			t.Fatalf("status %d, body %v; want 401 denied", res.status, res.body)
		}
		if res.headers.Get("WWW-Authenticate") != `Bearer realm="agentd"` {
			t.Errorf("WWW-Authenticate = %q", res.headers.Get("WWW-Authenticate"))
		}
	}
	waitFor(t, "the refusal to be logged", func() bool { return g.d.says("acp connection refused: a local program without the token") })
	if n := strings.Count(g.d.out.String(), "acp connection refused"); n != 1 {
		t.Errorf("the refusal was logged %d times, want once in ten seconds; output:\n%s", n, g.d.out)
	}

	ws, _ := dialACP(t, g.box, g.port, "/acp/claude?token="+token, "")
	if err := ws.WriteMessage(websocket.TextMessage, []byte(acpInitialize)); err != nil {
		t.Fatal(err)
	}
	readText(t, ws)
	waitFor(t, "the connection to be logged", func() bool { return g.d.says("claude for a local program") })
	out := g.d.out.String()
	for _, secret := range []string{token, wrong, "token=", "Bearer"} {
		if strings.Contains(out, secret) {
			t.Errorf("the daemon's output holds %q:\n%s", secret, out)
		}
	}
}

func TestARealDaemonRecordsWhereItsEndpointIsAndRemovesTheRecordWhenItEnds(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := g.box.acpToken(t)
	var info acpauth.Info
	waitFor(t, "the daemon to record its endpoint", func() bool {
		var err error
		info, err = acpauth.ReadInfo(g.box.agentdHome())
		return err == nil && len(info.Agents) > 0
	})
	if want := "ws://127.0.0.1:" + strconv.Itoa(g.port) + "/acp"; info.Address != want {
		t.Errorf("the recorded address is %q, want %q", info.Address, want)
	}
	// claude and codex are ready by their test seams; grok is whatever this machine has, so the record is not asked for it.
	if !hasString(info.Agents, "claude") || !hasString(info.Agents, "codex") {
		t.Errorf("the recorded agents %v: claude and codex are ready (their seams)", info.Agents)
	}
	raw, _ := os.ReadFile(acpauth.InfoFile(g.box.agentdHome()))
	if strings.Contains(string(raw), token) {
		t.Errorf("the record holds the token: %s", raw)
	}
	if fi, err := os.Stat(acpauth.InfoFile(g.box.agentdHome())); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the record: %v, %v; want mode 600", fi, err)
	}

	g.d.stop()
	if _, err := acpauth.ReadInfo(g.box.agentdHome()); err == nil {
		t.Error("the record of an endpoint that is gone is still there")
	}
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestAgentdAcpPrintsTheReadyMadeAddressOfEachReadyAgentAndItWorks(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := g.box.acpToken(t)
	waitFor(t, "the daemon to record its endpoint", func() bool {
		info, err := acpauth.ReadInfo(g.box.agentdHome())
		return err == nil && hasString(info.Agents, "claude")
	})
	base := "ws://127.0.0.1:" + strconv.Itoa(g.port) + "/acp/"

	code, stdout, stderr := runCommand(t, g.box, "acp")
	if code != 0 {
		t.Fatalf("agentd acp exited %d: %s", code, stderr)
	}
	// One address for each agent the daemon recorded as ready, in its order (grok depends on the machine).
	info, err := acpauth.ReadInfo(g.box.agentdHome())
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, agent := range info.Agents {
		want += base + agent + "?token=" + token + "\n"
	}
	if stdout != want || !strings.HasPrefix(stdout, base+"claude?token="+token+"\n"+base+"codex?token="+token+"\n") {
		t.Errorf("agentd acp printed\n%q\nwant\n%q (the agents that are ready, in the daemon's order)", stdout, want)
	}
	if !strings.Contains(stderr, "Authorization: Bearer <token>") || strings.Contains(stderr, token) {
		t.Errorf("the hint on stderr is %q: it should name the header form and never print the token", stderr)
	}

	// The address it prints is one a client can use as it stands.
	ws, _, err := websocket.DefaultDialer.Dial(strings.TrimSpace(strings.SplitN(stdout, "\n", 2)[0]), nil)
	if err != nil {
		t.Fatalf("dialling the printed address: %v", err)
	}
	_ = ws.Close()

	code, stdout, _ = runCommand(t, g.box, "acp", "claude")
	if code != 0 || stdout != base+"claude?token="+token+"\n" {
		t.Errorf("agentd acp claude: exit %d, printed %q", code, stdout)
	}
	code, stdout, _ = runCommand(t, g.box, "acp", "--token")
	if code != 0 || stdout != token+"\n" {
		t.Errorf("agentd acp --token: exit %d, printed %q", code, stdout)
	}
	code, _, stderr = runCommand(t, g.box, "acp", "nosuch")
	if code == 0 || !strings.Contains(stderr, "claude, codex or grok") {
		t.Errorf("agentd acp nosuch: exit %d, stderr %q; want an error that names the agents", code, stderr)
	}
}

func TestAgentdAcpRotateMakesTheOldAddressStopAtOnce(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	old := g.box.acpToken(t)
	waitFor(t, "the daemon to record its endpoint", func() bool {
		info, err := acpauth.ReadInfo(g.box.agentdHome())
		return err == nil && hasString(info.Agents, "claude")
	})
	open, _ := dialACP(t, g.box, g.port, "/acp/claude", "")

	code, stdout, stderr := runCommand(t, g.box, "acp", "--rotate", "claude")
	if code != 0 {
		t.Fatalf("agentd acp --rotate exited %d: %s", code, stderr)
	}
	fresh := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(stdout), "ws://127.0.0.1:"+strconv.Itoa(g.port)+"/acp/claude?token="), "\n")
	if !acpauth.Valid(fresh) || fresh == old {
		t.Fatalf("agentd acp --rotate printed %q; want an address with a new token (old %q)", stdout, old)
	}

	res := g.call(t, http.MethodGet, "/acp/claude?token="+old, callOptions{headers: upgradeHeaders})
	if res.status != http.StatusUnauthorized {
		t.Errorf("the old token after a rotation: status %d, want 401", res.status)
	}
	if ws, _, err := websocket.DefaultDialer.Dial(strings.TrimSpace(stdout), nil); err != nil {
		t.Errorf("the new address: %v", err)
	} else {
		_ = ws.Close()
	}
	// The connection that was open before the rotation is not cut.
	if err := open.WriteMessage(websocket.TextMessage, []byte(acpInitialize)); err != nil {
		t.Fatal(err)
	}
	readText(t, open)
}

func TestAgentdAcpWhenNoDaemonHasRunAndWhenItHasEnded(t *testing.T) {
	box := newSandbox(t)
	code, stdout, stderr := runCommand(t, box, "acp")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "has not run on this computer yet") {
		t.Errorf("before any start: exit %d, stdout %q, stderr %q; want exit 1 and a sentence that says agentd has not run yet", code, stdout, stderr)
	}

	g := startGatewayDaemon(t, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := g.box.acpToken(t)
	g.d.stop()
	code, stdout, stderr = runCommand(t, g.box, "acp", "claude")
	if code != 0 || !strings.Contains(stdout, "/acp/claude?token="+token) {
		t.Errorf("after the daemon ended: exit %d, stdout %q; want an address and exit 0", code, stdout)
	}
	if !strings.Contains(stderr, "is not running") {
		t.Errorf("stderr %q: it should warn that agentd is not running", stderr)
	}
}

// The app is told where the endpoint is and what the token is in the state it shows, so that it can copy the address
// for the user. The daemon never writes the token to its log.
func TestTheAppIsToldTheAcpAddressAndTokenAndTheLogNeverHasThem(t *testing.T) {
	box := newSandbox(t)
	d, app := startWithApp(t, box, map[string]string{"AGENTD_AGENT_CLAUDE": acpAgent(filepath.Join(t.TempDir(), "pid"))})
	token := box.acpToken(t)

	state := app.state(t, "a state with the ACP address", func(s ui.State) bool { return s.Acp != nil })
	acp := state.Acp
	var port int
	waitFor(t, "the gateway port", func() bool {
		m := gatewayLine.FindStringSubmatch(d.out.String())
		if m == nil {
			return false
		}
		port, _ = strconv.Atoi(m[1])
		return true
	})
	if want := "ws://127.0.0.1:" + strconv.Itoa(port) + "/acp/"; acp.Base != want {
		t.Errorf("acp.base = %v, want %q", acp.Base, want)
	}
	if acp.Token != token {
		t.Errorf("acp.token is not the token in the file")
	}
	found := map[string]bool{}
	for _, a := range acp.Agents {
		found[a] = true
	}
	if !found["claude"] || !found["codex"] {
		t.Errorf("acp.agents = %v, want claude and codex (ready by their seams)", acp.Agents)
	}
	if out := d.out.String(); strings.Contains(out, token) {
		t.Errorf("the daemon's output holds the token:\n%s", out)
	}
}
