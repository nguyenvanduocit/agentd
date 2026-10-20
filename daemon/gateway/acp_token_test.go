package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// The secret a program with no Origin header presents to /acp. A dev container, a sandboxed app or an `ssh -R` tunnel
// reaches 127.0.0.1 through a forwarder that runs as the user, so being the user's process is not enough; the token is
// in a file such a client cannot read.

const needsToken = "this computer's agentd needs its local token: run `agentd acp` to get the address"

func tokenRig(t *testing.T) *acpRig {
	t.Helper()
	return startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat")})
}

// bearer is the headers of a client that sends the token in the Authorization header.
func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

func TestAcpAProgramWithoutTheTokenIsRefusedWith401(t *testing.T) {
	r := tokenRig(t)
	res := r.getRaw(http.MethodGet, "/acp/claude", "", true, nil)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("status %d (%v), want 401", res.status, res.body)
	}
	if got := res.header.Get("WWW-Authenticate"); got != `Bearer realm="agentd"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if res.body["granted"] != false || res.body["reason"] != "denied" || res.body["message"] != needsToken {
		t.Errorf("body %v, want granted:false, reason:denied and the message that says where the token comes from", res.body)
	}
}

// An unauthenticated caller learns nothing: not that the endpoint has agents, not which are ready, not whether the
// path or the method is right, and it cannot take a place among the connections.
func TestAcpAProgramWithoutTheTokenLearnsNothing(t *testing.T) {
	r := tokenRig(t)
	for name, c := range map[string]struct {
		method, path string
		upgrade      bool
	}{
		"a bare /acp (it would list the ready agents)": {http.MethodGet, "/acp", true},
		"an agent that does not exist":                 {http.MethodGet, "/acp/nosuch", true},
		"an agent that is not ready":                   {http.MethodGet, "/acp/codex", true},
		"a path that is no agent path":                 {http.MethodGet, "/acp/claude/extra", true},
		"two different agents named":                   {http.MethodGet, "/acp/claude?agent=codex", true},
		"no upgrade":                                   {http.MethodGet, "/acp/claude", false},
		"the wrong method":                             {http.MethodPost, "/acp/claude", true},
	} {
		res := r.getRaw(c.method, c.path, "", c.upgrade, nil)
		if res.status != http.StatusUnauthorized || res.body["reason"] != "denied" {
			t.Errorf("%s: status %d, body %v; want the same 401 as everything else", name, res.status, res.body)
		}
		if _, listed := res.body["agents"]; listed {
			t.Errorf("%s: the answer lists agents: %v", name, res.body)
		}
	}
}

func TestAcpWhatIsPresentedIsNeverEchoed(t *testing.T) {
	r := tokenRig(t)
	wrong := strings.Repeat("Z", acpauth.TokenLen)
	for _, extra := range []map[string]string{{"Authorization": "Bearer " + wrong}} {
		res := r.getRaw(http.MethodGet, "/acp/claude?token="+wrong, "", true, extra)
		raw := res.body
		for k, v := range raw {
			if s, ok := v.(string); ok && strings.Contains(s, wrong) {
				t.Errorf("the answer echoes what was presented in %q: %q", k, s)
			}
		}
		for k, v := range res.header {
			if strings.Contains(strings.Join(v, ","), wrong) {
				t.Errorf("a header echoes what was presented: %s: %v", k, v)
			}
		}
	}
}

func TestAcpATokenThatIsNotTheTokenIsRefused(t *testing.T) {
	r := tokenRig(t)
	tok := r.token()
	other := acpauth.New()
	for name, c := range map[string]struct {
		path   string
		header http.Header
	}{
		"another token in the header":        {"/acp/claude", bearer(other)},
		"another token in the query":         {"/acp/claude?token=" + other, nil},
		"a truncated token":                  {"/acp/claude", bearer(tok[:acpauth.TokenLen-1])},
		"a longer value that begins with it": {"/acp/claude", bearer(tok + "x")},
		"a space before it in the header":    {"/acp/claude", http.Header{"Authorization": {"Bearer  " + tok}}},
		"a space after it in the query":      {"/acp/claude?token=" + tok + "%20", nil},
		"a space before it in the query":     {"/acp/claude?token=%20" + tok, nil},
		"an empty token in the query":        {"/acp/claude?token=", nil},
		"an empty bearer token":              {"/acp/claude", http.Header{"Authorization": {"Bearer "}}},
		"no scheme":                          {"/acp/claude", http.Header{"Authorization": {tok}}},
		"the wrong scheme":                   {"/acp/claude", http.Header{"Authorization": {"Basic " + tok}}},
		"the token in another parameter":     {"/acp/claude?access_token=" + tok, nil},
		"the token in the path":              {"/acp/claude/" + tok, nil},
		"the token in a cookie":              {"/acp/claude", http.Header{"Cookie": {"token=" + tok}}},
		"the token as the agent":             {"/acp?agent=" + tok, nil},
	} {
		_, res, err := r.dialRaw(c.path, "", c.header)
		if err == nil {
			t.Errorf("%s: the upgrade succeeded", name)
			continue
		}
		if res == nil || res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %v (response %v), want a 401", name, err, res)
		}
	}
}

func TestAcpTheTokenInTheHeaderOrInTheQueryLetsAProgramIn(t *testing.T) {
	r := tokenRig(t)
	tok := r.token()
	for name, c := range map[string]struct {
		path   string
		header http.Header
	}{
		"the header":                              {"/acp/claude", bearer(tok)},
		"the scheme in capitals":                  {"/acp/claude", http.Header{"Authorization": {"BEARER " + tok}}},
		"the scheme in odd case":                  {"/acp/claude", http.Header{"Authorization": {"bEaReR " + tok}}},
		"the query":                               {"/acp/claude?token=" + tok, nil},
		"the query with the agent in the query":   {"/acp?agent=claude&token=" + tok, nil},
		"the header right and the query wrong":    {"/acp/claude?token=" + acpauth.New(), bearer(tok)},
		"the header right and the query repeated": {"/acp/claude?token=a&token=b", bearer(tok)},
	} {
		c := c
		t.Run(name, func(t *testing.T) {
			ws, res, err := r.dialRaw(c.path, "", c.header)
			if err != nil {
				t.Fatalf("%v (response %v)", err, res)
			}
			if res.StatusCode != http.StatusSwitchingProtocols || ws.id == "" {
				t.Fatalf("status %d, connection id %q", res.StatusCode, ws.id)
			}
		})
	}
}

// If both are given the header is the credential: a wrong header is not rescued by a right query, and a header that is
// not a Bearer one is not ignored in favour of the query.
func TestAcpTheHeaderWinsOverTheQuery(t *testing.T) {
	r := tokenRig(t)
	tok := r.token()
	for name, c := range map[string]struct {
		path   string
		header http.Header
	}{
		"the header wrong and the query right":      {"/acp/claude?token=" + tok, bearer(acpauth.New())},
		"a Basic header and the query right":        {"/acp/claude?token=" + tok, http.Header{"Authorization": {"Basic " + tok}}},
		"two Authorization headers, one of them OK": {"/acp/claude", http.Header{"Authorization": {"Bearer " + acpauth.New(), "Bearer " + tok}}},
		"the query repeated, one of them right":     {"/acp/claude?token=" + acpauth.New() + "&token=" + tok, nil},
		// The right value first: a reader that takes the first of several would let these in.
		"two Authorization headers, the right one first": {"/acp/claude", http.Header{"Authorization": {"Bearer " + tok, "Bearer " + acpauth.New()}}},
		"two Authorization headers, both right":          {"/acp/claude", http.Header{"Authorization": {"Bearer " + tok, "Bearer " + tok}}},
		"the query repeated, the right one first":        {"/acp/claude?token=" + tok + "&token=" + acpauth.New(), nil},
		"the query repeated, both right":                 {"/acp/claude?token=" + tok + "&token=" + tok, nil},
	} {
		_, res, err := r.dialRaw(c.path, "", c.header)
		if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %v (response %v), want a 401", name, err, res)
		}
	}
}

// The token turns a program in; it does not turn a web page into one. An Origin keeps the web rules, whatever else is
// sent, and a page with a grant never needs the token.
func TestAcpTheTokenDoesNotChangeTheRulesOfAWebPage(t *testing.T) {
	r := tokenRig(t)
	tok := r.token()
	if _, res, err := r.dialRaw("/acp/claude?token="+tok, other, bearer(tok)); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("a page with no grant but the token: %v (response %v), want 403", err, res)
	} else if r := readReply(t, res); r["reason"] != "denied" {
		t.Errorf("body %v, want reason denied", r)
	}
	if ws, res, err := r.dialRaw("/acp/claude", page, nil); err != nil {
		t.Fatalf("a page with a grant and no token: %v (response %v)", err, res)
	} else if ws.id == "" {
		t.Error("no connection id")
	}
}

func TestAcpRefusedAttemptsDoNotTakeAPlaceAmongTheConnections(t *testing.T) {
	r := tokenRig(t)
	for i := 0; i < 12; i++ {
		if res := r.getRaw(http.MethodGet, "/acp/claude", "", true, nil); res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, res.status)
		}
		if _, _, err := r.dialRaw("/acp/claude?token="+acpauth.New(), "", nil); err == nil {
			t.Fatalf("attempt %d with a wrong token got in", i)
		}
	}
	var held []*acpConn
	for i := 0; i < MaxAcpConnections; i++ {
		held = append(held, r.mustDial("/acp/claude", ""))
	}
	if _, res, err := r.dialRaw("/acp/claude", "", bearer(r.token())); err == nil || res == nil || res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the ninth connection: %v (response %v), want 429: the limit is the connections, not the attempts", err, res)
	}
	_ = held
}

func TestAcpANewTokenWorksAtOnceAndTheOldOneStopsWithoutCuttingAConnection(t *testing.T) {
	r := tokenRig(t)
	old := r.token()
	open := r.mustDial("/acp/claude", "")
	open.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if got := open.next(); !strings.Contains(got, `"method":"ping"`) {
		t.Fatalf("the open connection answered %q", got)
	}

	fresh, err := acpauth.Rotate(r.home)
	if err != nil || fresh == old {
		t.Fatalf("Rotate() = %q, %v", fresh, err)
	}
	if _, res, err := r.dialRaw("/acp/claude", "", bearer(old)); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the old token after a rotation: %v (response %v), want 401", err, res)
	}
	if _, res, err := r.dialRaw("/acp/claude?token="+fresh, "", nil); err != nil {
		t.Fatalf("the new token: %v (response %v)", err, res)
	}
	open.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if got := open.next(); !strings.Contains(got, `"id":2`) {
		t.Fatalf("the connection open before the rotation answered %q", got)
	}
}

// With no readable token, no program gets in: the endpoint fails closed, and never lets the empty string match.
func TestAcpWithoutAReadableTokenNoProgramGetsIn(t *testing.T) {
	r := tokenRig(t)
	tok := r.token()
	if err := os.Remove(acpauth.TokenFile(r.home)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		path   string
		header http.Header
	}{
		"the old token":   {"/acp/claude", bearer(tok)},
		"an empty bearer": {"/acp/claude", http.Header{"Authorization": {"Bearer "}}},
		"an empty query":  {"/acp/claude?token=", nil},
		"nothing":         {"/acp/claude", nil},
	} {
		if _, res, err := r.dialRaw(c.path, "", c.header); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with the token file gone: %v (response %v), want 401", name, err, res)
		}
	}
	// A web page with a grant is not affected.
	if _, res, err := r.dialRaw("/acp/claude", page, nil); err != nil {
		t.Errorf("a page with a grant, token file gone: %v (response %v)", err, res)
	}
}

func TestAcpAGatewayWithNoTokenSourceRefusesEveryProgram(t *testing.T) {
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat"), noTokenSource: true})
	if _, res, err := r.dialRaw("/acp/claude", "", bearer(acpauth.New())); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%v (response %v), want 401", err, res)
	}
}

// A refusal is told to the log in one plain line, at most once in ten seconds, and the line holds nothing a client sent.
func TestAcpARefusedProgramIsLoggedOnceAWhileWithoutAnythingItSent(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat"), now: clock})
	sent := acpauth.New()

	for i := 0; i < 5; i++ {
		r.getRaw(http.MethodGet, "/acp/claude?token="+sent+"&x=1", "", true, map[string]string{"Authorization": "Bearer " + sent})
	}
	const line = "acp connection refused: a local program without the token"
	said := r.said.all()
	if len(said) != 1 || said[0] != line {
		t.Fatalf("after five refusals the log got %q, want exactly %q", said, line)
	}
	advance(11 * time.Second)
	r.getRaw(http.MethodGet, "/acp/claude", "", true, nil)
	said = r.said.all()
	if len(said) != 2 || !strings.HasPrefix(said[1], line) || !strings.Contains(said[1], "4 more since the last line, 11 s ago") {
		t.Fatalf("after the window the log got %q, want a second line that says 4 more were held back", said)
	}
	for _, l := range said {
		if strings.Contains(l, sent) || strings.Contains(l, "token=") || strings.Contains(l, "/acp") || strings.Contains(l, "Bearer") {
			t.Errorf("a log line holds what the client sent: %q", l)
		}
	}
}

// The log of a connection that came in with the token in its address holds neither the token nor the query.
func TestAcpTheLogOfAConnectionNeverHoldsTheTokenOrTheQuery(t *testing.T) {
	var log lockedBuffer
	r := startAcpRig(t, acpRigOptions{agent: agentScript(t, "cat"), tune: func(o *AcpOptions) { o.Fleet.Log = proc.NewLogger(&log, &log) }})
	tok := r.token()
	ws, _, err := r.dialRaw("/acp/claude?token="+tok, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.startReading()
	ws.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	ws.next()
	waitFor(t, "the connection to be logged", acpWait, func() bool { return strings.Contains(log.String(), "acp connection ") })
	if text := log.String(); strings.Contains(text, tok) || strings.Contains(text, "token=") || strings.Contains(text, "?") {
		t.Errorf("the log holds the token or the query:\n%s", text)
	}
	for _, l := range r.said.all() {
		if strings.Contains(l, tok) {
			t.Errorf("the gateway told its log the token: %q", l)
		}
	}
}

type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// readReply is the JSON body of a response the library handed back with its error.
func readReply(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", data, err)
	}
	return body
}

// The daemon reads the token the way the rig does now, with Ensure: a token file that was deleted is made again at the
// next request, the old token stops working, and the new one does.
func TestAcpADeletedTokenFileIsMadeAgainAndTheOldTokenStopsWorking(t *testing.T) {
	r := tokenRig(t)
	old := r.token()
	if err := os.Remove(acpauth.TokenFile(r.home)); err != nil {
		t.Fatal(err)
	}
	if _, res, err := r.dialRaw("/acp/claude?token="+old, "", nil); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the old token after the file was deleted: %v (response %v), want 401", err, res)
	}
	fresh := r.token()
	if fresh == old || !acpauth.Valid(fresh) {
		t.Fatalf("the token after the file was deleted is %q, want a new valid one other than the old", fresh)
	}
	if ws, res, err := r.dialRaw("/acp/claude?token="+fresh, "", nil); err != nil {
		t.Fatalf("the new token: %v (response %v)", err, res)
	} else if ws.id == "" {
		t.Error("no connection id")
	}
}

// The log line says how many refusals it stands for and since when: everything since the line before, not "in the last
// ten seconds".
func TestTheRefusalLineSaysHowManyAndSinceWhen(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var said []string
	log := newRefusalLog(func(l string) { said = append(said, l) }, func() time.Time { return now }, 10*time.Second)
	log.refused("refused")
	for i := 0; i < 5; i++ {
		now = now.Add(time.Second)
		log.refused("refused")
	}
	now = now.Add(37 * time.Second)
	log.refused("refused")
	want := []string{"refused", "refused (5 more since the last line, 42 s ago)"}
	if len(said) != 2 || said[0] != want[0] || said[1] != want[1] {
		t.Fatalf("the log got %q, want %q", said, want)
	}
}
