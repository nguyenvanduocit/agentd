package gateway

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
)

const acpWait = 10 * time.Second

// acpRigOptions shape a gateway whose /acp endpoint runs a fake "claude".
type acpRigOptions struct {
	agent   string                // the command line of the claude agent
	folders []string              // the folders the page's grant has; none is any folder
	now     func() time.Time      // the gateway's clock; nil is time.Now
	owners  consent.PeerOwners    // who owns the connections; nil is this process's user
	tune    func(o *AcpOptions)   // changes the sessions' options
	agents  []pagetools.AgentInfo // the agents the gateway reports; nil is the world's default
	// noTokenSource leaves the gateway with no source of the token at all (Deps.AcpToken is nil).
	noTokenSource bool
}

// acpRig is a gateway with /acp over real sessions, and the page origin `page` granted.
type acpRig struct {
	t     *testing.T
	port  int
	gw    *Gateway
	acp   *AcpSessions
	world *world
	dir   string
	home  string         // agentd's home in the test: where the token file is
	said  *capturedLines // what the gateway told its log
}

// capturedLines is a log destination a test reads while the gateway writes to it.
type capturedLines struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLines) add(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

func (c *capturedLines) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func startAcpRig(t *testing.T, o acpRigOptions) *acpRig {
	t.Helper()
	dir := shortDir(t)
	binDir := filepath.Join(dir, "bin", "page")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := home.Env{"PATH": os.Getenv("PATH"), agents.SeamVariable("claude"): o.agent}
	w := newWorld()
	w.now = o.now
	w.reg = w.reg.AddBrowser(page, w.clock(), o.folders)
	if o.agents != nil {
		w.agents = o.agents
	}
	ao := AcpOptions{
		Fleet: agents.FleetOptions{Cwd: dir, BinDir: binDir, Env: env, Adapter: noAdapter, State: noState{}},
		Folders: func(origin string) ([]string, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.reg.BrowserFolders(origin), nil
		},
	}
	if o.tune != nil {
		o.tune(&ao)
	}
	acp := NewAcpSessions(ao)
	deps := w.deps()
	deps.Acp = acp.Serve
	revoke := deps.Revoke
	deps.Revoke = func(origin string) error {
		err := revoke(origin)
		acp.EndOrigin(origin)
		return err
	}
	owners := o.owners
	if owners == nil {
		owners = ownOwners
	}
	authHome := filepath.Join(dir, "agentd-home")
	if _, err := acpauth.Ensure(authHome); err != nil {
		t.Fatal(err)
	}
	// The token is read from its file at each request, and made again when it is gone, as the daemon does (acpToken in
	// daemon/run), so a rotation takes effect at once.
	deps.AcpToken = func() string {
		token, err := acpauth.Ensure(authHome)
		if err != nil {
			return ""
		}
		return token
	}
	if o.noTokenSource {
		deps.AcpToken = nil
	}
	said := &capturedLines{}
	g, err := Start(deps, 0, Options{Gate: gateFor(owners), Now: o.now, Say: said.add})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	t.Cleanup(func() { acp.Shutdown(3 * time.Second) })
	return &acpRig{t: t, port: g.Port, gw: g, acp: acp, world: w, dir: dir, home: authHome, said: said}
}

// token is the token in the file now.
func (r *acpRig) token() string {
	r.t.Helper()
	token, err := acpauth.Load(r.home)
	if err != nil {
		r.t.Fatal(err)
	}
	return token
}

// acpAgentScript is a fake ACP agent: it appends every line it reads to `rec`, writes its pid to `pidFile`, answers
// `initialize` with the id it was sent (an error for a second one), and says every other line back unchanged.
func acpAgentScript(t *testing.T, rec, pidFile string) string {
	t.Helper()
	return agentScript(t, fmt.Sprintf(`echo $$ > %s
n=0
while IFS= read -r line; do
  printf '%%s\n' "$line" >> %s
  id=$(printf '%%s' "$line" | sed -nE 's/.*"id": *("[^"]*"|[0-9]+).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      n=$((n+1))
      if [ "$n" -gt 1 ]; then
        printf '{"jsonrpc":"2.0","id":%%s,"error":{"code":-32600,"message":"already initialized"}}\n' "$id"
      else
        printf '{"jsonrpc":"2.0","id":%%s,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}\n' "$id"
      fi;;
    *) printf '%%s\n' "$line";;
  esac
done`, pidFile, rec))
}

// recorded is the lines the fake agent read, one per element.
func recorded(t *testing.T, rec string) []string {
	t.Helper()
	data, err := os.ReadFile(rec)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func acpPid(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	waitFor(t, "the agent's pid", acpWait, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	})
	return pid
}

// acpConn is a client of /acp in a test. Unless it was dialled deaf, a goroutine reads it, so that the library answers
// the daemon's pings as any client does, and what the daemon sends waits in a channel.
type acpConn struct {
	t   *testing.T
	ws  *websocket.Conn
	id  string // the Acp-Connection-Id of the handshake
	in  chan string
	end chan acpEnd // the end of the socket, once
}

type acpEnd struct {
	code   int // -1 for an end without a close frame
	reason string
}

// dial opens /acp<path> with the Origin header when origin is not empty; the connection is read in the background.
func (r *acpRig) dial(path, origin string) (*acpConn, *http.Response, error) {
	c, res, err := r.dialDeaf(path, origin)
	if err == nil {
		c.startReading()
	}
	return c, res, err
}

// dialDeaf is dial for a client that does not read (the test, or a library, reads the socket itself).
func (r *acpRig) dialDeaf(path, origin string) (*acpConn, *http.Response, error) {
	r.t.Helper()
	header := http.Header{}
	if origin == "" { // a program on this computer carries the token, as every test of the endpoint's behaviour assumes
		header.Set("Authorization", "Bearer "+r.token())
	}
	return r.dialRaw(path, origin, header)
}

// dialRaw dials with exactly these headers (and the Origin when origin is not empty): nothing is added, so a test can
// leave the token out or change it.
func (r *acpRig) dialRaw(path, origin string, header http.Header) (*acpConn, *http.Response, error) {
	r.t.Helper()
	if header == nil {
		header = http.Header{}
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	d := websocket.Dialer{HandshakeTimeout: acpWait}
	ws, res, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(r.port)+path, header)
	if err != nil {
		return nil, res, err
	}
	c := &acpConn{t: r.t, ws: ws, in: make(chan string, 4096), end: make(chan acpEnd, 1)}
	if res != nil {
		c.id = res.Header.Get("Acp-Connection-Id")
	}
	r.t.Cleanup(func() { _ = ws.Close() })
	return c, res, nil
}

func (c *acpConn) startReading() {
	go func() {
		for {
			kind, data, err := c.ws.ReadMessage()
			if err != nil {
				end := acpEnd{-1, err.Error()}
				if ce, ok := err.(*websocket.CloseError); ok {
					end = acpEnd{ce.Code, ce.Text}
				}
				c.end <- end
				return
			}
			if kind != websocket.TextMessage {
				c.in <- fmt.Sprintf("<message of type %d>", kind)
				continue
			}
			if !utf8.Valid(data) {
				c.in <- fmt.Sprintf("<not UTF-8: %q>", data)
				continue
			}
			c.in <- string(data)
		}
	}()
}

func (r *acpRig) mustDial(path, origin string) *acpConn {
	r.t.Helper()
	c, res, err := r.dial(path, origin)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		r.t.Fatalf("dial %s as %q: %v (status %d)", path, origin, err, status)
	}
	return c
}

func (r *acpRig) mustDialDeaf(path, origin string) *acpConn {
	r.t.Helper()
	c, res, err := r.dialDeaf(path, origin)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		r.t.Fatalf("dial %s as %q: %v (status %d)", path, origin, err, status)
	}
	return c
}

func (c *acpConn) send(text string) {
	c.t.Helper()
	if err := c.ws.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		c.t.Fatalf("send %q: %v", text, err)
	}
}

// next is the next text message; the test fails when none comes.
func (c *acpConn) next() string {
	c.t.Helper()
	select {
	case m := <-c.in:
		return m
	case end := <-c.end:
		c.t.Fatalf("the socket ended (%d %q) while a message was expected", end.code, end.reason)
	case <-time.After(acpWait):
		c.t.Fatal("timed out waiting for a message")
	}
	return ""
}

// silence fails when anything arrives, or the socket ends, within d.
func (c *acpConn) silence(d time.Duration) {
	c.t.Helper()
	select {
	case m := <-c.in:
		c.t.Fatalf("unexpected message %q", m)
	case end := <-c.end:
		c.t.Fatalf("the socket ended instead of staying quiet: %d %q", end.code, end.reason)
	case <-time.After(d):
	}
}

// closed waits for the end of the socket and returns the close code and reason (-1 for an end without a close frame).
func (c *acpConn) closed() (int, string) {
	c.t.Helper()
	timeout := time.After(acpWait)
	for {
		select {
		case <-c.in:
		case end := <-c.end:
			return end.code, end.reason
		case <-timeout:
			c.t.Fatal("timed out waiting for the socket to end")
		}
	}
}

// expectClose fails unless the socket closes with code and a reason that contains part.
func (c *acpConn) expectClose(code int, part string) {
	c.t.Helper()
	got, reason := c.closed()
	if got != code || !strings.Contains(reason, part) {
		c.t.Fatalf("closed with %d %q, want %d containing %q", got, reason, code, part)
	}
}

// get is a plain request with the headers of a WebSocket upgrade (when upgrade is true), as call() makes it.
func (r *acpRig) get(method, path, origin string, upgrade bool) reply {
	r.t.Helper()
	extra := map[string]string{}
	if origin == "" { // a program on this computer carries the token
		extra["Authorization"] = "Bearer " + r.token()
	}
	return r.getRaw(method, path, origin, upgrade, extra)
}

// getRaw is get with exactly these extra headers: the token is not added.
func (r *acpRig) getRaw(method, path, origin string, upgrade bool, extra map[string]string) reply {
	r.t.Helper()
	header := map[string]string{}
	if upgrade {
		header = map[string]string{
			"Connection":            "Upgrade",
			"Upgrade":               "websocket",
			"Sec-WebSocket-Version": "13",
			"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
		}
	}
	for k, v := range extra {
		header[k] = v
	}
	return call(r.t, r.port, method, path, callOpts{origin: origin, header: header})
}

func groupGone(pid int) bool { return syscall.Kill(-pid, 0) != nil }

func waitGone(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("agent %d to be gone", pid), acpWait, func() bool { return groupGone(pid) })
}
