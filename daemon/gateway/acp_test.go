package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const initializeLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`
const initializedLine = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}`

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newAcpRig is a rig over the fake agent that records what it reads.
func newAcpRig(t *testing.T, o acpRigOptions) (r *acpRig, rec, pidFile string) {
	t.Helper()
	dir := shortDir(t)
	rec, pidFile = filepath.Join(dir, "rec"), filepath.Join(dir, "pid")
	if o.agent == "" {
		o.agent = acpAgentScript(t, rec, pidFile)
	}
	return startAcpRig(t, o), rec, pidFile
}

func TestAcpALocalProgramConnectsWithoutAnOriginAndGetsAConnectionId(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})
	a := r.mustDial("/acp/claude", "")
	b := r.mustDial("/acp/claude", "")
	if !uuidV4.MatchString(a.id) || !uuidV4.MatchString(b.id) || a.id == b.id {
		t.Fatalf("Acp-Connection-Id = %q and %q, want two different uuids", a.id, b.id)
	}
	a.send(initializeLine)
	if got := a.next(); got != initializedLine {
		t.Fatalf("initialize answered %q", got)
	}
}

func TestAcpAWebPageNeedsALiveGrant(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r, _, _ := newAcpRig(t, acpRigOptions{now: clock})

	r.mustDial("/acp/claude", page)

	for _, path := range []string{"/acp/claude", "/acp", "/acp/nosuch"} {
		got := r.get(http.MethodGet, path, other, true)
		wantReply(t, got, http.StatusForbidden, map[string]any{"granted": false, "reason": "denied"})
		if msg, _ := got.body["message"].(string); !strings.Contains(msg, "ask first") {
			t.Errorf("%s: message %q does not tell the site to ask first", path, msg)
		}
		if _, leaked := got.body["agents"]; leaked {
			t.Errorf("%s: a site with no grant was told which agents are ready: %v", path, got.body)
		}
	}

	mu.Lock()
	now = now.Add(registry.BrowserGrantTTL + time.Hour)
	mu.Unlock()
	_, res, err := r.dial("/acp/claude", page)
	if err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("a lapsed grant connected: err %v, response %v", err, res)
	}
	wantReply(t, r.get(http.MethodGet, "/acp/claude", page, true), http.StatusForbidden, map[string]any{"reason": "denied"})
}

func TestAcpOriginAndHostAreCheckedAsEverywhereElse(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})
	for _, origin := range []string{"null", "ftp://example.com", "http://example.com", page + "/", "HTTP://LOCALHOST:5173", "https://example.com/path"} {
		wantReply(t, r.get(http.MethodGet, "/acp/claude", origin, true), http.StatusForbidden, map[string]any{"granted": false, "reason": "origin"})
	}
	got := call(t, r.port, http.MethodGet, "/acp/claude", callOpts{host: "rebound.example.com:" + strconv.Itoa(r.port), header: map[string]string{
		"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
	}})
	wantReply(t, got, http.StatusMisdirectedRequest, map[string]any{"reason": "host"})

	// An Origin header that appears twice is not what a browser sends.
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(r.port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "GET /acp/claude HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nOrigin: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", r.port, page, page)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != http.StatusForbidden || body["reason"] != "origin" {
		t.Fatalf("a repeated Origin header: %d %v", res.StatusCode, body)
	}
}

func TestAcpAnotherOSUserGetsNothing(t *testing.T) {
	foreign := func(consent.Connection) ([]int, error) { return []int{osUID() + 1}, nil }
	r, _, _ := newAcpRig(t, acpRigOptions{owners: foreign})
	wantReply(t, r.get(http.MethodGet, "/acp/claude", "", false), http.StatusForbidden, map[string]any{"reason": "peer"})
	if _, _, err := r.dial("/acp/claude", ""); err == nil {
		t.Fatal("another OS user's program connected to /acp")
	}
}

func TestAcpRoutesAndTheirRefusals(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{})

	for _, path := range []string{"/acp", "/acp/"} {
		got := r.get(http.MethodGet, path, "", true)
		wantReply(t, got, http.StatusBadRequest, map[string]any{"granted": false, "reason": "malformed"})
		if msg, _ := got.body["message"].(string); !strings.Contains(msg, "say which agent: /acp/claude, /acp/codex or /acp/grok") {
			t.Errorf("%s: message %q", path, msg)
		}
		if fmt.Sprint(got.body["agents"]) != "[claude]" {
			t.Errorf("%s: agents = %v, want the ready ones: [claude]", path, got.body["agents"])
		}
	}
	r.mustDial("/acp?agent=claude", "")
	r.mustDial("/acp/claude?agent=claude", "")
	wantReply(t, r.get(http.MethodGet, "/acp/claude?agent=codex", "", true), http.StatusBadRequest, map[string]any{"reason": "malformed"})
	wantReply(t, r.get(http.MethodGet, "/acp?agent=claude&agent=codex", "", true), http.StatusBadRequest, map[string]any{"reason": "malformed"})

	for _, path := range []string{"/acp/nosuch", "/acp/claude/extra", "/acp/CLAUDE", "/acp/cla%75de", "/acp/../v1/status"} {
		got := r.get(http.MethodGet, path, "", true)
		if got.status != http.StatusNotFound && got.status != http.StatusMovedPermanently {
			t.Errorf("%s: status %d (%v), want 404", path, got.status, got.body)
		}
	}

	got := r.get(http.MethodGet, "/acp/codex", "", true)
	wantReply(t, got, http.StatusServiceUnavailable, map[string]any{"reason": "error"})
	if msg, _ := got.body["message"].(string); !strings.Contains(msg, "not installed") {
		t.Errorf("codex is not ready: message %q should carry the agent's own reason", msg)
	}

	plain := r.get(http.MethodGet, "/acp/claude", "", false)
	wantReply(t, plain, http.StatusUpgradeRequired, map[string]any{"reason": "malformed"})
	if plain.header.Get("Upgrade") != "websocket" {
		t.Errorf("a 426 must name the protocol: Upgrade = %q", plain.header.Get("Upgrade"))
	}
	post := r.get(http.MethodPost, "/acp/claude", "", true)
	wantReply(t, post, http.StatusMethodNotAllowed, map[string]any{"reason": "malformed"})
	if post.header.Get("Allow") != "GET" {
		t.Errorf("Allow = %q, want GET", post.header.Get("Allow"))
	}
}

func TestAcpAGatewayWithoutTheEndpointSaysSo(t *testing.T) {
	w := newWorld()
	g := startGateway(t, w.deps(), nil)
	wantReply(t, call(t, g.Port, http.MethodGet, "/acp/claude", callOpts{}), http.StatusNotFound, map[string]any{"reason": "not-found"})
}

func TestAcpFraming(t *testing.T) {
	t.Run("a binary frame is ignored and an empty one too", func(t *testing.T) {
		r, rec, _ := newAcpRig(t, acpRigOptions{})
		c := r.mustDial("/acp/claude", "")
		if err := c.ws.WriteMessage(websocket.BinaryMessage, []byte(`{"binary":1}`)); err != nil {
			t.Fatal(err)
		}
		c.send("")
		c.send(`{"text":1}`)
		if got := c.next(); got != `{"text":1}` {
			t.Fatalf("first message back = %q", got)
		}
		for _, line := range recorded(t, rec) {
			if strings.Contains(line, "binary") {
				t.Fatalf("the agent read a binary frame: %q", line)
			}
		}
	})
	t.Run("one trailing newline is tolerated and never sent back", func(t *testing.T) {
		r, _, _ := newAcpRig(t, acpRigOptions{})
		c := r.mustDial("/acp/claude", "")
		c.send("{\"a\":1}\n")
		c.send("{\"b\":2}\r\n")
		if a, b := c.next(), c.next(); a != `{"a":1}` || b != `{"b":2}` {
			t.Fatalf("got %q and %q", a, b)
		}
	})
	t.Run("a newline or a carriage return inside a message ends the connection before the agent reads anything", func(t *testing.T) {
		for _, text := range []string{"{\"a\":1}\n{\"smuggled\":2}", "{\"a\":\r1,\"smuggled\":2}", "\n{\"smuggled\":2}"} {
			r, rec, _ := newAcpRig(t, acpRigOptions{})
			c := r.mustDial("/acp/claude", "")
			c.send(text)
			c.expectClose(1007, "one line")
			waitFor(t, "the agent to be gone", acpWait, func() bool { return r.acp.Size() == 0 })
			for _, line := range recorded(t, rec) {
				if strings.Contains(line, "smuggled") {
					t.Fatalf("%q: the agent read %q", text, line)
				}
			}
		}
	})
	t.Run("a message that is not UTF-8 ends the connection", func(t *testing.T) {
		r, _, _ := newAcpRig(t, acpRigOptions{})
		c := r.mustDial("/acp/claude", "")
		if err := c.ws.WriteMessage(websocket.TextMessage, []byte{'{', 0xff, 0xfe, '}'}); err != nil {
			t.Fatal(err)
		}
		c.expectClose(1007, "UTF-8")
	})
	t.Run("a message past the limit ends the connection with 1009", func(t *testing.T) {
		r, _, _ := newAcpRig(t, acpRigOptions{tune: func(o *AcpOptions) { o.MaxMessageBytes = 1024 }})
		c := r.mustDial("/acp/claude", "")
		c.send(`{"big":"` + strings.Repeat("x", 4096) + `"}`)
		c.expectClose(1009, "")
	})
	t.Run("each line the agent writes is one text frame without a newline; blank lines and stderr are not sent", func(t *testing.T) {
		agent := agentScript(t, `printf 'a\r\nb\n\nc\n'; echo oops >&2; sleep 0.2; printf '{"last":1}\n'; exit 3`)
		r, _, _ := newAcpRig(t, acpRigOptions{agent: agent})
		c := r.mustDial("/acp/claude", "")
		for _, want := range []string{"a", "b", "c", `{"last":1}`} {
			if got := c.next(); got != want {
				t.Fatalf("frame = %q, want %q", got, want)
			}
		}
		c.expectClose(1000, "agent exited with code 3")
	})
	t.Run("the close reason of a dead agent carries the last thing it said on stderr", func(t *testing.T) {
		agent := agentScript(t, `echo 'fatal: no credentials' >&2; exit 1`)
		r, _, _ := newAcpRig(t, acpRigOptions{agent: agent})
		c := r.mustDial("/acp/claude", "")
		c.expectClose(1000, "agent exited with code 1: fatal: no credentials")
	})
}

func TestAcpAWebPageIsHeldToThePageRules(t *testing.T) {
	work := shortDir(t)
	r, rec, _ := newAcpRig(t, acpRigOptions{folders: []string{work}})
	c := r.mustDial("/acp/claude", page)

	refused := func(line string, id int, part string) {
		t.Helper()
		c.send(line)
		var got struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(c.next()), &got); err != nil || got.ID != id || got.Error.Code != -32602 || !strings.Contains(got.Error.Message, part) {
			t.Fatalf("%s: answer %+v (%v), want -32602 for id %d mentioning %q", line, got, err, id, part)
		}
	}
	refused(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"session/new","params":{"cwd":%q,"mcpServers":[{"name":"x","command":"/bin/sh","args":[],"env":[]}]}}`, work), 7, "mcpServers")
	refused(fmt.Sprintf(`{"jsonrpc":"2.0","id":8,"method":"session/new","params":{"cwd":%q,"mcpServers":[],"_meta":{"claudeCode":{"options":{"x":1}}}}}`, work), 8, "_meta")
	refused(`{"jsonrpc":"2.0","id":9,"method":"session/new","params":{"cwd":"/etc","mcpServers":[]}}`, 9, "outside the folders")

	ok := fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"session/new","params":{"cwd":%q,"mcpServers":[]}}`, work)
	c.send(ok)
	if got := c.next(); got != ok {
		t.Fatalf("a request inside the grant came back as %q", got)
	}
	for _, line := range recorded(t, rec) {
		if strings.Contains(line, "/bin/sh") || strings.Contains(line, "_meta") || strings.Contains(line, `"cwd":"/etc"`) {
			t.Fatalf("the agent read a request that was refused: %q", line)
		}
	}

	c.send("this is not JSON-RPC")
	c.expectClose(1008, "not passed to the agent")
}

func TestAcpALocalProgramIsNotHeldToThePageRules(t *testing.T) {
	r, rec, _ := newAcpRig(t, acpRigOptions{folders: []string{"/only/here"}})
	c := r.mustDial("/acp/claude", "")
	line := `{"jsonrpc":"2.0","id":3,"method":"session/new","params":{"cwd":"/etc","mcpServers":[{"name":"x","command":"/bin/sh","args":["-c","true"],"env":[]}],"_meta":{"anything":true}}}`
	c.send(line)
	if got := c.next(); got != line {
		t.Fatalf("a local program's request was changed: %q", got)
	}
	if lines := recorded(t, rec); len(lines) != 1 || lines[0] != line {
		t.Fatalf("the agent read %v", lines)
	}
}

func TestAcpTheGatewayReportsWhatIsReady(t *testing.T) {
	r, _, _ := newAcpRig(t, acpRigOptions{agents: []pagetools.AgentInfo{{Name: "claude", Ready: true}, {Name: "grok", Ready: true}, {Name: "codex", Reason: "Codex is setting up (first run only, a few minutes)."}}})
	got := r.get(http.MethodGet, "/acp", "", true)
	if fmt.Sprint(got.body["agents"]) != "[claude grok]" {
		t.Fatalf("agents = %v", got.body["agents"])
	}
	wantReply(t, r.get(http.MethodGet, "/acp/codex", "", true), http.StatusServiceUnavailable, map[string]any{"message": "Codex is setting up (first run only, a few minutes)."})
}

// acpPidFileIn is the path an agent writes its pid to.
func acpPidFileIn(dir string) string { return filepath.Join(dir, "pid") }
