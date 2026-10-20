package goserver_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	csocket "github.com/zishang520/socket.io/clients/socket/v3"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

const (
	alice = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// clock is the Server's clock, which a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	server *goserver.Server
	tools  *fakeTools
	tokens *goserver.MemoryTokens
	// outage is the store the Server reads tokens through: e.tokens, until a test makes its lookups fail.
	outage *outage
	clock  *clock
	srv    *httptest.Server
}

var errDatabaseDown = errors.New("connection refused: the database is down")

// outage is a token store whose lookups fail, as a database's do while it is down: by hash (the handshake's first
// look) or by id (the look that a revoke did not race the handshake). With anyDelete it also accepts a Delete of a
// daemon the user does not own, as a store with a missing user condition would.
type outage struct {
	goserver.Tokens
	byHash, byID, anyDelete atomic.Bool
}

func (o *outage) Delete(userID, id string) error {
	if o.anyDelete.Load() {
		return nil
	}
	return o.Tokens.Delete(userID, id)
}

func (o *outage) ByHash(hash string) (goserver.Record, error) {
	if o.byHash.Load() {
		return goserver.Record{}, errDatabaseDown
	}
	return o.Tokens.ByHash(hash)
}

func (o *outage) ByID(userID, id string) (goserver.Record, error) {
	if o.byID.Load() {
		return goserver.Record{}, errDatabaseDown
	}
	return o.Tokens.ByID(userID, id)
}

// fakeTools is the MCP tool server of an app: it knows which session grants exist and for whom, and serves nothing.
type fakeTools struct {
	mu     sync.Mutex
	grants map[string][2]string // grant id -> user, daemon
	n      int
}

type grant struct{ ID string }

func newFakeTools() *fakeTools { return &fakeTools{grants: map[string][2]string{}} }

// Open starts a tool session for user's daemon and returns its grant.
func (f *fakeTools) Open(user, daemon string) grant {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := fmt.Sprintf("grant-%d", f.n)
	f.grants[id] = [2]string{user, daemon}
	return grant{ID: id}
}

func (f *fakeTools) Owns(user, daemon, grantID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants[grantID] == [2]string{user, daemon}
}

func (f *fakeTools) Serve(ctx context.Context, _, _ string, _ mcp.Transport) error {
	<-ctx.Done()
	return nil
}

func newEnv(t *testing.T) env {
	t.Helper()
	return newEnvWith(t, goserver.Options{})
}

// newEnvWith is newEnv with extra Server options; the test's own wiring (tokens, tools, clock) overrides them.
func newEnvWith(t *testing.T, extra goserver.Options) env {
	t.Helper()
	return buildEnv(t, extra, true)
}

// buildEnv is the Server of a test; withTools false leaves Options.Tools nil, an app that serves no MCP tools.
func buildEnv(t *testing.T, extra goserver.Options, withTools bool) env {
	t.Helper()
	e := env{tools: newFakeTools(), tokens: goserver.NewMemoryTokens(), clock: &clock{t: time.Now()}}
	e.outage = &outage{Tokens: e.tokens}
	extra.Tokens, extra.Now = e.outage, e.clock.now
	if withTools {
		extra.Tools = e.tools
	}
	e.server = goserver.New(extra)
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(goserver.MaxMessageBytes)
	io := socket.NewServer(nil, opts)
	mux := http.NewServeMux()
	e.server.Mount(io, mux)
	mux.Handle("/socket.io/", io.ServeHandler(nil))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() { io.Close(nil); e.srv.Close() })
	return e
}

// startCode is what the browser does to show a pairing code to user.
func (e env) startCode(t *testing.T, user string) string {
	t.Helper()
	list, err := e.server.StartPairing(user)
	if err != nil {
		t.Fatal(err)
	}
	return list.Pairing.Code
}

// pair runs the whole pairing of user, from a machine called "laptop": the browser asks for a code, the daemon trades
// it for a token.
func (e env) pair(t *testing.T, user string) string {
	t.Helper()
	return e.pairHost(t, user, "laptop")
}

func (e env) pairHost(t *testing.T, user, hostname string) string {
	t.Helper()
	token, status := e.postPairHost(t, e.startCode(t, user), hostname)
	if status != http.StatusOK {
		t.Fatalf("pair = %d", status)
	}
	return token
}

func (e env) postPair(t *testing.T, code string) (token string, status int) {
	t.Helper()
	return e.postPairHost(t, code, "laptop")
}

func (e env) postPairHost(t *testing.T, code, hostname string) (token string, status int) {
	t.Helper()
	return e.postPairFrom(t, code, hostname, "")
}

// postPairFrom posts a pairing as a client that a proxy reports as clientIP in the X-Test-Client-Ip header ("" sends none).
func (e env) postPairFrom(t *testing.T, code, hostname, clientIP string) (token string, status int) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"code": code, "hostname": hostname, "os": "darwin", "arch": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/agentd/pair", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if clientIP != "" {
		req.Header.Set("X-Test-Client-Ip", clientIP)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	answer, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(answer, &out); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
	}
	return out.Token, res.StatusCode
}

// idOf is the id of the daemon a token belongs to.
func (e env) idOf(t *testing.T, token string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	rec, err := e.tokens.ByHash(hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

// online pairs user, connects their daemon and says hello.
func (e env) online(t *testing.T, user string) *fakeDaemon {
	t.Helper()
	return e.onlineAs(t, user, "laptop")
}

// onlineAs is online for a machine with the given hostname.
func (e env) onlineAs(t *testing.T, user, hostname string) *fakeDaemon {
	t.Helper()
	return e.onlineWith(t, user, hostname, true)
}

// onlineWith is onlineAs for a daemon that does, or does not, run under a service.
func (e env) onlineWith(t *testing.T, user, hostname string, managed bool) *fakeDaemon {
	t.Helper()
	token := e.pairHost(t, user, hostname)
	d := connectDaemon(t, e.srv.URL, token)
	d.id, d.token = e.idOf(t, token), token
	d.hello(contract.Hello{Version: "0.1.0", Hostname: hostname, Os: "darwin", Arch: "arm64", Agents: allAgents(), Managed: managed})
	waitDaemon(t, e.server, user, d.id, "online")
	return d
}

func (e env) list(t *testing.T, user string) goserver.List {
	t.Helper()
	list, err := e.server.List(user)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func infoOf(list goserver.List, id string) (goserver.Info, bool) {
	for _, d := range list.Daemons {
		if d.ID == id {
			return d, true
		}
	}
	return goserver.Info{}, false
}

// waitDaemon waits until user's list shows daemon id in the state, and returns that entry.
func waitDaemon(t *testing.T, h *goserver.Server, user, id, want string) goserver.Info {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		list, err := h.List(user)
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := infoOf(list, id); ok && d.State == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon %s of %+v is not %q", id, list, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitGone waits until the daemon's socket is closed.
func waitGone(t *testing.T, d *fakeDaemon) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for d.sock.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("the daemon is still connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPairingTradesACodeForATokenThatIsStoredOnlyAsAHash(t *testing.T) {
	e := newEnv(t)
	list, err := e.server.StartPairing(alice)
	if err != nil {
		t.Fatal(err)
	}
	p := list.Pairing
	if len(p.Code) != 11 || p.Code[5] != '-' || p.ExpiresAt <= e.clock.now().UnixMilli() {
		t.Fatalf("pairing = %+v, want a code with an expiry", p)
	}
	token, status := e.postPair(t, strings.ToLower(p.Code)) // typed in lower case
	if status != http.StatusOK || !strings.HasPrefix(token, "agd_") {
		t.Fatalf("pair = %d %q", status, token)
	}
	rec, err := e.tokens.ByHash(hashOf(token))
	if err != nil || rec.UserID != alice || rec.Hostname != "laptop" || rec.OS != "darwin" || rec.Arch != "arm64" || len(rec.ID) != 32 {
		t.Fatalf("stored = %+v, %v", rec, err)
	}
	if strings.Contains(rec.TokenHash, token) || len(rec.TokenHash) != 64 {
		t.Fatalf("stored hash %q must not contain the token", rec.TokenHash)
	}
	after := e.list(t, alice)
	d, ok := infoOf(after, rec.ID)
	if !ok || d.State != "offline" || d.Name != "laptop" || d.OS != "darwin" || len(after.Daemons) != 1 || after.Pairing.Code != "" {
		t.Fatalf("list after pairing = %+v, want one offline daemon and no pending code", after)
	}
}

func namesOf(list goserver.List) []string {
	names := make([]string, len(list.Daemons))
	for i, d := range list.Daemons {
		names[i] = d.Name
	}
	return names
}

func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestACodeWorksOnce(t *testing.T) {
	e := newEnv(t)
	code := e.startCode(t, alice)
	if _, status := e.postPair(t, code); status != http.StatusOK {
		t.Fatalf("first use = %d", status)
	}
	if _, status := e.postPair(t, code); status != http.StatusForbidden {
		t.Fatalf("second use = %d, want 403", status)
	}
}

func TestAWrongOrExpiredCodeIsRefused(t *testing.T) {
	e := newEnv(t)
	code := e.startCode(t, alice)
	if _, status := e.postPair(t, "AAAAA-AAAAA"); status != http.StatusForbidden {
		t.Fatalf("wrong code = %d, want 403", status)
	}
	e.clock.add(10*time.Minute + time.Second)
	if _, status := e.postPair(t, code); status != http.StatusForbidden {
		t.Fatalf("expired code = %d, want 403", status)
	}
	if list := e.list(t, alice); len(list.Daemons) != 0 || list.Pairing.Code != "" {
		t.Fatalf("list = %+v, want an expired code to leave no pairing and no daemon", list)
	}
}

func TestAUserHasOnePendingCodeAndAskingAgainReplacesIt(t *testing.T) {
	e := newEnv(t)
	first := e.startCode(t, alice)
	second := e.startCode(t, alice)
	if first == second {
		t.Fatal("a new code was not issued")
	}
	if got := e.list(t, alice).Pairing.Code; got != second {
		t.Fatalf("pending code = %q, want the second", got)
	}
	if _, status := e.postPair(t, first); status != http.StatusForbidden {
		t.Fatalf("replaced code = %d, want 403", status)
	}
	if _, status := e.postPair(t, second); status != http.StatusOK {
		t.Fatalf("current code = %d", status)
	}
}

func TestCancellingPairingForgetsTheCode(t *testing.T) {
	e := newEnv(t)
	code := e.startCode(t, alice)
	list, err := e.server.CancelPairing(alice)
	if err != nil || list.Pairing.Code != "" {
		t.Fatalf("cancel = %+v, %v", list, err)
	}
	if _, status := e.postPair(t, code); status != http.StatusForbidden {
		t.Fatalf("a cancelled code = %d, want 403", status)
	}
	if list, err := e.server.CancelPairing(alice); err != nil || len(list.Daemons) != 0 {
		t.Fatalf("cancelling with nothing pending = %+v, %v", list, err)
	}
}

func TestACodeIsOnlyForItsUser(t *testing.T) {
	e := newEnv(t)
	code := e.startCode(t, alice)
	e.startCode(t, bob)
	if _, status := e.postPair(t, code); status != http.StatusOK {
		t.Fatalf("alice's code = %d", status)
	}
	if got := e.list(t, alice); len(got.Daemons) != 1 {
		t.Fatalf("alice = %+v", got)
	}
	if got := e.list(t, bob); len(got.Daemons) != 0 || got.Pairing.Code == "" {
		t.Fatalf("bob = %+v, want his own code still pending and no daemon", got)
	}
}

func TestEveryPairingAddsADaemonNamedAfterItsMachine(t *testing.T) {
	e := newEnv(t)
	first := e.pairHost(t, alice, "mac")
	second := e.pairHost(t, alice, "mac") // the same machine again
	third := e.pairHost(t, alice, "mac")
	other := e.pairHost(t, alice, "box")
	bobs := e.pairHost(t, bob, "mac")
	if first == second || e.idOf(t, first) == e.idOf(t, second) {
		t.Fatal("two pairings share a token or an id")
	}
	names := namesOf(e.list(t, alice))
	if !slices.Equal(names, []string{"mac", "mac (2)", "mac (3)", "box"}) {
		t.Fatalf("names = %q", names)
	}
	if got := e.list(t, bob).Daemons; len(got) != 1 || got[0].Name != "mac" {
		t.Fatalf("bob's list = %+v: names are per user", got)
	}
	// The first entry is still its machine's: every token keeps working.
	for _, token := range []string{first, second, third, other, bobs} {
		if _, refused := dial(t, e.srv.URL, map[string]any{"role": "daemon", "token": token}); refused != nil {
			t.Fatalf("a paired token was refused: %v", refused)
		}
	}
}

func TestAHostnameThatIsEmptyOrHasControlCharactersGivesAUsableName(t *testing.T) {
	e := newEnv(t)
	e.pairHost(t, alice, "")
	e.pairHost(t, alice, "evil\x1b[31m\nname")
	e.pairHost(t, alice, strings.Repeat("h", 200))
	names := namesOf(e.list(t, alice))
	if len(names) != 3 || names[0] != "Daemon" || names[1] != "evil[31mname" || len([]rune(names[2])) != 64 {
		t.Fatalf("names = %q", names)
	}
}

func TestWrongCodesLockOnlyTheClientTheProxyNamed(t *testing.T) {
	e := newEnvWith(t, goserver.Options{ClientIPHeader: "X-Test-Client-Ip"})
	code := e.startCode(t, alice)
	for range 10 {
		if _, status := e.postPairFrom(t, "AAAAA-AAAAA", "laptop", "203.0.113.1"); status != http.StatusForbidden {
			t.Fatalf("a wrong code = %d, want 403", status)
		}
	}
	if _, status := e.postPairFrom(t, code, "laptop", "203.0.113.1"); status != http.StatusTooManyRequests {
		t.Fatalf("the locked client = %d, want 429", status)
	}
	if _, status := e.postPairFrom(t, code, "laptop", "203.0.113.2"); status != http.StatusOK {
		t.Fatalf("another client behind the same proxy = %d, want 200", status)
	}
}

func TestManyWrongCodesLockPairingForAMinute(t *testing.T) {
	e := newEnv(t)
	code := e.startCode(t, alice)
	for range 10 {
		if _, status := e.postPair(t, "AAAAA-AAAAA"); status != http.StatusForbidden {
			t.Fatalf("a wrong code = %d, want 403", status)
		}
	}
	if _, status := e.postPair(t, code); status != http.StatusTooManyRequests {
		t.Fatalf("the right code during the lock = %d, want 429", status)
	}
	e.clock.add(time.Minute + time.Second)
	if _, status := e.postPair(t, code); status != http.StatusOK {
		t.Fatalf("the right code after the lock = %d, want 200", status)
	}
}

func TestPairingRejectsAMalformedRequest(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/agentd/pair", strings.NewReader("not json"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	get, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+"/agentd/pair", http.NoBody)
	res, err = http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatalf("GET status = %d: pairing is a POST", res.StatusCode)
	}
}

func TestAHandshakeWithoutAValidTokenOrRoleIsRefused(t *testing.T) {
	e := newEnv(t)
	token := e.pair(t, alice)
	for name, auth := range map[string]map[string]any{
		"no auth":       nil,
		"wrong token":   {"role": "daemon", "token": "agd_nope"},
		"no token":      {"role": "daemon"},
		"no role":       {"token": token},
		"unknown role":  {"role": "admin", "token": token},
		"daemon, empty": {"role": "daemon", "token": ""},
	} {
		if _, refused := dial(t, e.srv.URL, auth); refused == nil {
			t.Errorf("%s: the handshake was accepted", name)
		}
	}
	if _, refused := dial(t, e.srv.URL, map[string]any{"role": "daemon", "token": token}); refused != nil {
		t.Fatalf("the right token was refused: %v", refused)
	}
}

func TestABridgeMustPresentASessionOfItsOwnUser(t *testing.T) {
	e := newEnv(t)
	aliceToken, bobToken := e.pair(t, alice), e.pair(t, bob)
	aliceDaemonID, bobDaemonID := e.idOf(t, aliceToken), e.idOf(t, bobToken)
	bobsGrant := e.tools.Open(bob, bobDaemonID)
	alicesGrant := e.tools.Open(alice, aliceDaemonID)

	if _, refused := bridge(t, e.srv.URL, aliceToken, bobsGrant.ID); refused == nil {
		t.Fatal("alice's bridge reached bob's tool session")
	}
	if _, refused := bridge(t, e.srv.URL, aliceToken, "no-such-session"); refused == nil {
		t.Fatal("a bridge reached a session that does not exist")
	}
	if _, refused := bridge(t, e.srv.URL, aliceToken, ""); refused == nil {
		t.Fatal("a bridge without a session was accepted")
	}
	if _, refused := bridge(t, e.srv.URL, "agd_nope", alicesGrant.ID); refused == nil {
		t.Fatal("a bridge with a wrong token was accepted")
	}
	if _, refused := bridge(t, e.srv.URL, bobToken, bobsGrant.ID); refused != nil {
		t.Fatalf("bob's own bridge was refused: %v", refused)
	}
}

// listsOf follows the lists pushed to a tab of user.
type listsOf struct {
	t  *testing.T
	ch <-chan goserver.List
}

func watch(t *testing.T, h *goserver.Server, user string) listsOf {
	t.Helper()
	ch, cancel := h.Watch(user)
	t.Cleanup(cancel)
	return listsOf{t: t, ch: ch}
}

// until reads pushed lists until one satisfies ok, and returns it.
func (w listsOf) until(what string, ok func(goserver.List) bool) goserver.List {
	w.t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case l := <-w.ch:
			if ok(l) {
				return l
			}
		case <-deadline:
			w.t.Fatalf("no list pushed where %s", what)
			return goserver.List{}
		}
	}
}

func (w listsOf) silent(d time.Duration) {
	w.t.Helper()
	select {
	case l := <-w.ch:
		w.t.Fatalf("a tab was told %+v", l)
	case <-time.After(d):
	}
}

func TestADaemonIsOnlineAfterHelloAndOfflineAfterItLeaves(t *testing.T) {
	e := newEnv(t)
	tab := watch(t, e.server, alice)
	if first := tab.until("it is the first", func(goserver.List) bool { return true }); len(first.Daemons) != 0 || first.Pairing.Code != "" {
		t.Fatalf("first list = %+v, want nothing", first)
	}
	token := e.pair(t, alice)
	id := e.idOf(t, token)
	d := connectDaemon(t, e.srv.URL, token)
	if _, err := e.server.Ready(alice, id); !errors.As(err, new(goserver.OfflineError)) {
		t.Fatalf("Ready before hello: %v, want the daemon offline", err)
	}
	d.hello(contract.Hello{Version: "0.1.0", Hostname: "laptop-renamed", Os: "darwin", Arch: "arm64", Agents: allAgents()})
	online := tab.until("the daemon is online", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Daemons[0].State == "online" }).Daemons[0]
	if online.ID != id || online.Name != "laptop" || online.Hostname != "laptop-renamed" || online.Version != "0.1.0" || online.OS != "darwin" || len(online.Agents) != 3 {
		t.Fatalf("online = %+v: the name is the user's, the hostname the machine's", online)
	}
	// The agents come as the daemon reported them: its order, its titles and its reasons.
	if a := online.Agents; a[0].Name != "claude" || a[0].Title != "Claude Code" || a[1].Name != "codex" || a[1].Ready || a[1].Reason != "Node is not installed" || a[2].Name != "grok" {
		t.Fatalf("agents = %+v", a)
	}
	if ready, err := e.server.Ready(alice, id); err != nil || len(ready) != 2 {
		t.Fatalf("ready = %v, %v", ready, err)
	}
	e.clock.add(time.Hour)
	d.disconnect()
	offline := tab.until("the daemon is offline", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Daemons[0].State == "offline" }).Daemons[0]
	if offline.Name != "laptop" || offline.OS != "darwin" || len(offline.Agents) != 0 || offline.LastSeen != e.clock.now().UnixMilli() {
		t.Fatalf("offline = %+v, want the paired machine, no agents, and the time it left", offline)
	}
	if got := e.list(t, alice).Daemons[0].LastSeen; got != offline.LastSeen {
		t.Fatalf("last seen is %v after a fresh list, want the stored %v", got, offline.LastSeen)
	}
}

func TestBobsTabNeverHearsAboutAlicesDaemons(t *testing.T) {
	e := newEnv(t)
	tab := watch(t, e.server, bob)
	tab.until("the first list", func(goserver.List) bool { return true })
	d := e.online(t, alice)
	e.server.Rename(alice, d.id, "mine")
	e.server.StartPairing(alice)
	e.server.Revoke(alice, d.id)
	tab.silent(200 * time.Millisecond)
}

func TestTwoDaemonsOfOneUserAreOnlineTogether(t *testing.T) {
	e := newEnv(t)
	laptop := e.onlineAs(t, alice, "laptop")
	desktop := e.onlineAs(t, alice, "desktop")
	list := e.list(t, alice)
	if len(list.Daemons) != 2 || list.Daemons[0].ID != laptop.id || list.Daemons[1].ID != desktop.id ||
		list.Daemons[0].State != "online" || list.Daemons[1].State != "online" {
		t.Fatalf("list = %+v, want both online, in the order they were paired", list)
	}
	if got := e.server.Online(alice); len(got) != 2 || got[0].ID != desktop.id || got[0].Name != "desktop" || got[1].ID != laptop.id {
		t.Fatalf("Online = %+v, want the one that came online last first", got)
	}
	// A daemon that reconnects is the newest again.
	laptop.disconnect()
	waitDaemon(t, e.server, alice, laptop.id, "offline")
	if got := e.server.Online(alice); len(got) != 1 || got[0].ID != desktop.id {
		t.Fatalf("Online = %+v, want only the desktop", got)
	}
	back := connectDaemon(t, e.srv.URL, laptop.token)
	back.hello(contract.Hello{Version: "0.1.0", Hostname: "laptop", Agents: allAgents()})
	waitDaemon(t, e.server, alice, laptop.id, "online")
	if got := e.server.Online(alice); len(got) != 2 || got[0].ID != laptop.id {
		t.Fatalf("Online = %+v, want the laptop first after it came back", got)
	}
	if got := e.server.Online(bob); len(got) != 0 {
		t.Fatalf("bob's online daemons = %+v", got)
	}
}

func TestRenamingChangesTheNameEverywhereAndTellsEveryTab(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	one, two := watch(t, e.server, alice), watch(t, e.server, alice)
	list, err := e.server.Rename(alice, d.id, "  Work Mac  ")
	if err != nil || list.Daemons[0].Name != "Work Mac" {
		t.Fatalf("rename = %+v, %v, want the trimmed name", list, err)
	}
	for _, tab := range []listsOf{one, two} {
		got := tab.until("the new name", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Daemons[0].Name == "Work Mac" })
		if got.Daemons[0].State != "online" {
			t.Fatalf("pushed = %+v", got)
		}
	}
	if got := e.server.Online(alice); got[0].Name != "Work Mac" {
		t.Fatalf("Online = %+v, want the new name", got)
	}
	if name, ok := e.server.Name(alice, d.id); !ok || name != "Work Mac" {
		t.Fatalf("Name = %q, %v", name, ok)
	}
	if list, err := e.server.Rename(alice, d.id, "Work Mac"); err != nil || list.Daemons[0].Name != "Work Mac" {
		t.Fatalf("keeping the name: %+v, %v", list, err)
	}
}

func TestRenamingRefusesANameThatIsNotAGoodOne(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	e.pairHost(t, alice, "box")
	for name, bad := range map[string]string{
		"empty": "", "blanks": " \t ", "too long": strings.Repeat("n", 65), "newline": "a\nb", "escape": "a\x1bb", "nul": "a\x00b",
		"tab inside": "a\tb", "bidi override": "a" + string(rune(0x202e)) + "b", "zero width": "a" + string(rune(0x200b)) + "b", "taken": "box",
	} {
		if _, err := e.server.Rename(alice, d.id, bad); err == nil {
			t.Errorf("%s: %q was accepted", name, bad)
		}
	}
	if got := e.list(t, alice).Daemons[0].Name; got != "laptop" {
		t.Fatalf("name = %q after refused renames", got)
	}
	for _, good := range []string{strings.Repeat("n", 64), "x", "Mac Studio 2", "máy tính", "机器"} {
		if list, err := e.server.Rename(alice, d.id, good); err != nil || list.Daemons[0].Name != good {
			t.Errorf("%q: %+v, %v", good, list, err)
		}
	}
}

func TestAnotherUsersDaemonCannotBeRenamedOrRevoked(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	if _, err := e.server.Rename(bob, d.id, "stolen"); !errors.Is(err, goserver.ErrNoSuchDaemon) {
		t.Fatalf("bob renamed alice's daemon: %v", err)
	}
	if _, err := e.server.Revoke(bob, d.id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
		t.Fatalf("bob revoked alice's daemon: %v", err)
	}
	for _, id := range []string{"", "nope", strings.Repeat("0", 32)} {
		if _, err := e.server.Rename(alice, id, "x"); !errors.Is(err, goserver.ErrNoSuchDaemon) {
			t.Fatalf("renaming %q: %v", id, err)
		}
		if _, err := e.server.Revoke(alice, id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
			t.Fatalf("revoking %q: %v", id, err)
		}
	}
	got := waitDaemon(t, e.server, alice, d.id, "online")
	if got.Name != "laptop" || !d.sock.Connected() {
		t.Fatalf("alice's daemon = %+v connected=%v: another user's calls changed it", got, d.sock.Connected())
	}
}

// The Server does not rely on the store alone to keep one user off another's daemon: a Delete the store wrongly accepts
// still leaves the other user's socket and bridges alone.
func TestARevokeTheStoreWronglyAcceptsDoesNotDropAnotherUsersDaemon(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	_, bridgeSock, refused := bridgeSocket(t, e.srv.URL, d.token, e.tools.Open(alice, d.id).ID)
	if refused != nil {
		t.Fatalf("the bridge was refused: %v", refused)
	}
	e.outage.anyDelete.Store(true)
	if _, err := e.server.Revoke(bob, d.id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a disconnect, had one been sent, reaches the client by now
	if !d.sock.Connected() || !bridgeSock.Connected() {
		t.Fatalf("bob's revoke dropped alice's daemon (connected=%v) or its bridge (connected=%v)", d.sock.Connected(), bridgeSock.Connected())
	}
	if got := e.server.Online(alice); len(got) != 1 || got[0].ID != d.id {
		t.Fatalf("Online(alice) = %+v: bob's revoke took alice's daemon offline", got)
	}
}

func TestRevokingCutsOffThatDaemonAndItsBridgesAndLeavesTheOthers(t *testing.T) {
	e := newEnv(t)
	doomed := e.onlineAs(t, alice, "laptop")
	kept := e.onlineAs(t, alice, "desktop")
	// Each daemon gets its own grant: a grant is bound to the daemon that will bridge it.
	doomedGrant := e.tools.Open(alice, doomed.id)
	keptGrant := e.tools.Open(alice, kept.id)
	_, bridgeSock, refused := bridgeSocket(t, e.srv.URL, doomed.token, doomedGrant.ID)
	if refused != nil {
		t.Fatalf("the bridge was refused: %v", refused)
	}
	keptBridge, keptSock, refused := bridgeSocket(t, e.srv.URL, kept.token, keptGrant.ID)
	if refused != nil || keptBridge == nil {
		t.Fatalf("the other daemon's bridge was refused: %v", refused)
	}
	tab := watch(t, e.server, alice)
	conn, err := e.server.Open(context.Background(), alice, doomed.id, "claude")
	if err != nil {
		t.Fatal(err)
	}

	list, err := e.server.Revoke(alice, doomed.id)
	if err != nil || len(list.Daemons) != 1 || list.Daemons[0].ID != kept.id || list.Daemons[0].State != "online" {
		t.Fatalf("revoke = %+v, %v, want only the other daemon, online", list, err)
	}
	tab.until("the list without it", func(l goserver.List) bool { return len(l.Daemons) == 1 })
	waitGone(t, doomed)
	deadline := time.Now().Add(waitFor)
	for bridgeSock.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("the revoked daemon's MCP bridge is still connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, refused := dial(t, e.srv.URL, map[string]any{"role": "daemon", "token": doomed.token}); refused == nil {
		t.Fatal("the revoked token was accepted")
	}
	if _, refused := bridge(t, e.srv.URL, doomed.token, doomedGrant.ID); refused == nil {
		t.Fatal("the revoked token opened a bridge")
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Done():
	case <-time.After(waitFor):
		t.Fatal("a session on the revoked daemon never ended")
	}
	if _, err := e.server.Open(context.Background(), alice, doomed.id, "claude"); !errors.Is(err, goserver.ErrUnknownDaemon) {
		t.Fatalf("Open after revoke: %v, want ErrUnknownDaemon", err)
	}
	if got := e.server.Online(alice); len(got) != 1 || got[0].ID != kept.id {
		t.Fatalf("Online = %+v", got)
	}
	if !kept.sock.Connected() || !keptSock.Connected() {
		t.Fatal("revoking one daemon disconnected another")
	}
	if _, err := e.server.Revoke(alice, doomed.id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
		t.Fatalf("revoking twice: %v", err)
	}
}

// A daemon that connects while it is being revoked never stays connected, whichever way the two interleave.
func TestADaemonConnectingWhileItIsRevokedDoesNotStayOnline(t *testing.T) {
	e := newEnv(t)
	for range 6 {
		token := e.pair(t, alice)
		id := e.idOf(t, token)
		var wg sync.WaitGroup
		var sock *csocket.Socket
		wg.Go(func() {
			s, _, _ := dialOnce(t, e.srv.URL, map[string]any{"role": "daemon", "token": token})
			sock = s
		})
		wg.Go(func() {
			if _, err := e.server.Revoke(alice, id); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		if sock != nil {
			t.Cleanup(func() { sock.Close() })
		}
		deadline := time.Now().Add(waitFor)
		for sock != nil && sock.Connected() {
			if time.Now().After(deadline) {
				t.Fatal("a revoked daemon is still connected")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if got := e.server.Online(alice); len(got) != 0 {
			t.Fatalf("Online = %+v after a revoke", got)
		}
	}
}

func TestPairingCodeChangesReachTheTabs(t *testing.T) {
	e := newEnv(t)
	tab := watch(t, e.server, alice)
	tab.until("the first list", func(goserver.List) bool { return true })
	code := e.startCode(t, alice)
	tab.until("the pending code", func(l goserver.List) bool { return l.Pairing.Code == code })
	e.server.CancelPairing(alice)
	tab.until("no pending code", func(l goserver.List) bool { return l.Pairing.Code == "" })
	code = e.startCode(t, alice)
	tab.until("the new code", func(l goserver.List) bool { return l.Pairing.Code == code })
	e.postPair(t, code)
	tab.until("the new daemon and no code", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Pairing.Code == "" })
}

func TestOpeningOnADaemonThatIsOfflineOrNotTheUsersSaysSo(t *testing.T) {
	e := newEnv(t)
	token := e.pairHost(t, alice, "work mac")
	id := e.idOf(t, token)
	_, err := e.server.Open(context.Background(), alice, id, "claude")
	var offline goserver.OfflineError
	if !errors.As(err, &offline) || offline.Name != "work mac" || !strings.Contains(err.Error(), `"work mac"`) || !strings.Contains(err.Error(), "pick another") {
		t.Fatalf("Open on an offline daemon: %v, want it named", err)
	}
	if _, err := e.server.Ready(alice, id); !errors.As(err, &offline) {
		t.Fatalf("Ready on an offline daemon: %v", err)
	}
	if _, err := e.server.Open(context.Background(), bob, id, "claude"); !errors.Is(err, goserver.ErrUnknownDaemon) {
		t.Fatalf("bob opened on alice's daemon: %v", err)
	}
	if _, err := e.server.Open(context.Background(), alice, "", "claude"); !errors.Is(err, goserver.ErrUnknownDaemon) {
		t.Fatalf("Open without a daemon id: %v", err)
	}
	if name, ok := e.server.Name(alice, id); !ok || name != "work mac" {
		t.Fatalf("Name = %q, %v", name, ok)
	}
	if _, ok := e.server.Name(bob, id); ok {
		t.Fatal("bob learned the name of alice's daemon")
	}
}

func TestHelloIsAnsweredWithTheProtocolTheHubSpeaks(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	got := d.hello(contract.Hello{Version: "0.1.0", Hostname: "laptop", Agents: allAgents()})
	if got != (contract.HelloReply{Protocol: goserver.Protocol}) || goserver.Protocol != 1 {
		t.Fatalf("hello = %+v, want protocol %d", got, goserver.Protocol)
	}
}

func TestTheListCarriesTheLatestVersionTheAppSays(t *testing.T) {
	latest := ""
	e := newEnvWith(t, goserver.Options{LatestVersion: func() string { return latest }})
	if got := e.list(t, alice).LatestVersion; got != "" {
		t.Fatalf("latest version %q before the app says one", got)
	}
	latest = "1.4.0"
	if got := e.list(t, alice).LatestVersion; got != "1.4.0" {
		t.Fatalf("latest version %q", got)
	}
}

func TestAnAppWithoutToolsRefusesEveryBridgeAndDoesNotPanic(t *testing.T) {
	e := buildEnv(t, goserver.Options{}, false)
	token := e.pair(t, alice)
	for _, session := range []string{"", "x", "grant-1"} {
		if _, refused := bridge(t, e.srv.URL, token, session); refused == nil {
			t.Fatalf("a bridge for session %q was accepted by a server that serves no tools", session)
		}
	}
	d := e.online(t, alice) // the daemon's own socket is not a bridge: it still connects
	if d.id == "" {
		t.Fatal("the daemon did not connect")
	}
}

func TestSessionsStayOnTheirOwnDaemon(t *testing.T) {
	e := newEnv(t)
	laptop := e.onlineAs(t, alice, "laptop")
	desktop := e.onlineAs(t, alice, "desktop")
	onLaptop, err := e.server.Open(context.Background(), alice, laptop.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	onDesktop, err := e.server.Open(context.Background(), alice, desktop.id, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if got := laptop.openedSessions(); len(got) != 1 || !strings.HasSuffix(got[0], ":claude") {
		t.Fatalf("laptop opened %v", got)
	}
	if got := desktop.openedSessions(); len(got) != 1 || !strings.HasSuffix(got[0], ":grok") {
		t.Fatalf("desktop opened %v", got)
	}
	laptopSession, _, _ := strings.Cut(laptop.openedSessions()[0], ":")
	desktop.frame(laptopSession, contract.FrameKindOut, "forged by the other daemon", 0) // not its session
	laptop.frame(laptopSession, contract.FrameKindOut, "genuine", 0)
	if got := readLine(t, bufio.NewReader(onLaptop)); got != "genuine\n" {
		t.Fatalf("got %q: one daemon spoke for another's session", got)
	}
	// One daemon leaving ends its sessions only.
	laptop.disconnect()
	select {
	case <-onLaptop.Done():
	case <-time.After(waitFor):
		t.Fatal("the laptop's session never ended")
	}
	select {
	case <-onDesktop.Done():
		t.Fatal("the desktop's session ended with the laptop")
	case <-time.After(100 * time.Millisecond):
	}
	t.Cleanup(onDesktop.Close)
}

func TestAnotherConnectionOfTheSameDaemonReplacesTheFirst(t *testing.T) {
	e := newEnv(t)
	token := e.pair(t, alice)
	id := e.idOf(t, token)
	first := connectDaemon(t, e.srv.URL, token)
	first.hello(contract.Hello{Agents: allAgents()})
	second := connectDaemon(t, e.srv.URL, token)
	waitGone(t, first)
	second.hello(contract.Hello{Agents: allAgents()})
	waitDaemon(t, e.server, alice, id, "online")
	if got := e.server.Online(alice); len(got) != 1 {
		t.Fatalf("Online = %+v, want the daemon once", got)
	}
}

func TestOpeningWithoutAnyDaemonIsRefused(t *testing.T) {
	e := newEnv(t)
	_, err := e.server.Open(context.Background(), alice, "any", "claude")
	if !errors.Is(err, goserver.ErrUnknownDaemon) {
		t.Fatalf("err = %v", err)
	}
	if got := e.server.Online(alice); len(got) != 0 {
		t.Fatalf("Online = %+v", got)
	}
}

func TestOpeningAnAgentThatIsNotReadyGivesTheDaemonsReason(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	_, err := e.server.Open(context.Background(), alice, d.id, "codex")
	if err == nil || err.Error() != "Codex is not ready on laptop: Node is not installed" {
		t.Fatalf("err = %v, want the reason the daemon gave", err)
	}
	if _, err := e.server.Open(context.Background(), alice, d.id, "gemini"); err == nil || err.Error() != "laptop does not offer gemini" {
		t.Fatalf("an agent the daemon does not offer: %v", err)
	}
	if got := d.openedSessions(); len(got) != 0 {
		t.Fatalf("the daemon was asked to open %v", got)
	}
}

func TestADaemonThatFailsToOpenReportsItsMessage(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	d.onOpen = func(string, string) (contract.Opened, error) {
		return contract.Opened{}, errors.New("npx: not found")
	}
	_, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err == nil || !strings.Contains(err.Error(), "npx: not found") {
		t.Fatalf("err = %v", err)
	}
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line := make(chan string, 1)
	go func() { s, _ := r.ReadString('\n'); line <- s }()
	select {
	case s := <-line:
		return s
	case <-time.After(waitFor):
		t.Fatal("no line arrived")
		return ""
	}
}

func TestASessionCarriesStdoutLinesInOrderAndStdinLinesToTheDaemon(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	if conn.Cwd() != "/work" {
		t.Fatalf("cwd = %q, want the one the daemon answered", conn.Cwd())
	}
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	const n = 500
	go func() {
		for i := range n {
			d.frame(sid, contract.FrameKindOut, fmt.Sprintf("line %d", i), 0)
		}
	}()
	r := bufio.NewReader(conn)
	for i := range n {
		if got, want := readLine(t, r), fmt.Sprintf("line %d\n", i); got != want {
			t.Fatalf("line %d = %q, want %q: frames must keep their order", i, got, want)
		}
	}
	if _, err := io.WriteString(conn, "one\ntw"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "o\n"); err != nil {
		t.Fatal(err)
	}
	if got := d.writtenTo(sid); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("daemon got %q, want whole lines", got)
	}
}

func TestALargeLineGoesThrough(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	big := strings.Repeat("x", 5<<20) // above the engine's default of 1 MB
	d.frame(sid, contract.FrameKindOut, big, 0)
	if got := readLine(t, bufio.NewReaderSize(conn, 1<<20)); len(got) != len(big)+1 {
		t.Fatalf("got %d bytes, want %d", len(got), len(big)+1)
	}
}

func TestASessionWhoseOutputIsNotReadEndsInsteadOfGrowing(t *testing.T) {
	const limit = 1 << 20
	e := newEnvWith(t, goserver.Options{MaxUnreadBytes: limit})
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	line := strings.Repeat("x", 64<<10)
	for range limit/len(line) + 1 { // nobody reads: one line more than the session keeps unread
		d.frame(sid, contract.FrameKindOut, line, 0)
	}
	select {
	case <-conn.Done():
	case <-time.After(waitFor):
		t.Fatal("a session nobody reads kept growing: Done never closed")
	}
	if n, err := io.Copy(io.Discard, conn); err != nil || n != 0 {
		t.Fatalf("read %d bytes, %v: the ended session's unread output must be dropped, then EOF", n, err)
	}
	if got := conn.Stderr(); !strings.Contains(got, "not read") {
		t.Fatalf("stderr = %q: it must say why the session ended", got)
	}
	deadline := time.Now().Add(waitFor)
	for !slices.Contains(d.closedSessions(), sid) {
		if time.Now().After(deadline) {
			t.Fatal("the daemon was never told to end the agent")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Under the bound, output nobody has read yet is kept whole: the session ends only past it.
func TestOutputUpToTheUnreadBoundIsKept(t *testing.T) {
	const limit = 1 << 20
	e := newEnvWith(t, goserver.Options{MaxUnreadBytes: limit})
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	line := strings.Repeat("x", 64<<10-1) // with its newline, limit/64 KiB lines fill the bound exactly
	for range limit / (len(line) + 1) {
		d.frame(sid, contract.FrameKindOut, line, 0)
	}
	d.frame(sid, contract.FrameKindExit, "", 0)
	all, err := io.ReadAll(conn)
	if err != nil || len(all) != limit {
		t.Fatalf("read %d bytes, %v, want %d", len(all), err, limit)
	}
}

func TestAnExitFrameEndsTheStreamAndKeepsStderr(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	d.frame(sid, contract.FrameKindOut, "last words", 0)
	d.frame(sid, contract.FrameKindErr, "adapter crashed\n", 0)
	d.frame(sid, contract.FrameKindExit, "", 3)
	all, err := io.ReadAll(conn)
	if err != nil || string(all) != "last words\n" {
		t.Fatalf("stdout = %q, %v: what was relayed before the exit is still read, then EOF", all, err)
	}
	select {
	case <-conn.Done():
	case <-time.After(waitFor):
		t.Fatal("Done never closed")
	}
	if got := conn.Stderr(); !strings.Contains(got, "adapter crashed") || !strings.Contains(got, "exited with code 3") {
		t.Fatalf("stderr = %q", got)
	}
	if _, err := io.WriteString(conn, "x\n"); err == nil {
		t.Fatal("writing to an ended session must fail")
	}
}

func TestClosingASessionTellsTheDaemon(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	conn.Close()
	conn.Close() // a second Close sends nothing more
	deadline := time.Now().Add(waitFor)
	for len(d.closedSessions()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the daemon was never told to close")
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.frame(sid, contract.FrameKindExit, "", 0)
	select {
	case <-conn.Done():
	case <-time.After(waitFor):
		t.Fatal("Done never closed after the exit report")
	}
	if got := d.closedSessions(); len(got) != 1 || got[0] != sid {
		t.Fatalf("closed = %v", got)
	}
}

func TestAFrameForASessionOfAnotherUsersDaemonIsIgnored(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	mallory := e.online(t, bob)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	mallory.frame(sid, contract.FrameKindOut, "forged", 0)
	mallory.frame(sid, contract.FrameKindExit, "", 0)
	d.frame(sid, contract.FrameKindOut, "genuine", 0)
	if got := readLine(t, bufio.NewReader(conn)); got != "genuine\n" {
		t.Fatalf("got %q: bob's daemon spoke for alice's session", got)
	}
}

func TestTheDaemonLeavingEndsItsSessionsAndSaysWhy(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	d.disconnect()
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Done():
	case <-time.After(waitFor):
		t.Fatal("Done never closed")
	}
	if !strings.Contains(conn.Stderr(), "the daemon disconnected") {
		t.Fatalf("stderr = %q", conn.Stderr())
	}
}

// get is a GET of path on the test server.
func (e env) get(t *testing.T, path string) (status int, header http.Header, body string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+path, http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(raw)
}

func TestRestartAndStopTellTheDaemonAndLeaveItsSocketToIt(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	tab := watch(t, e.server, alice)
	tab.until("the first list", func(goserver.List) bool { return true })

	if _, err := e.server.Restart(alice, d.id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.server.Stop(alice, d.id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(waitFor)
	for len(d.told()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the daemon was told %v", d.told())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := d.told(); !slices.Equal(got, []string{"restart", "stop"}) {
		t.Fatalf("told = %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if !d.sock.Connected() {
		t.Fatal("the server closed the socket: the daemon leaves by itself")
	}
	if got := waitDaemon(t, e.server, alice, d.id, "online"); got.ID != d.id {
		t.Fatalf("still online until the socket really drops: %+v", got)
	}
	d.disconnect() // the daemon exits
	got := tab.until("the daemon is offline", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Daemons[0].State == "offline" })
	if got.Daemons[0].Sessions != 0 {
		t.Fatalf("an offline daemon runs %v sessions", got.Daemons[0].Sessions)
	}
}

func TestOnlyTheOwnerOfAnOnlineDaemonCanRestartOrStopIt(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	for _, id := range []string{"", "nope"} {
		if _, err := e.server.Restart(alice, id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
			t.Fatalf("restart %q: %v", id, err)
		}
		if _, err := e.server.Stop(alice, id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
			t.Fatalf("stop %q: %v", id, err)
		}
	}
	if _, err := e.server.Restart(bob, d.id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
		t.Fatalf("bob restarted alice's daemon: %v", err)
	}
	if _, err := e.server.Stop(bob, d.id); !errors.Is(err, goserver.ErrNoSuchDaemon) {
		t.Fatalf("bob stopped alice's daemon: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := d.told(); len(got) != 0 {
		t.Fatalf("the daemon was told %v by someone else", got)
	}
	// An offline daemon is named.
	off := e.pairHost(t, alice, "work mac")
	for name, call := range map[string]func(string, string) (goserver.List, error){"restart": e.server.Restart, "stop": e.server.Stop} {
		if _, err := call(alice, e.idOf(t, off)); err == nil || err.Error() != "work mac is offline" {
			t.Fatalf("%s an offline daemon: %v", name, err)
		}
	}
}

func TestADaemonWithoutAServiceCannotBeRestartedButCanBeStopped(t *testing.T) {
	e := newEnv(t)
	d := e.onlineWith(t, alice, "laptop", false)
	if got := e.list(t, alice).Daemons[0]; got.Managed {
		t.Fatalf("list = %+v, want managed=false as the daemon said", got)
	}
	_, err := e.server.Restart(alice, d.id)
	if err == nil || err.Error() != "laptop was started without a service and cannot be restarted; stop it or restart it on its machine" {
		t.Fatalf("restart: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := d.told(); len(got) != 0 {
		t.Fatalf("the daemon was told %v", got)
	}
	if _, err := e.server.Stop(alice, d.id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(waitFor)
	for len(d.told()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the daemon was never told to stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheListCountsASessionsPerDaemonAndPushesOnlyRealChanges(t *testing.T) {
	e := newEnv(t)
	laptop := e.onlineAs(t, alice, "laptop")
	desktop := e.onlineAs(t, alice, "desktop")
	tab := watch(t, e.server, alice)
	tab.until("the first list", func(goserver.List) bool { return true })
	sessions := func(l goserver.List) map[string]int {
		out := map[string]int{}
		for _, d := range l.Daemons {
			out[d.Name] = d.Sessions
		}
		return out
	}
	one, err := e.server.Open(context.Background(), alice, laptop.id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.server.Open(context.Background(), alice, laptop.id, "grok")
	if err != nil {
		t.Fatal(err)
	}
	tab.until("two sessions on the laptop", func(l goserver.List) bool { return sessions(l)["laptop"] == 2 })
	if got := sessions(e.list(t, alice)); got["laptop"] != 2 || got["desktop"] != 0 {
		t.Fatalf("sessions = %v", got)
	}
	firstSession, _, _ := strings.Cut(laptop.openedSessions()[0], ":")
	one.Close()
	laptop.frame(firstSession, contract.FrameKindExit, "", 0)
	tab.until("one session on the laptop", func(l goserver.List) bool { return sessions(l)["laptop"] == 1 })
	// A call that changes nothing is not pushed.
	if _, err := e.server.Rename(alice, desktop.id, "desktop"); err != nil {
		t.Fatal(err)
	}
	e.server.CancelPairing(alice)
	tab.silent(200 * time.Millisecond)
	two.Close()
	laptop.disconnect()
	tab.until("no session after the daemon left", func(l goserver.List) bool { return sessions(l)["laptop"] == 0 && l.Daemons[0].State == "offline" })
}

// A turn that is running when the daemon is told to restart or stop ends with the reason, however the daemon's agent
// reports its end.
func TestSessionsThatDieWithARestartOrAStopSayWhy(t *testing.T) {
	for what, want := range map[string]string{"restart": "the daemon was restarted", "stop": "the daemon was stopped"} {
		for _, report := range []string{"exit frame", "socket only"} {
			t.Run(what+", "+report, func(t *testing.T) {
				e := newEnv(t)
				d := e.online(t, alice)
				conn, err := e.server.Open(context.Background(), alice, d.id, "claude")
				if err != nil {
					t.Fatal(err)
				}
				sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
				d.onControl = func(string) {
					if report == "exit frame" {
						d.frame(sid, contract.FrameKindExit, "", 143)
					}
					d.disconnect()
				}
				call := e.server.Restart
				if what == "stop" {
					call = e.server.Stop
				}
				if _, err := call(alice, d.id); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadAll(conn); err != nil {
					t.Fatal(err)
				}
				select {
				case <-conn.Done():
				case <-time.After(waitFor):
					t.Fatal("the session never ended")
				}
				if got := conn.Stderr(); !strings.Contains(got, want) {
					t.Fatalf("stderr = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestARevokedTokenIsRefusedWithAnUnmistakableUnauthorized(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	if _, err := e.server.Revoke(alice, d.id); err != nil {
		t.Fatal(err)
	}
	_, revoked := dial(t, e.srv.URL, map[string]any{"role": "daemon", "token": d.token})
	_, unknown := dial(t, e.srv.URL, map[string]any{"role": "daemon", "token": "agd_nope"})
	for name, refused := range map[string]any{"revoked": revoked, "unknown": unknown} {
		if refused == nil || !strings.Contains(fmt.Sprint(refused), "unauthorized") {
			t.Fatalf("%s token: refused = %v, want an unauthorized answer a daemon can tell from a network error", name, refused)
		}
	}
}

func TestATokenLookupThatFailsIsRefusedAsUnavailableSoTheDaemonRetries(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	d.disconnect()
	auth := map[string]any{"role": "daemon", "token": d.token}
	for name, down := range map[string]*atomic.Bool{"by hash": &e.outage.byHash, "by id": &e.outage.byID} {
		down.Store(true)
		_, refused := dial(t, e.srv.URL, auth)
		down.Store(false)
		if got := fmt.Sprint(refused); !strings.Contains(got, "unavailable") || strings.Contains(got, "unauthorized") {
			t.Fatalf("lookup %s failing: refused = %v, want unavailable: the token is fine, the store is not", name, refused)
		}
	}
	if _, refused := dial(t, e.srv.URL, auth); refused != nil {
		t.Fatalf("the store is back: refused = %v, want the same token accepted", refused)
	}
}

// changes records the calls of Server.OnAgentsChanged.
type changes struct {
	mu   sync.Mutex
	list []string
}

func (c *changes) add(user, daemon string) {
	c.mu.Lock()
	c.list = append(c.list, user+"/"+daemon)
	c.mu.Unlock()
}
func (c *changes) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.list) }

func (c *changes) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for c.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("agents changed %d times, want %d", c.count(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func agentsOf(list goserver.List, id string) map[string]bool {
	out := map[string]bool{}
	if d, ok := infoOf(list, id); ok {
		for _, a := range d.Agents {
			out[a.Name] = a.Ready
		}
	}
	return out
}

func TestAnAgentThatBecomesReadyShowsAtOnceInTheListTheAgentsAndOpen(t *testing.T) {
	e := newEnv(t)
	var seen changes
	e.server.OnAgentsChanged(seen.add)
	d := e.online(t, alice) // codex is not ready: its adapter is installing
	seen.waitFor(t, 1)      // hello counts: the daemon may be a new process
	installing := []contract.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Ready: false, Reason: "installing the Codex adapter…"}}
	d.agents(installing)
	waitAgents(t, e.server, alice, d.id, func(a map[string]bool) bool { return !a["codex"] && a["claude"] && len(a) == 2 })
	seen.waitFor(t, 2)

	_, err := e.server.Open(context.Background(), alice, d.id, "codex")
	if err == nil || err.Error() != "Codex is not ready on laptop: installing the Codex adapter…" {
		t.Fatalf("Open while installing: %v, want the agent, the daemon and the reason", err)
	}
	if got := d.openedSessions(); len(got) != 0 {
		t.Fatalf("the daemon was asked to open %v: the answer comes from what it reported", got)
	}
	if ready, err := e.server.Ready(alice, d.id); err != nil || slices.Contains(ready, "codex") {
		t.Fatalf("ready = %v, %v", ready, err)
	}

	tab := watch(t, e.server, alice)
	tab.until("the first list", func(goserver.List) bool { return true })
	d.agents([]contract.AgentInfo{{Name: "claude", Ready: true}, {Name: "codex", Ready: true}})
	pushed := tab.until("codex is ready", func(l goserver.List) bool { return agentsOf(l, d.id)["codex"] })
	if len(pushed.Daemons) != 1 || pushed.Daemons[0].State != "online" {
		t.Fatalf("pushed = %+v", pushed)
	}
	seen.waitFor(t, 3)
	if ready, err := e.server.Ready(alice, d.id); err != nil || !slices.Contains(ready, "codex") {
		t.Fatalf("ready = %v, %v: the agent that just became ready is offered at once", ready, err)
	}
	conn, err := e.server.Open(context.Background(), alice, d.id, "codex")
	if err != nil {
		t.Fatalf("Open once ready: %v", err)
	}
	sid, _, _ := strings.Cut(d.openedSessions()[0], ":")
	conn.Close()
	d.frame(sid, contract.FrameKindExit, "", 0)
	tab.until("the session is gone again", func(l goserver.List) bool { return len(l.Daemons) == 1 && l.Daemons[0].Sessions == 0 })

	// The same list again changes nothing: no push, no cache drop.
	before := seen.count()
	d.agents([]contract.AgentInfo{{Name: "claude", Ready: true}, {Name: "codex", Ready: true}})
	tab.silent(300 * time.Millisecond)
	if seen.count() != before {
		t.Fatalf("an unchanged list dropped the caches again (%d calls)", seen.count())
	}
	if got := seen.list[0]; got != alice+"/"+d.id {
		t.Fatalf("callback arguments = %q", got)
	}
}

func waitAgents(t *testing.T, h *goserver.Server, user, id string, ok func(map[string]bool) bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		list, err := h.List(user)
		if err != nil {
			t.Fatal(err)
		}
		if got := agentsOf(list, id); ok(got) {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("agents = %v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOnlyAConnectionCanReportItsOwnDaemonsAgents(t *testing.T) {
	e := newEnv(t)
	var seen changes
	e.server.OnAgentsChanged(seen.add)
	mine := e.onlineAs(t, alice, "laptop")
	other := e.onlineAs(t, alice, "desktop")
	strangers := e.onlineAs(t, bob, "laptop")
	seen.waitFor(t, 3)
	before := seen.count()
	// Each daemon speaks for itself only: the id is the socket's, never a parameter.
	other.agents([]contract.AgentInfo{{Name: "claude", Ready: false, Reason: "other says so"}})
	strangers.agents([]contract.AgentInfo{{Name: "claude", Ready: false, Reason: "bob's daemon says so"}})
	waitAgents(t, e.server, alice, other.id, func(a map[string]bool) bool { return len(a) == 1 && !a["claude"] })
	seen.waitFor(t, before+2)
	if got := agentsOf(e.list(t, alice), mine.id); len(got) != 3 || !got["claude"] || !got["grok"] {
		t.Fatalf("alice's laptop agents = %v: another connection changed them", got)
	}
	if got := agentsOf(e.list(t, bob), strangers.id); len(got) != 1 {
		t.Fatalf("bob's daemon agents = %v", got)
	}
	if got := agentsOf(e.list(t, alice), other.id); len(got) != 1 {
		t.Fatalf("alice's desktop agents = %v", got)
	}
	// An MCP bridge is not a daemon.
	grant := e.tools.Open(alice, mine.id)
	_, sock, refused := bridgeSocket(t, e.srv.URL, mine.token, grant.ID)
	if refused != nil {
		t.Fatal(refused)
	}
	_ = sock.Emit("agents", []contract.AgentInfo{{Name: "claude", Ready: false, Reason: "bridge"}})
	time.Sleep(200 * time.Millisecond)
	if got := agentsOf(e.list(t, alice), mine.id); !got["claude"] {
		t.Fatalf("a bridge changed the daemon's agents: %v", got)
	}
}

func TestTheListShowsTheDaemonsAgentsInItsOrderAndAnOlderDaemonsByName(t *testing.T) {
	e := newEnv(t)
	token := e.pair(t, alice)
	d := connectDaemon(t, e.srv.URL, token)
	id := e.idOf(t, token)
	// A daemon older than 0.3 reports no title: its agents show by name.
	d.hello(contract.Hello{Version: "0.2.0", Hostname: "laptop", Os: "darwin", Arch: "arm64", Agents: []contract.AgentInfo{
		{Name: "grok", Title: "Grok", Ready: true}, {Name: "claude", Ready: true},
	}})
	waitDaemon(t, e.server, alice, id, "online")
	got := e.list(t, alice).Daemons[0].Agents
	want := []goserver.Agent{{Name: "grok", Title: "Grok", Ready: true}, {Name: "claude", Title: "claude", Ready: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("agents = %+v, want %+v", got, want)
	}
}

func TestAgentsReportedBeforeHelloAreIgnored(t *testing.T) {
	e := newEnv(t)
	token := e.pair(t, alice)
	id := e.idOf(t, token)
	d := connectDaemon(t, e.srv.URL, token)
	d.agents(allAgents())
	time.Sleep(200 * time.Millisecond)
	if got := agentsOf(e.list(t, alice), id); len(got) != 0 {
		t.Fatalf("agents = %v before hello", got)
	}
}
