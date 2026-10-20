package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const (
	page  = "http://localhost:5173"
	other = "http://localhost:5174"
)

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ownOwners says every connection comes from this process's OS user.
func ownOwners(consent.Connection) ([]int, error) { return []int{os.Getuid()}, nil }

func gateFor(owners consent.PeerOwners) *consent.PeerGate {
	return consent.NewPeerGate(owners, os.Getuid(), func(string) {})
}

// dialog writes a program for AGENTD_CONSENT: it appends a line to `ran`, waits delayS, exits code. ask is what the
// gateway calls; ran counts the dialogs that opened.
func dialog(t *testing.T, code int, delayS int) (ask func(consent.Question) consent.Answer, ran func() int) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "consent")
	ranFile := filepath.Join(dir, "ran")
	script := fmt.Sprintf("#!/bin/sh\necho \"$1\" >> %s\nsleep %d\nexit %d\n", ranFile, delayS, code)
	if err := os.WriteFile(file, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return askWith(file), func() int {
		b, err := os.ReadFile(ranFile)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}
}

func askWith(file string) func(consent.Question) consent.Answer {
	return func(q consent.Question) consent.Answer {
		return consent.Ask(q, home.Env{"AGENTD_CONSENT": file}, consent.TimeoutS)
	}
}

// world is what the gateway's deps stand for: the registry file, the hub of linked apps, and a record of what the
// gateway asked for.
type world struct {
	mu       sync.Mutex
	reg      registry.Registry
	linked   map[string]bool
	paired   []string // "origin code"
	revoked  []string
	asked    []consent.Question
	upgraded []string
	agents   []pagetools.AgentInfo
	ask      func(consent.Question) consent.Answer
	chosen   []string // the folders the user picks after an Allow; none by default means /work
	sessions *BrowserSessions
	now      func() time.Time // the clock of the grants it makes; nil is time.Now
}

func (w *world) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func newWorld() *world {
	return &world{
		reg:    registry.Empty(),
		linked: map[string]bool{},
		agents: []pagetools.AgentInfo{{Name: "claude", Title: "Claude Code", Ready: true}, {Name: "codex", Title: "Codex", Reason: "not installed"}},
	}
}

func (w *world) deps() Deps {
	return Deps{
		Version: "test",
		Agents:  func() []pagetools.AgentInfo { return w.agents },
		Registry: func() (registry.Registry, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.reg, nil
		},
		Ask: func(q consent.Question) consent.Decision {
			w.mu.Lock()
			w.asked = append(w.asked, q)
			ask, folders := w.ask, w.chosen
			w.mu.Unlock()
			answer := consent.AnswerAllow
			if ask != nil {
				answer = ask(q)
			}
			if answer != consent.AnswerAllow {
				return consent.Decision{Answer: answer}
			}
			if folders == nil {
				folders = []string{"/work"}
			}
			return consent.Decision{Answer: answer, Folders: folders}
		},
		Linked: func(server string) bool {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.linked[server]
		},
		ConnectServer: func(server, code string, folders []string) error {
			if code == "WRONG" {
				return fmt.Errorf("%s refused the code", server)
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			w.paired = append(w.paired, server+" "+code)
			w.reg = w.reg.AddApp(registry.App{Server: server, Token: "token", Folders: folders})
			w.linked[server] = true
			return nil
		},
		GrantBrowser: func(origin string, folders []string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.reg = w.reg.AddBrowser(origin, w.clock(), folders)
			return nil
		},
		Revoke: func(origin string) error {
			w.mu.Lock()
			w.revoked = append(w.revoked, origin)
			w.reg = w.reg.RemoveOrigin(origin)
			delete(w.linked, origin)
			sessions := w.sessions
			w.mu.Unlock()
			if sessions != nil {
				sessions.EndOrigin(origin)
			}
			return nil
		},
	}
}

func (w *world) apps() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var servers []string
	for _, a := range w.reg.Apps {
		servers = append(servers, a.Server)
	}
	return servers
}

func (w *world) asks() []consent.Question {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]consent.Question(nil), w.asked...)
}

// startGateway runs a gateway on any free port with the world's deps, the connecting process counting as the daemon's
// own user unless owners says otherwise.
func startGateway(t *testing.T, deps Deps, owners consent.PeerOwners) *Gateway {
	t.Helper()
	if owners == nil {
		owners = ownOwners
	}
	g, err := Start(deps, 0, Options{Gate: gateFor(owners)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// startGatewayAt is startGateway on the clock now, which the gateway reads for how old a grant is.
func startGatewayAt(t *testing.T, deps Deps, now func() time.Time) *Gateway {
	t.Helper()
	g, err := Start(deps, 0, Options{Gate: gateFor(ownOwners), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

type reply struct {
	status int
	body   map[string]any
	header http.Header
}

// callOpts shape a request: no Origin header when origin is empty; the gateway's own Host unless host is set.
type callOpts struct {
	origin string
	host   string
	body   any
	header map[string]string
}

func call(t *testing.T, port int, method, path string, o callOpts) reply {
	t.Helper()
	var payload io.Reader
	if o.body != nil {
		b, err := json.Marshal(o.body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if o.host != "" {
		req.Host = o.host
	}
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	if o.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range o.header {
		req.Header.Set(k, v)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	r := reply{status: res.StatusCode, body: map[string]any{}, header: res.Header}
	if len(text) > 0 {
		if err := json.Unmarshal(text, &r.body); err != nil {
			t.Fatalf("%s %s: body %q is not JSON: %v", method, path, text, err)
		}
	}
	return r
}

func connectAs(t *testing.T, port int, body any, origin string) reply {
	t.Helper()
	return call(t, port, http.MethodPost, "/v1/connect", callOpts{origin: origin, body: body})
}

// wantReply checks the status and the fields of the body that want names.
func wantReply(t *testing.T, r reply, status int, want map[string]any) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (body %v)", r.status, status, r.body)
	}
	for k, v := range want {
		if fmt.Sprint(r.body[k]) != fmt.Sprint(v) {
			t.Fatalf("body[%s] = %v, want %v (body %v)", k, r.body[k], v, r.body)
		}
	}
}

// granted is the `granted` object of a status answer.
func granted(t *testing.T, r reply) (server, browser bool) {
	t.Helper()
	g, ok := r.body["granted"].(map[string]any)
	if !ok {
		t.Fatalf("no granted in %v", r.body)
	}
	return g["server"] == true, g["browser"] == true
}

func registryApp(server string) registry.App { return registry.App{Server: server, Token: "token"} }

func osUID() int { return os.Getuid() }
