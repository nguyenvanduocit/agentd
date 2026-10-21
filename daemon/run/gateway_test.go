package run

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The local gateway of a whole daemon: what pages may ask, what the user is asked, and what the daemon does with the
// answer. (The gateway's rules on their own are tested in daemon/gateway.)

// consentScript is a program for AGENTD_CONSENT: it appends a line to `ran`, waits delay seconds, prints `folder` (the
// folder the user chose) and exits with code.
type consentScript struct {
	file   string
	ran    string
	folder string
}

func newConsent(t *testing.T, code, delayS int) consentScript {
	t.Helper()
	dir := t.TempDir()
	c := consentScript{file: filepath.Join(dir, "consent"), ran: filepath.Join(dir, "ran"), folder: filepath.Join(dir, "work")}
	script := fmt.Sprintf("#!/bin/sh\necho \"$1\" >> %s\nsleep %d\necho %s\nexit %d\n", c.ran, delayS, c.folder, code)
	if err := os.WriteFile(c.file, []byte(script), 0o755); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	return c
}

// lines is how many dialogs opened: one line each.
func (c consentScript) lines() int {
	data, err := os.ReadFile(c.ran)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// gatewayDaemon is a daemon with its gateway on a port of its own.
type gatewayDaemon struct {
	d    *daemonProc
	box  *sandbox
	port int
}

var gatewayLine = regexp.MustCompile(`local gateway on 127\.0\.0\.1:(\d+)`)

// startGatewayDaemon starts a daemon paired with `apps` and waits for its gateway.
func startGatewayDaemon(t *testing.T, env map[string]string, apps ...*fakeApp) *gatewayDaemon {
	t.Helper()
	box := newSandbox(t)
	if len(apps) > 0 {
		origins := make([]string, len(apps))
		for i, a := range apps {
			origins[i] = a.origin()
		}
		box.writeServers(t, origins...)
	}
	merged := map[string]string{"AGENTD_LOCAL_PORT": "0", "AGENTD_AGENT_CLAUDE": sleepAgent}
	for k, v := range env {
		merged[k] = v
	}
	return waitForGateway(t, startDaemon(t, box, nil, merged), box)
}

// waitForGateway waits for the daemon to say on which port its gateway listens.
func waitForGateway(t *testing.T, d *daemonProc, box *sandbox) *gatewayDaemon {
	t.Helper()
	g := &gatewayDaemon{d: d, box: box}
	waitFor(t, "the gateway port", func() bool {
		m := gatewayLine.FindStringSubmatch(d.out.String())
		if m == nil {
			return false
		}
		g.port, _ = strconv.Atoi(m[1])
		return true
	})
	return g
}

type reply struct {
	status  int
	body    map[string]any
	headers http.Header
}

// callOptions are the parts of a request a test varies. origin nil sends no Origin header.
type callOptions struct {
	origin  *string
	host    string
	body    any
	headers map[string]string
}

func origin(o string) *string { return &o }

func (g *gatewayDaemon) call(t *testing.T, method, path string, o callOptions) reply {
	t.Helper()
	var body io.Reader
	if o.body != nil {
		data, err := json.Marshal(o.body)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", g.port, path), body)
	if err != nil {
		t.Fatal(err)
	}
	if o.host != "" {
		req.Host = o.host
	}
	if o.origin != nil {
		req.Header.Set("Origin", *o.origin)
	}
	if o.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	text, _ := io.ReadAll(res.Body)
	out := reply{status: res.StatusCode, body: map[string]any{}, headers: res.Header}
	if len(text) > 0 {
		if err := json.Unmarshal(text, &out.body); err != nil {
			t.Fatalf("%s %s answered %q: %v", method, path, text, err)
		}
	}
	return out
}

// connect asks to connect as `from`. A body that names no app gets a name: what these tests are about is not the name.
func (g *gatewayDaemon) connect(t *testing.T, body any, from string) reply {
	t.Helper()
	if m, ok := body.(map[string]any); ok {
		if _, named := m["name"]; !named {
			named := map[string]any{"name": "Test app"}
			for k, v := range m {
				named[k] = v
			}
			body = named
		}
	}
	return g.call(t, "POST", "/v1/connect", callOptions{origin: origin(from), body: body})
}

func (g *gatewayDaemon) status(t *testing.T, from string) reply {
	t.Helper()
	return g.call(t, "GET", "/v1/status", callOptions{origin: origin(from)})
}

func (g *gatewayDaemon) savedApps(t *testing.T) []string {
	t.Helper()
	reg, err := registry.Load(g.box.configFile())
	if err != nil {
		t.Fatal(err)
	}
	servers := []string{}
	for _, a := range reg.Apps {
		servers = append(servers, a.Server)
	}
	return servers
}

// saved is what daemon.json holds now.
func (g *gatewayDaemon) saved(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Load(g.box.configFile())
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// want checks a reply's status and the fields of its body.
func (r reply) want(t *testing.T, status int, fields map[string]any) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d (%v), want %d", r.status, r.body, status)
	}
	for k, v := range fields {
		if !jsonEqual(r.body[k], v) {
			t.Fatalf("body[%q] = %v, want %v (body %v)", k, r.body[k], v, r.body)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func granted(server, browser bool) map[string]any {
	return map[string]any{"granted": map[string]any{"server": server, "browser": browser}}
}

var serverBody = map[string]any{"mode": "server", "code": pairingCode}

func TestGatewayRefusalsBeforeAnyDialog(t *testing.T) {
	c := newConsent(t, 0, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	for _, tc := range []struct {
		name string
		call func() reply
		want int
	}{
		{"a foreign Host is 421", func() reply {
			return g.call(t, "GET", "/v1/status", callOptions{origin: origin(pageOrigin), host: "evil.example"})
		}, 421},
		{"a missing Origin is 403", func() reply { return g.call(t, "GET", "/v1/status", callOptions{}) }, 403},
		{"a null Origin is 403", func() reply { return g.status(t, "null") }, 403},
		{"an untrusted Origin is 403", func() reply { return g.status(t, "http://evil.example") }, 403},
		{"connect from an untrusted Origin is 403", func() reply { return g.connect(t, map[string]any{"mode": "browser"}, "http://evil.example") }, 403},
	} {
		if got := tc.call().status; got != tc.want {
			t.Fatalf("%s: status %d", tc.name, got)
		}
	}
	if n := c.lines(); n != 0 {
		t.Fatalf("the dialog ran %d times", n)
	}
}

func TestGatewayMalformedBodyIs400AndOpensNoDialog(t *testing.T) {
	c := newConsent(t, 0, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	g.connect(t, map[string]any{"mode": "server"}, pageOrigin).want(t, 400, nil)
	g.connect(t, map[string]any{"mode": "root"}, pageOrigin).want(t, 400, nil)
	if n := c.lines(); n != 0 {
		t.Fatalf("the dialog ran %d times", n)
	}
}

func TestGatewayPreflightWithPrivateNetworkAccessIsAnsweredForATrustedOrigin(t *testing.T) {
	g := startGatewayDaemon(t, nil)
	r := g.call(t, "OPTIONS", "/v1/connect", callOptions{origin: origin(pageOrigin), headers: map[string]string{"Access-Control-Request-Private-Network": "true", "Access-Control-Request-Method": "POST"}})
	if r.status != 204 || r.headers.Get("Access-Control-Allow-Origin") != pageOrigin || r.headers.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("status %d, headers %v", r.status, r.headers)
	}
}

func TestGatewayStatusOfAnOriginThatWasNeverAllowedIsTheVersionAndNothingElse(t *testing.T) {
	g := startGatewayDaemon(t, nil)
	r := g.status(t, pageOrigin)
	r.want(t, 200, map[string]any{"name": "agentd", "granted": map[string]any{"server": false, "browser": false}})
	if _, has := r.body["agents"]; has {
		t.Fatal("an origin that was never allowed saw the agents")
	}
	if r.headers.Get("Access-Control-Allow-Origin") != pageOrigin {
		t.Fatalf("headers %v", r.headers)
	}
}

func TestGatewayAllowPairsSavesAndLinksWithoutARestartAndTheOriginThenSeesTheAgents(t *testing.T) {
	app := startApp(t)
	c := newConsent(t, 0, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	g.connect(t, serverBody, app.origin()).want(t, 200, map[string]any{"granted": true})
	if n := c.lines(); n != 1 {
		t.Fatalf("the dialog ran %d times, want 1", n)
	}
	if got := g.saved(t).AppFolders(app.origin()); len(got) != 1 || got[0] != c.folder {
		t.Fatalf("the app's folders = %v, want the one the user chose: %s", got, c.folder)
	}
	if saved := g.savedApps(t); len(saved) != 1 || saved[0] != app.origin() {
		t.Fatalf("saved apps = %v", saved)
	}
	app.waitHellos(t, 1)
	st := g.status(t, app.origin())
	st.want(t, 200, granted(true, false))
	var names []string
	for _, a := range st.body["agents"].([]any) {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "claude,codex,grok" {
		t.Fatalf("agents = %v", names)
	}
}

func TestGatewayARequestThatNamesAServerIsRefusedBeforeAnyDialog(t *testing.T) {
	c := newConsent(t, 0, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	body := map[string]any{"mode": "server", "code": pairingCode, "server": "https://api.example.com"}
	g.connect(t, body, pageOrigin).want(t, 400, map[string]any{"reason": "malformed"})
	if n := c.lines(); n != 0 {
		t.Fatalf("the dialog ran %d times", n)
	}
}

func TestGatewayDenySavesNothingAndTheSameOriginWaitsOutACooldown(t *testing.T) {
	app := startApp(t)
	c := newConsent(t, 1, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	g.connect(t, serverBody, app.origin()).want(t, 403, map[string]any{"granted": false, "reason": "denied"})
	g.connect(t, serverBody, app.origin()).want(t, 429, map[string]any{"granted": false, "reason": "cooldown"})
	if n := c.lines(); n != 1 {
		t.Fatalf("the dialog ran %d times, want 1", n)
	}
	g.status(t, app.origin()).want(t, 200, granted(false, false))
	if app.connected() {
		t.Fatal("the daemon linked to an app that was denied")
	}
}

func TestGatewayRefusalsFromThreeOriginsLockEveryDialog(t *testing.T) {
	c := newConsent(t, 1, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	browser := map[string]any{"mode": "browser"}
	for _, o := range []string{"http://localhost:5171", "http://localhost:5172", "http://localhost:5173"} {
		g.connect(t, browser, o).want(t, 403, map[string]any{"reason": "denied"})
	}
	g.connect(t, browser, "http://localhost:5174").want(t, 429, map[string]any{"reason": "cooldown"})
	if n := c.lines(); n != 3 {
		t.Fatalf("the dialog ran %d times, want 3", n)
	}
}

func TestGatewayASecondRequestWhileADialogIsOpenIsBusy(t *testing.T) {
	c := newConsent(t, 0, 2)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file})
	browser := map[string]any{"mode": "browser"}
	first := make(chan reply, 1)
	go func() {
		first <- g.connect(t, browser, pageOrigin)
	}()
	waitFor(t, "the dialog to open", func() bool { return c.lines() == 1 })
	g.connect(t, browser, "http://localhost:5174").want(t, 409, map[string]any{"reason": "busy"})
	(<-first).want(t, 200, nil)
}

func TestGatewayAMachineThatCannotShowADialogSaysSoAndGrantsNothing(t *testing.T) {
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": "/nonexistent/consent"})
	g.connect(t, map[string]any{"mode": "browser"}, pageOrigin).want(t, 503, map[string]any{"granted": false, "reason": "no-dialog"})
}

func TestGatewayAPairingCodeTheAppRefusesIs502AfterAllowAndNothingIsSaved(t *testing.T) {
	app := startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file})
	g.connect(t, map[string]any{"mode": "server", "code": "WRONG"}, app.origin()).want(t, 502, map[string]any{"granted": false, "reason": "pairing"})
	if _, err := os.Stat(g.box.configFile()); err == nil {
		if saved := g.savedApps(t); len(saved) != 0 {
			t.Fatalf("saved apps = %v", saved)
		}
	}
}

func TestGatewayConnectingAnAppThatIsAlreadyPairedLinksItAgainAtOnce(t *testing.T) {
	app := startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file}, app)
	app.waitHellos(t, 1)
	g.connect(t, serverBody, app.origin()).want(t, 200, nil)
	app.waitHellos(t, 2)
	if saved := g.savedApps(t); len(saved) != 1 || saved[0] != app.origin() {
		t.Fatalf("saved apps = %v", saved)
	}
}

func TestGatewayDeleteDisconnectsTheCallersOwnAppAndTheDaemonStaysUp(t *testing.T) {
	app := startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file}, app)
	app.waitHellos(t, 1)
	g.call(t, "DELETE", "/v1/connect", callOptions{origin: origin(app.origin())}).want(t, 200, nil)
	waitFor(t, "the app is disconnected", func() bool { return !app.connected() })
	if saved := g.savedApps(t); len(saved) != 0 {
		t.Fatalf("saved apps = %v", saved)
	}
	if g.d.hasExited() {
		t.Fatalf("the daemon ended; output:\n%s", g.d.out)
	}
}

func TestGatewayStatusForAnotherOriginSaysNothingAboutTheAppAndDeleteFromItDoesNothing(t *testing.T) {
	app := startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file}, app)
	app.waitHellos(t, 1)
	asked := "?server=" + app.origin() // the old way to name another origin's app: ignored
	st := g.call(t, "GET", "/v1/status"+asked, callOptions{origin: origin(pageOrigin)})
	st.want(t, 200, granted(false, false))
	if _, has := st.body["agents"]; has {
		t.Fatal("another origin saw the agents")
	}
	g.call(t, "DELETE", "/v1/connect"+asked, callOptions{origin: origin(pageOrigin)}).want(t, 200, nil)
	time.Sleep(500 * time.Millisecond)
	if saved := g.savedApps(t); len(saved) != 1 || saved[0] != app.origin() {
		t.Fatalf("saved apps = %v", saved)
	}
	if !app.connected() {
		t.Fatal("another origin disconnected the app")
	}
}

func TestGatewayAnAppThatRevokedThisDaemonReadsAsNotLinked(t *testing.T) {
	a, b := startApp(t), startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file}, a, b)
	a.waitHellos(t, 1)
	b.waitHellos(t, 1)
	a.revoke()
	waitFor(t, "the refusal", func() bool { return g.d.says(a.origin() + " refused this daemon (unauthorized)") })
	g.status(t, a.origin()).want(t, 200, map[string]any{"granted": map[string]any{"server": false, "browser": false}})
}

func TestGatewayAnAppThatStopDetachedIsOfferedAgainAndConnectsAgain(t *testing.T) {
	a, b := startApp(t), startApp(t)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": newConsent(t, 0, 0).file}, a, b)
	a.waitHellos(t, 1)
	b.waitHellos(t, 1)
	a.stopDaemon(t)
	waitFor(t, "a is disconnected", func() bool { return !a.connected() })
	g.status(t, a.origin()).want(t, 200, granted(false, false))
	g.connect(t, serverBody, a.origin()).want(t, 200, nil)
	waitFor(t, "a is linked again", a.connected)
	g.status(t, a.origin()).want(t, 200, granted(true, false))
}

func TestGatewayBrowserAllowSavesTheOriginAndTheDaemonOutlivesItsLastServerApp(t *testing.T) {
	app := startApp(t)
	c := newConsent(t, 0, 0)
	g := startGatewayDaemon(t, map[string]string{"AGENTD_CONSENT": c.file}, app)
	app.waitHellos(t, 1)
	g.connect(t, map[string]any{"mode": "browser"}, pageOrigin).want(t, 200, map[string]any{"granted": true})
	g.status(t, pageOrigin).want(t, 200, granted(false, true))
	if got := g.saved(t).BrowserFolders(pageOrigin); len(got) != 1 || got[0] != c.folder {
		t.Fatalf("the page's folders = %v, want the one the user chose: %s", got, c.folder)
	}
	app.stopDaemon(t)
	waitFor(t, "the app is detached", func() bool { return !app.connected() })
	waitForNothing(t, "the daemon to stay up", g.d.hasExited)
	if !g.d.says("the last app left; staying up for the web pages that may connect") {
		t.Fatalf("output:\n%s", g.d.out)
	}
}

func TestGatewayIsOffWhenEveryPortOfTheRangeIsTakenAndTheAppsStillWork(t *testing.T) {
	base, blockers := takePorts(t, 10)
	defer func() {
		for _, l := range blockers {
			_ = l.Close()
		}
	}()
	app := startApp(t)
	box := newSandbox(t)
	box.writeServers(t, app.origin())
	d := startDaemon(t, box, nil, map[string]string{"AGENTD_LOCAL_PORT": strconv.Itoa(base), "AGENTD_AGENT_CLAUDE": sleepAgent})
	waitFor(t, "the gateway off line", func() bool { return d.says("local gateway off") })
	app.waitHellos(t, 1)
}

// takePorts listens on n consecutive ports of loopback and returns the first one.
func takePorts(t *testing.T, n int) (int, []net.Listener) {
	t.Helper()
	for base := 53100; base < 60000; base += n {
		var held []net.Listener
		for p := base; p < base+n; p++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				break
			}
			held = append(held, l)
		}
		if len(held) == n {
			return base, held
		}
		for _, l := range held {
			_ = l.Close()
		}
	}
	t.Fatal("no run of free ports")
	return 0, nil
}

func TestGatewayBadPortVariableStopsTheDaemonWithAMessage(t *testing.T) {
	box := newSandbox(t)
	d := startDaemon(t, box, nil, map[string]string{"AGENTD_LOCAL_PORT": "nope"})
	if code := d.exitCode(t, waitLimit); code != 1 || !d.says("AGENTD_LOCAL_PORT") {
		t.Fatalf("exit code %d; output:\n%s", code, d.out)
	}
}
