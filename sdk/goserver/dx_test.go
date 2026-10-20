package goserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

func TestNewWithoutTokensFailsAtOnceAndSaysWhy(t *testing.T) {
	defer func() {
		if got := fmt.Sprint(recover()); !strings.Contains(got, "goserver: Options.Tokens is required") {
			t.Fatalf("New(Options{}) panicked with %q, want it to name Options.Tokens", got)
		}
	}()
	goserver.New(goserver.Options{})
	t.Fatal("New(Options{}) returned a Server that panics at its first use")
}

func TestSocketOptionsHoldTheLargestMessage(t *testing.T) {
	if got := goserver.SocketOptions().MaxHttpBufferSize(); got != goserver.MaxMessageBytes {
		t.Fatalf("MaxHttpBufferSize = %d, want MaxMessageBytes (%d): a tool result of a few MB would end the session", got, goserver.MaxMessageBytes)
	}
}

// pairOver trades a pairing code for a token over the HTTP endpoint, as a daemon does.
func pairOver(t *testing.T, base, code string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "hostname": "laptop", "os": "darwin", "arch": "arm64"})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/agentd/pair", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Token string }
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil || out.Token == "" {
		t.Fatalf("pair = %d, token %q", res.StatusCode, out.Token)
	}
	return out.Token
}

func TestAttachServesWhatADaemonNeedsAndNothingElseIsWired(t *testing.T) {
	tokens := goserver.NewMemoryTokens()
	h := goserver.New(goserver.Options{Tokens: tokens})
	mux := http.NewServeMux()
	shutdown := h.Attach(mux) // the only wiring: no Socket.IO server, no /socket.io/ handler, no buffer size
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { srv.Close(); shutdown() })

	list, err := h.StartPairing(alice)
	if err != nil {
		t.Fatal(err)
	}
	token := pairOver(t, srv.URL, list.Pairing.Code)
	sum := sha256.Sum256([]byte(token))
	rec, err := tokens.ByHash(hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	d := connectDaemon(t, srv.URL, token)
	d.id, d.token = rec.ID, token
	d.hello(contract.Hello{Version: "0.7.0", Hostname: "laptop", Os: "darwin", Arch: "arm64", Agents: allAgents(), Managed: true})
	waitDaemon(t, h, alice, rec.ID, goserver.StateOnline)
}

func TestMountAloneDoesNotServeSocketIO(t *testing.T) {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	mux := http.NewServeMux()
	io := socket.NewServer(nil, goserver.SocketOptions())
	t.Cleanup(func() { io.Close(nil) })
	h.Mount(io, mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/socket.io/?EIO=4&transport=polling", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /socket.io/ after Mount = %d, want 404: Mount serves only the namespace and POST /agentd/pair (use Attach, or add the /socket.io/ handler)", res.StatusCode)
	}
}

func TestEveryCallSaysUnknownDaemonForAnIdThatIsNotTheUsers(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	calls := map[string]func() error{
		"Open":    func() error { _, err := e.server.Open(context.Background(), alice, "nope", "claude"); return err },
		"Ready":   func() error { _, err := e.server.Ready(alice, "nope"); return err },
		"Revoke":  func() error { _, err := e.server.Revoke(alice, "nope"); return err },
		"Rename":  func() error { _, err := e.server.Rename(alice, "nope", "x"); return err },
		"Restart": func() error { _, err := e.server.Restart(alice, "nope"); return err },
		"Stop":    func() error { _, err := e.server.Stop(alice, "nope"); return err },
		"Open as bob": func() error {
			_, err := e.server.Open(context.Background(), bob, d.id, "claude")
			return err
		},
		"Restart as bob": func() error { _, err := e.server.Restart(bob, d.id); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, goserver.ErrUnknownDaemon) {
			t.Errorf("%s: %v, want errors.Is(err, ErrUnknownDaemon)", name, err)
		}
		if !errors.Is(err, goserver.ErrNoSuchDaemon) { // the old name matches the same error
			t.Errorf("%s: %v does not match the deprecated ErrNoSuchDaemon", name, err)
		}
	}
}

func TestControlCallsOnAnOfflineDaemonCanBeTestedAsOffline(t *testing.T) {
	e := newEnv(t)
	id := e.idOf(t, e.pairHost(t, alice, "work mac"))
	for name, call := range map[string]func(string, string) (goserver.List, error){"Restart": e.server.Restart, "Stop": e.server.Stop} {
		_, err := call(alice, id)
		var offline goserver.OfflineError
		if !errors.As(err, &offline) || offline.Name != "work mac" {
			t.Errorf("%s: %v, want errors.As to find the OfflineError of \"work mac\"", name, err)
		}
		if err == nil || err.Error() != "work mac is offline" {
			t.Errorf("%s: message %v, want it unchanged", name, err)
		}
	}
}

func TestRenameWithABadNameIsErrBadName(t *testing.T) {
	e := newEnv(t)
	id := e.idOf(t, e.pairHost(t, alice, "work mac"))
	for _, name := range []string{"", "   ", "tab\there", strings.Repeat("x", 65)} {
		if _, err := e.server.Rename(alice, id, name); !errors.Is(err, goserver.ErrBadName) {
			t.Errorf("Rename(%q) = %v, want ErrBadName", name, err)
		}
	}
}

func TestAnAgentThatCannotStartIsAnAgentNotReadyError(t *testing.T) {
	e := newEnv(t)
	d := e.online(t, alice)
	_, err := e.server.Open(context.Background(), alice, d.id, "codex")
	var notReady *goserver.AgentNotReadyError
	if !errors.As(err, &notReady) || notReady.Daemon != "laptop" || notReady.Agent != "codex" || notReady.Title != "Codex" || notReady.Reason != "Node is not installed" || !notReady.Offered {
		t.Fatalf("Open codex: %v / %+v, want the daemon, agent, title and the daemon's reason", err, notReady)
	}
	_, err = e.server.Open(context.Background(), alice, d.id, "gemini")
	if !errors.As(err, &notReady) || notReady.Agent != "gemini" || notReady.Offered {
		t.Fatalf("Open gemini: %v / %+v, want an agent the daemon does not offer", err, notReady)
	}
}

func TestInfoStateHasNamedValues(t *testing.T) {
	e := newEnv(t)
	off := e.idOf(t, e.pairHost(t, alice, "work mac"))
	on := e.online(t, alice)
	list := e.list(t, alice)
	if got, _ := infoOf(list, off); got.State != goserver.StateOffline {
		t.Errorf("offline daemon: State = %q, want StateOffline", got.State)
	}
	if got, _ := infoOf(list, on.id); got.State != goserver.StateOnline {
		t.Errorf("online daemon: State = %q, want StateOnline", got.State)
	}
	if goserver.StateOnline != "online" || goserver.StateOffline != "offline" {
		t.Errorf("the constants changed the JSON values: %q, %q", goserver.StateOnline, goserver.StateOffline)
	}
}

func TestOpenLatestPicksTheNewestOnlineDaemon(t *testing.T) {
	e := newEnv(t)
	if _, err := e.server.OpenLatest(context.Background(), alice, "claude"); !errors.Is(err, goserver.ErrNoDaemon) {
		t.Fatalf("no daemon paired: %v, want ErrNoDaemon", err)
	}
	e.pairHost(t, alice, "work mac") // paired, never online
	if _, err := e.server.OpenLatest(context.Background(), alice, "claude"); !errors.Is(err, goserver.ErrNoOnlineDaemon) {
		t.Fatalf("paired but offline: %v, want ErrNoOnlineDaemon", err)
	}
	first := e.onlineAs(t, alice, "first")
	second := e.onlineAs(t, alice, "second")
	conn, err := e.server.OpenLatest(context.Background(), alice, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := second.openedSessions(); len(got) != 1 {
		t.Fatalf("the newest daemon was asked to open %v, the older one %v", got, first.openedSessions())
	}
	if got := first.openedSessions(); len(got) != 0 {
		t.Fatalf("the older daemon was asked to open %v", got)
	}
}

func TestWatchClosesItsChannelWhenCancelled(t *testing.T) {
	e := newEnv(t)
	ch, cancel := e.server.Watch(alice)
	<-ch // the current list
	cancel()
	cancel() // a second call is harmless
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("`for range` over a cancelled Watch never ends")
	}
}

// AgentInfo is what the other SDKs call an agent.
var _ goserver.AgentInfo = goserver.Agent{}
