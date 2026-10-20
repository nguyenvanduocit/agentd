package goclient_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// browserLocal is a LocalDaemon in browser mode whose looks find a daemon on the fake daemon's port, allowed or not.
func browserLocal(t *testing.T, d *fakeDaemon, allowed bool, more ...goclient.Detected) (*goclient.LocalDaemon, *fakeClient) {
	t.Helper()
	first := goclient.Detected{Found: true, Port: d.Daemon.Port, Version: "1.0.0", Granted: goclient.Granted{Browser: allowed}}
	local, client := setup(t, setupOptions{mode: goclient.ModeBrowser, detected: append([]goclient.Detected{first}, more...)})
	local.Detect(bg)
	return local, client
}

type opened struct {
	s   *goclient.Session
	err error
}

func openAsync(local *goclient.LocalDaemon, agent string) <-chan opened {
	out := make(chan opened, 1)
	go func() {
		s, err := local.OpenSession(bg, agent)
		out <- opened{s, err}
	}()
	return out
}

func TestOpenSessionOpensTheAgentOnTheDaemonTheLastLookFound(t *testing.T) {
	d := startFakeDaemon(t)
	local, _ := browserLocal(t, d, true)
	result := openAsync(local, "claude")
	d.accept()
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	open := d.nextSent(0)
	if open["agent"] != "claude" {
		t.Fatalf("open = %v", open)
	}
	d.say(msg{"t": "opened", "id": open["id"], "pid": 7, "cwd": "/work"})
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatal(r.err)
		}
		defer func() { _ = r.s.Close() }()
		if r.s.Pid != 7 || r.s.Cwd != "/work" {
			t.Fatalf("session = %+v", r.s)
		}
	case <-time.After(wait):
		t.Fatal("the session did not open")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.origin != "https://app.example.com" { // the origin the LocalDaemon was made with
		t.Fatalf("Origin = %q", d.origin)
	}
}

func TestOpenSessionWithoutAGrantIsErrNotAllowedAndNeverDials(t *testing.T) {
	d := startFakeDaemon(t)
	local, client := browserLocal(t, d, false, goclient.Detected{Found: true, Port: d.Daemon.Port, Version: "1.0.0"})
	_, err := local.OpenSession(bg, "claude")
	if !errors.Is(err, goclient.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if n := client.count("detect"); n != 2 { // the look before, and one more: the grant may have come since
		t.Fatalf("looks = %d", n)
	}
	select {
	case <-d.conns:
		t.Fatal("a socket was opened without a grant")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOpenSessionLooksOnceMoreSoAGrantThatCameSinceOpens(t *testing.T) {
	d := startFakeDaemon(t)
	later := goclient.Detected{Found: true, Port: d.Daemon.Port, Version: "1.0.0", Granted: goclient.Granted{Browser: true}}
	local, _ := browserLocal(t, d, false, later)
	result := openAsync(local, "claude")
	d.accept()
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	d.say(msg{"t": "opened", "id": d.nextSent(0)["id"], "pid": 1, "cwd": "/w"})
	if r := <-result; r.err != nil {
		t.Fatal(r.err)
	} else {
		_ = r.s.Close()
	}
}

func TestOpenSessionWithNoDaemonIsErrNoDaemon(t *testing.T) {
	local, _ := setup(t, setupOptions{mode: goclient.ModeBrowser, detected: []goclient.Detected{notFound}})
	local.Detect(bg)
	if _, err := local.OpenSession(bg, "claude"); !errors.Is(err, goclient.ErrNoDaemon) {
		t.Fatalf("err = %v, want ErrNoDaemon", err)
	}
}

func TestOpenSessionNeedsBrowserMode(t *testing.T) {
	local, _ := setup(t, setupOptions{detected: []goclient.Detected{connected}})
	local.Detect(bg)
	if _, err := local.OpenSession(bg, "claude"); err == nil || !strings.Contains(err.Error(), "ModeBrowser") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnAgentTheDaemonRefusesIsASessionErrorWithItsOwnWords(t *testing.T) {
	d := startFakeDaemon(t)
	local, _ := browserLocal(t, d, true)
	result := openAsync(local, "nosuch")
	d.accept()
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	const words = `unknown agent "nosuch": this daemon runs claude, codex, grok`
	d.say(msg{"t": "error", "id": d.nextSent(0)["id"], "message": words})
	r := <-result
	var refused *goclient.SessionError
	if !errors.As(r.err, &refused) {
		t.Fatalf("err = %v, want a *SessionError", r.err)
	}
	if refused.Agent != "nosuch" || refused.Message != words || refused.Reason != goclient.ReasonError || r.err.Error() != words {
		t.Fatalf("SessionError = %+v, Error() = %q", refused, r.err.Error())
	}
}

func TestTheLowLevelOpenSessionAlsoReturnsTheTypedErrors(t *testing.T) {
	// No grant: the gateway answers without upgrading the socket.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	_, err := goclient.OpenSession(bg, goclient.Daemon{Port: srv.Listener.Addr().(*net.TCPAddr).Port}, "http://localhost:5173", "claude")
	if !errors.Is(err, goclient.ErrNotAllowed) || !strings.Contains(err.Error(), "cannot open a session: the daemon refused the connection") {
		t.Fatalf("a refused socket: %v", err)
	}
	// Nothing listens.
	l, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	_, err = goclient.OpenSession(bg, goclient.Daemon{Port: port}, "http://localhost:5173", "claude")
	if !errors.Is(err, goclient.ErrNoDaemon) || errors.Is(err, goclient.ErrNotAllowed) {
		t.Fatalf("no daemon: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(port)) {
		t.Fatalf("the message does not name the port: %v", err)
	}
	// An agent the daemon refuses through the low-level call is a SessionError too.
	d := startFakeDaemon(t)
	errs := make(chan error, 1)
	go func() {
		_, err := goclient.OpenSession(context.Background(), d.Daemon, "http://localhost:5173", "rm -rf")
		errs <- err
	}()
	d.accept()
	d.say(msg{"t": "hello", "version": "1", "agents": []any{}})
	d.say(msg{"t": "error", "id": d.nextSent(0)["id"], "message": `unknown agent "rm -rf"`})
	var refused *goclient.SessionError
	if err := <-errs; !errors.As(err, &refused) || refused.Agent != "rm -rf" || err.Error() != `unknown agent "rm -rf"` {
		t.Fatalf("error = %v", err)
	}
}
