package gateway

import (
	"errors"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

const wait = 10 * time.Second

func status(t *testing.T, port int, origin string) reply {
	t.Helper()
	return call(t, port, http.MethodGet, "/v1/status", callOpts{origin: origin})
}

func TestRefusalsBeforeAnyDialog(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)

	// A foreign Host is 421; a missing, null or untrusted Origin is 403; the dialog never runs.
	wantReply(t, call(t, g.Port, "GET", "/v1/status", callOpts{origin: page, host: "evil.example"}), 421, map[string]any{"granted": false, "reason": "host"})
	for _, origin := range []string{"", "null", "http://evil.example"} {
		wantReply(t, call(t, g.Port, "GET", "/v1/status", callOpts{origin: origin}), 403, map[string]any{"reason": "origin"})
	}
	wantReply(t, call(t, g.Port, "POST", "/v1/connect", callOpts{origin: "http://evil.example", body: map[string]any{"mode": "browser"}}), 403, nil)
	if ran() != 0 {
		t.Fatalf("the dialog ran %d times", ran())
	}
}

func TestACheckOrderIsPeerThenHostThenOrigin(t *testing.T) {
	g := startGateway(t, newWorld().deps(), func(consent.Connection) ([]int, error) { return []int{-1}, nil })
	r := call(t, g.Port, "GET", "/v1/status", callOpts{origin: "null", host: "evil.example"})
	wantReply(t, r, 403, map[string]any{"reason": "peer"})
	if _, cors := r.header["Access-Control-Allow-Origin"]; cors {
		t.Error("a peer refusal carries CORS headers")
	}

	g = startGateway(t, newWorld().deps(), nil)
	wantReply(t, call(t, g.Port, "GET", "/v1/status", callOpts{origin: "null", host: "evil.example"}), 421, map[string]any{"reason": "host"})
}

func TestAMalformedBodyIs400AndDoesNotOpenTheDialog(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "server"}, page), 400, map[string]any{"reason": "malformed"})
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "root"}, page), 400, nil)
	big := map[string]any{"mode": "server", "code": string(make([]byte, 5000))}
	wantReply(t, connectAs(t, g.Port, big, page), 400, nil)
	if ran() != 0 {
		t.Fatalf("the dialog ran %d times", ran())
	}
}

func TestAPreflightWithPrivateNetworkAccessIsAnsweredForATrustedOrigin(t *testing.T) {
	g := startGateway(t, newWorld().deps(), nil)
	r := call(t, g.Port, "OPTIONS", "/v1/connect", callOpts{origin: page, header: map[string]string{"Access-Control-Request-Private-Network": "true", "Access-Control-Request-Method": "POST"}})
	if r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != page || r.header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("status %d, headers %v", r.status, r.header)
	}
}

func TestAnUnknownRouteIs404WithCORS(t *testing.T) {
	g := startGateway(t, newWorld().deps(), nil)
	for _, c := range [][2]string{{"POST", "/v1/status"}, {"GET", "/v1/nothing"}, {"GET", "/v1/%73tatus"}} {
		r := call(t, g.Port, c[0], c[1], callOpts{origin: page})
		wantReply(t, r, 404, map[string]any{"reason": "not-found"})
		if r.header.Get("Access-Control-Allow-Origin") != page {
			t.Errorf("%s %s: no CORS on a 404", c[0], c[1])
		}
	}
}

func TestStatusOfAnOriginThatWasNeverAllowedIsTheVersionAndNothingElse(t *testing.T) {
	g := startGateway(t, newWorld().deps(), nil)
	r := status(t, g.Port, page)
	wantReply(t, r, 200, map[string]any{"name": "agentd", "version": "test"})
	if server, browser := granted(t, r); server || browser {
		t.Fatalf("granted = %v %v", server, browser)
	}
	if _, has := r.body["agents"]; has {
		t.Fatal("agents for an origin that is not granted")
	}
	if r.header.Get("Access-Control-Allow-Origin") != page || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", r.header)
	}
}

const code = "TESTCODE"

func TestAllowPairsSavesAndLinksAndTheOriginThenSeesTheAgents(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	app := "https://app.example.com"

	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "server", "code": code}, app), 200, map[string]any{"granted": true})
	if ran() != 1 || len(w.paired) != 1 || w.paired[0] != app+" "+code {
		t.Fatalf("dialogs %d, paired %v", ran(), w.paired)
	}
	if apps := w.apps(); len(apps) != 1 || apps[0] != app {
		t.Fatalf("saved apps = %v", apps)
	}
	if q := w.asks()[0]; q.Origin != app || q.Mode != consent.ModeServer || len(q.Agents) != 1 || q.Agents[0] != "claude" {
		t.Fatalf("the dialog asked %+v: only agents that are ready, for the caller's own origin", q)
	}
	if q := w.asks()[0]; len(q.Titles) != 1 || q.Titles[0] != "Claude Code" {
		t.Fatalf("the dialog names the agents %q: want them as a user reads them", q.Titles)
	}
	st := status(t, g.Port, app)
	if server, browser := granted(t, st); !server || browser {
		t.Fatalf("granted = %v %v", server, browser)
	}
	agents, _ := st.body["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %v", st.body["agents"])
	}
}

func TestARequestThatNamesAServerIsRefusedBeforeAnyDialog(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "server", "code": code, "server": "https://api.example.com"}, page), 400, map[string]any{"reason": "malformed"})
	if ran() != 0 {
		t.Fatal("the dialog ran")
	}
}

func TestDenySavesNothingAndTheSameOriginWaitsOutACooldown(t *testing.T) {
	ask, ran := dialog(t, 1, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	app := "https://app.example.com"
	body := map[string]any{"mode": "server", "code": code}
	wantReply(t, connectAs(t, g.Port, body, app), 403, map[string]any{"granted": false, "reason": "denied"})
	wantReply(t, connectAs(t, g.Port, body, app), 429, map[string]any{"granted": false, "reason": "cooldown"})
	if ran() != 1 {
		t.Fatalf("dialogs = %d", ran())
	}
	if server, _ := granted(t, status(t, g.Port, app)); server || len(w.apps()) != 0 {
		t.Fatal("Deny granted something")
	}
}

func TestACooldownLastsThirtySecondsOnTheGatewaysClock(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	w := newWorld()
	w.ask = func(consent.Question) consent.Answer { return consent.AnswerTimeout }
	g, err := Start(w.deps(), 0, Options{Gate: gateFor(ownOwners), Now: c.read})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 403, map[string]any{"reason": "timeout"})
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 429, nil)
	c.now = c.now.Add(30 * time.Second)
	w.ask = func(consent.Question) consent.Answer { return consent.AnswerAllow }
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
}

func TestRefusalsFromThreeOriginsLockEveryDialog(t *testing.T) {
	ask, ran := dialog(t, 1, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	for _, o := range []string{"http://localhost:5171", "http://localhost:5172", "http://localhost:5173"} {
		wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, o), 403, map[string]any{"reason": "denied"})
	}
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, "http://localhost:5174"), 429, map[string]any{"reason": "cooldown"})
	if ran() != 3 {
		t.Fatalf("dialogs = %d", ran())
	}
}

func TestAnAllowClearsTheCountOfRefusals(t *testing.T) {
	w := newWorld()
	answers := []consent.Answer{consent.AnswerDeny, consent.AnswerDeny, consent.AnswerAllow, consent.AnswerDeny, consent.AnswerDeny, consent.AnswerDeny}
	w.ask = func(consent.Question) consent.Answer {
		a := answers[0]
		answers = answers[1:]
		return a
	}
	g := startGateway(t, w.deps(), nil)
	want := []int{403, 403, 200, 403, 403, 403}
	for i, status := range want {
		o := "http://localhost:" + strconv.Itoa(5170+i)
		wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, o), status, nil)
	}
	// Two refusals, an Allow, three refusals: the three after the Allow lock every dialog, so the next one is never asked.
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, "http://localhost:5180"), 429, nil)
}

func TestASecondRequestWhileADialogIsOpenIsBusy(t *testing.T) {
	ask, ran := dialog(t, 0, 2)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	first := make(chan reply, 1)
	go func() { first <- connectAs(t, g.Port, map[string]any{"mode": "browser"}, page) }()
	waitFor(t, "the dialog to open", wait, func() bool { return ran() == 1 })
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, other), 409, map[string]any{"reason": "busy"})
	wantReply(t, <-first, 200, nil)
}

func TestAMachineThatCannotShowADialogSaysSoAndGrantsNothing(t *testing.T) {
	w := newWorld()
	w.ask = askWith("/nonexistent/consent")
	g := startGateway(t, w.deps(), nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 503, map[string]any{"granted": false, "reason": "no-dialog"})
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 503, nil) // no cooldown: it was not a refusal
}

func TestAPairingCodeTheAppRefusesIs502AfterAllowAndNothingIsSaved(t *testing.T) {
	ask, _ := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	r := connectAs(t, g.Port, map[string]any{"mode": "server", "code": "WRONG"}, "https://app.example.com")
	wantReply(t, r, 502, map[string]any{"granted": false, "reason": "pairing", "message": "https://app.example.com refused the code"})
	if len(w.apps()) != 0 {
		t.Fatalf("saved %v", w.apps())
	}
}

func TestConnectingAnAppThatIsAlreadyPairedPairsItAgain(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	app := "https://app.example.com"
	body := map[string]any{"mode": "server", "code": code}
	wantReply(t, connectAs(t, g.Port, body, app), 200, nil)
	wantReply(t, connectAs(t, g.Port, body, app), 200, nil)
	if len(w.paired) != 2 || ran() != 2 {
		t.Fatalf("paired %v, dialogs %d", w.paired, ran())
	}
}

func TestDeleteDisconnectsTheCallersOwnApp(t *testing.T) {
	w := newWorld()
	app := "https://app.example.com"
	w.reg = w.reg.AddApp(registryApp(app))
	w.linked[app] = true
	g := startGateway(t, w.deps(), nil)
	wantReply(t, call(t, g.Port, "DELETE", "/v1/connect", callOpts{origin: app}), 200, map[string]any{"granted": false})
	if len(w.apps()) != 0 || len(w.revoked) != 1 || w.revoked[0] != app {
		t.Fatalf("apps %v, revoked %v", w.apps(), w.revoked)
	}
}

func TestOneOriginCannotSeeOrTouchAnothersApp(t *testing.T) {
	w := newWorld()
	app := "https://app.example.com"
	w.reg = w.reg.AddApp(registryApp(app))
	w.linked[app] = true
	g := startGateway(t, w.deps(), nil)
	asked := "?server=" + "https%3A%2F%2Fapp.example.com" // the old way to name another origin's app: ignored
	st := call(t, g.Port, "GET", "/v1/status"+asked, callOpts{origin: page})
	if server, browser := granted(t, st); server || browser {
		t.Fatalf("granted = %v %v", server, browser)
	}
	if _, has := st.body["agents"]; has {
		t.Fatal("agents for another origin")
	}
	wantReply(t, call(t, g.Port, "DELETE", "/v1/connect"+asked, callOpts{origin: page}), 200, nil)
	if apps := w.apps(); len(apps) != 1 || apps[0] != app {
		t.Fatalf("apps = %v", apps)
	}
	if len(w.revoked) != 1 || w.revoked[0] != page {
		t.Fatalf("revoked %v", w.revoked)
	}
}

func TestAnAppThatIsSavedButNotLinkedReadsAsNotGrantedAndIsOfferedAgain(t *testing.T) {
	// An app that revoked this daemon, or that Stop detached, stays in the registry and leaves the hub.
	ask, _ := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	app := "https://app.example.com"
	w.reg = w.reg.AddApp(registryApp(app))
	g := startGateway(t, w.deps(), nil)
	if server, _ := granted(t, status(t, g.Port, app)); server {
		t.Fatal("a saved app that is not linked reads as granted")
	}
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "server", "code": code}, app), 200, nil)
	if server, _ := granted(t, status(t, g.Port, app)); !server {
		t.Fatal("linked again, still not granted")
	}
}

func TestBrowserGrantAllowSavesTheOriginAndAGrantedOriginGetsNoDialog(t *testing.T) {
	ask, ran := dialog(t, 0, 0)
	w := newWorld()
	w.ask = ask
	g := startGateway(t, w.deps(), nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
	st := status(t, g.Port, page)
	if server, browser := granted(t, st); server || !browser {
		t.Fatalf("granted = %v %v", server, browser)
	}
	if _, has := st.body["agents"]; !has {
		t.Fatal("no agents for a granted origin")
	}
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
	if ran() != 1 {
		t.Fatalf("dialogs = %d, want 1", ran())
	}
}

func TestAGrantedOriginGetsItsAnswerWhileALockoutHoldsEveryDialog(t *testing.T) {
	w := newWorld()
	w.ask = func(consent.Question) consent.Answer { return consent.AnswerDeny }
	g := startGateway(t, w.deps(), nil)
	for i := range 3 {
		connectAs(t, g.Port, map[string]any{"mode": "browser"}, "http://localhost:"+strconv.Itoa(5170+i))
	}
	w.reg = w.reg.AddBrowser(page, "2026-10-19T00:00:00.000Z", nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 200, map[string]any{"granted": true})
}

// occupy listens on every port from a free base on, and returns the base.
func occupy(t *testing.T, count int) int {
	t.Helper()
	for base := 53100; base < 60000; base += 37 {
		var held []net.Listener
		for p := base; p < base+count; p++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
			if err != nil {
				break
			}
			held = append(held, l)
		}
		if len(held) == count {
			t.Cleanup(func() {
				for _, l := range held {
					_ = l.Close()
				}
			})
			return base
		}
		for _, l := range held {
			_ = l.Close()
		}
	}
	t.Fatal("no free range of ports")
	return 0
}

func TestWhenEveryPortOfTheRangeIsTakenThereIsNoGateway(t *testing.T) {
	base := occupy(t, PortRange)
	g, err := Start(newWorld().deps(), base, Options{Gate: gateFor(ownOwners)})
	if g != nil || !errors.Is(err, ErrPortsTaken) {
		t.Fatalf("Start = %v, %v; want ErrPortsTaken", g, err)
	}
}

func TestTheGatewayTakesTheNextFreePortOfTheRange(t *testing.T) {
	base := occupy(t, 3)
	g, err := Start(newWorld().deps(), base, Options{Gate: gateFor(ownOwners)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if g.Port != base+3 {
		t.Fatalf("port = %d, want %d", g.Port, base+3)
	}
}

func TestStartNeedsAGate(t *testing.T) {
	if _, err := Start(newWorld().deps(), 0, Options{}); err == nil {
		t.Fatal("Start without a gate")
	}
}

// ---- only programs of the daemon's own OS user reach the gateway ----

// gatewayHere is a gateway whose PAGE is granted as a browser origin and whose dialogs are answered Allow; an upgrade
// is recorded and the socket closed.
func gatewayHere(t *testing.T, owners consent.PeerOwners) (*Gateway, *world) {
	t.Helper()
	w := newWorld()
	w.reg = w.reg.AddBrowser(page, "2026-10-19T00:00:00.000Z", nil)
	deps := w.deps()
	deps.Upgrade = func(origin string, rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		w.upgraded = append(w.upgraded, origin)
		w.mu.Unlock()
		drop(rw)
	}
	return startGateway(t, deps, owners), w
}

// dialSession dials /v1/session as page and returns once the gateway hangs up or the upgrade completes.
func dialSession(t *testing.T, port int) error {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: wait}
	conn, res, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/v1/session", http.Header{"Origin": {page}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

func TestAConnectionFromAnotherUsersProcessIsRefusedOnEveryRouteAndUpgradeWithNoDialog(t *testing.T) {
	g, w := gatewayHere(t, func(consent.Connection) ([]int, error) { return []int{-1}, nil })
	for _, r := range []reply{
		status(t, g.Port, page),
		connectAs(t, g.Port, map[string]any{"mode": "browser"}, other),
		connectAs(t, g.Port, map[string]any{"mode": "server", "code": code}, other),
		call(t, g.Port, "DELETE", "/v1/connect", callOpts{origin: page}),
	} {
		wantReply(t, r, 403, map[string]any{"granted": false, "reason": "peer"})
	}
	if err := dialSession(t, g.Port); err == nil {
		t.Fatal("the upgrade of another user's process went through")
	}
	if len(w.asks()) != 0 || len(w.revoked) != 0 || len(w.upgraded) != 0 {
		t.Fatalf("asked %v, revoked %v, upgraded %v", w.asks(), w.revoked, w.upgraded)
	}
}

func TestAnOwnerThatCannotBeFoundOrALookupThatFailsIsRefusedToo(t *testing.T) {
	for name, owners := range map[string]consent.PeerOwners{
		"no owner":       func(consent.Connection) ([]int, error) { return nil, nil },
		"lookup failure": func(consent.Connection) ([]int, error) { return nil, errors.New("lsof is gone") },
	} {
		t.Run(name, func(t *testing.T) {
			g, w := gatewayHere(t, owners)
			wantReply(t, status(t, g.Port, page), 403, map[string]any{"reason": "peer"})
			_ = dialSession(t, g.Port)
			if len(w.upgraded) != 0 {
				t.Fatal("upgraded")
			}
		})
	}
}

func TestALookupThatFailsIsLoggedOnce(t *testing.T) {
	var warned []string
	gate := consent.NewPeerGate(func(consent.Connection) ([]int, error) { return nil, errors.New("lsof is gone") }, 1000, func(m string) { warned = append(warned, m) })
	w := newWorld()
	g, err := Start(w.deps(), 0, Options{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	status(t, g.Port, page)
	status(t, g.Port, page)
	if len(warned) != 1 {
		t.Fatalf("warned %d times: %v", len(warned), warned)
	}
}

func TestTheRealLookupLetsThisProcessInRoutesAnswerAndTheUpgradeGoesThrough(t *testing.T) {
	w := newWorld()
	w.reg = w.reg.AddBrowser(page, "2026-10-19T00:00:00.000Z", nil)
	deps := w.deps()
	deps.Upgrade = func(origin string, rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		w.upgraded = append(w.upgraded, origin)
		w.mu.Unlock()
		drop(rw)
	}
	g, err := Start(deps, 0, Options{Gate: consent.NewPeerGate(consent.SystemOwners(), osUID(), func(m string) { t.Log(m) })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	wantReply(t, status(t, g.Port, page), 200, nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, other), 200, nil)
	_ = dialSession(t, g.Port)
	if asks := w.asks(); len(asks) != 1 || asks[0].Origin != other {
		t.Fatalf("asked %v", asks)
	}
	if len(w.upgraded) != 1 || w.upgraded[0] != page {
		t.Fatalf("upgraded %v", w.upgraded)
	}
}

func TestAllowSavesTheFoldersTheUserChose(t *testing.T) {
	for _, mode := range []map[string]any{{"mode": "server", "code": code}, {"mode": "browser"}} {
		w := newWorld()
		w.chosen = []string{"/work/a", "/work/b"}
		g := startGateway(t, w.deps(), nil)
		wantReply(t, connectAs(t, g.Port, mode, page), 200, map[string]any{"granted": true})
		if got := append(w.reg.AppFolders(page), w.reg.BrowserFolders(page)...); !reflect.DeepEqual(got, []string{"/work/a", "/work/b"}) {
			t.Errorf("%v: saved folders = %v", mode["mode"], got)
		}
	}
}

func TestAnAllowWithoutAFolderGrantsNothing(t *testing.T) {
	w := newWorld()
	w.chosen = []string{}
	g := startGateway(t, w.deps(), nil)
	wantReply(t, connectAs(t, g.Port, map[string]any{"mode": "browser"}, page), 503, map[string]any{"reason": "no-dialog"})
	if len(w.reg.Browsers) != 0 || len(w.reg.Apps) != 0 {
		t.Fatalf("saved %+v", w.reg)
	}
}
