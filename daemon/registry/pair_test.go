package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) doerFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func failing(err error) doerFunc {
	return func(*http.Request) (*http.Response, error) { return nil, err }
}

var testMachine = Machine{Hostname: "box", OS: "linux", Arch: "x64"}

func pairWith(t *testing.T, client Doer, server, code, file string) (App, error) {
	t.Helper()
	return Pair(context.Background(), client, testMachine, server, code, file)
}

func TestPairPostsTheCodeWithThisMachinesFactsAndSavesTheToken(t *testing.T) {
	var gotURL string
	var gotBody map[string]string
	var gotType string
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL, gotType = r.URL.String(), r.Header.Get("Content-Type")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Error(err)
		}
		return reply(200, `{"token":"secret"}`)(r)
	})
	file := filepath.Join(t.TempDir(), "daemon.json")
	app, err := pairWith(t, client, "https://mc.example.com/", "CODE-1", file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, app, App{Server: "https://mc.example.com", Token: "secret"})
	if gotURL != "https://mc.example.com/agentd/pair" || gotType != "application/json" {
		t.Errorf("request = %s %s", gotURL, gotType)
	}
	wantEqual(t, gotBody, map[string]string{"code": "CODE-1", "hostname": "box", "os": "linux", "arch": "x64"})
	saved, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, string(saved), string(Empty().AddApp(app).Marshal()))
	if runtime.GOOS != "windows" {
		if got := mode(t, file); got != 0o600 {
			t.Errorf("file mode = %o, want 600", got)
		}
	}
}

func TestPairBodyKeysAreCodeHostnameOsArchInThatOrder(t *testing.T) {
	var raw []byte
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ = io.ReadAll(r.Body)
		return reply(200, `{"token":"t"}`)(r)
	})
	if _, err := pairWith(t, client, "https://s", "C", filepath.Join(t.TempDir(), "d.json")); err != nil {
		t.Fatal(err)
	}
	if want := `{"code":"C","hostname":"box","os":"linux","arch":"x64"}`; string(raw) != want {
		t.Errorf("body = %s, want %s", raw, want)
	}
}

func TestPairOverARealServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agentd/pair" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"token":"tok-123"}`)
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "daemon.json")
	app, err := pairWith(t, srv.Client(), srv.URL, "ABCD-1234", file)
	if err != nil {
		t.Fatal(err)
	}
	if app.Server != srv.URL || app.Token != "tok-123" {
		t.Errorf("app = %+v", app)
	}
}

func TestPairRefusedStatusesTellTheUserToCopyAFreshCommand(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 410} {
		_, err := pairWith(t, reply(status, "expired"), "https://s", "bad", filepath.Join(t.TempDir(), "d.json"))
		var pe *PairError
		if !errors.As(err, &pe) {
			t.Fatalf("HTTP %d: error = %v, want a PairError", status, err)
		}
		for _, want := range []string{fmt.Sprintf("HTTP %d: expired", status), "fresh command"} {
			if !strings.Contains(pe.Message, want) {
				t.Errorf("HTTP %d: %q does not contain %q", status, pe.Message, want)
			}
		}
	}
}

func TestPairAServerErrorIsADifferentMessage(t *testing.T) {
	_, err := pairWith(t, reply(503, ""), "https://s", "c", filepath.Join(t.TempDir(), "d.json"))
	var pe *PairError
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "failed to pair (HTTP 503)") || strings.Contains(pe.Message, "HTTP 503:") {
		t.Errorf("error = %v", err)
	}
}

func TestPairDetailIsCutAt200Characters(t *testing.T) {
	_, err := pairWith(t, reply(410, "  "+strings.Repeat("é", 300)+"  "), "https://s", "c", filepath.Join(t.TempDir(), "d.json"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 410: "+strings.Repeat("é", 200)+")") {
		t.Errorf("error = %v", err)
	}
}

func TestPairANetworkFailureNamesTheAddressAndTheCause(t *testing.T) {
	file := filepath.Join(t.TempDir(), "d.json")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"url error", &url.Error{Op: "Post", URL: "http://127.0.0.1:9/agentd/pair", Err: errors.New("connect ECONNREFUSED 127.0.0.1:9")}, "cannot reach http://127.0.0.1:9 (connect ECONNREFUSED 127.0.0.1:9)"},
		{"plain error", errors.New("boom"), "cannot reach http://127.0.0.1:9 (boom)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pairWith(t, failing(tc.err), "http://127.0.0.1:9", "c", file)
			var pe *PairError
			if !errors.As(err, &pe) || !strings.Contains(pe.Message, tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestPairNothingListeningIsCannotReach(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	origin := srv.URL
	srv.Close()
	_, err := pairWith(t, http.DefaultClient, origin, "c", filepath.Join(t.TempDir(), "d.json"))
	if err == nil || !strings.Contains(err.Error(), "cannot reach "+origin+" (") || strings.Contains(err.Error(), "/agentd/pair") {
		t.Errorf("error = %v", err)
	}
}

func TestPairGivesUpAfterTheTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slow := doerFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := Pair(ctx, slow, testMachine, "https://s", "c", filepath.Join(t.TempDir(), "d.json"))
	if err == nil || !strings.Contains(err.Error(), "cannot reach https://s (context deadline exceeded)") {
		t.Errorf("error = %v", err)
	}
}

func TestPairAnAnswerWithoutATokenSavesNothing(t *testing.T) {
	for _, body := range []string{"<html>", `{}`, `{"token":""}`, `{"token":5}`, `[]`, `null`} {
		file := filepath.Join(t.TempDir(), "d.json")
		_, err := pairWith(t, reply(200, body), "https://s", "c", file)
		if err == nil || !strings.Contains(err.Error(), "without a token") {
			t.Errorf("body %q: error = %v", body, err)
		}
		if _, statErr := os.Stat(file); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("body %q: file was created", body)
		}
	}
}

func TestPairABadServerAddressFailsBeforeAnyRequest(t *testing.T) {
	called := false
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return reply(200, "{}")(nil)
	})
	_, err := pairWith(t, client, "nonsense", "c", filepath.Join(t.TempDir(), "d.json"))
	if err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Errorf("error = %v", err)
	}
	if called {
		t.Error("a request was sent")
	}
}

func TestPairTwiceForOneServerKeepsOneAppWithTheLaterToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "d.json")
	for _, tok := range []string{"one", "two"} {
		if _, err := pairWith(t, reply(200, `{"token":"`+tok+`"}`), "https://s", "c", file); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Apps, []App{{Server: "https://s", Token: "two"}})
}

func TestMachineOfUsesTheNamesTheServerHasAlwaysReceived(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, wantOS, wantArch string }{
		{"darwin", "arm64", "darwin", "arm64"},
		{"linux", "amd64", "linux", "x64"},
		{"windows", "amd64", "win32", "x64"},
		{"windows", "386", "win32", "ia32"},
	} {
		wantEqual(t, MachineOf("h", tc.goos, tc.goarch), Machine{Hostname: "h", OS: tc.wantOS, Arch: tc.wantArch})
	}
}

type pairRig struct {
	deps   PairDeps
	out    *bytes.Buffer
	prompt *bytes.Buffer
	file   string
}

func newPairRig(t *testing.T, client Doer, env home.Env, stdin string) pairRig {
	t.Helper()
	dir := t.TempDir()
	out := &bytes.Buffer{}
	prompt := &bytes.Buffer{}
	if env == nil {
		env = home.Env{}
	}
	env["XDG_CONFIG_HOME"] = dir
	h := home.Host{Platform: "linux", Env: env, Home: "/nowhere"}
	return pairRig{
		deps:   PairDeps{Client: client, Machine: testMachine, Host: h, Log: proc.NewLogger(out, out), Stdin: strings.NewReader(stdin), Prompt: prompt},
		out:    out,
		prompt: prompt,
		file:   filepath.Join(dir, "agentd", "daemon.json"),
	}
}

func TestRunPairTradesTheCodeAndSaysWhereItSaved(t *testing.T) {
	var code string
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		code = body["code"]
		return reply(200, `{"token":"tok"}`)(r)
	})
	rig := newPairRig(t, client, nil, "")
	exit, err := RunPair(context.Background(), cli.Pair{App: "https://s", Code: ptr("ABCD-1234")}, rig.deps)
	if err != nil || exit != 0 {
		t.Fatalf("RunPair = %d, %v", exit, err)
	}
	if code != "ABCD-1234" {
		t.Errorf("code = %q", code)
	}
	if want := "paired with https://s; saved " + rig.file + ". Run agentd to connect.\n"; rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
	saved, err := Load(rig.file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Apps, []App{{Server: "https://s", Token: "tok"}})
}

func TestRunPairWritesToTheConfigFileOfTheFlag(t *testing.T) {
	rig := newPairRig(t, reply(200, `{"token":"tok"}`), nil, "")
	cfg := filepath.Join(t.TempDir(), "mine.json")
	if _, err := RunPair(context.Background(), cli.Pair{App: "https://s", Code: ptr("c"), Config: &cfg}, rig.deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Errorf("--config file not written: %v", err)
	}
}

func TestRunPairTheCodeComesFromTheFlagThenTheEnvironmentThenStdin(t *testing.T) {
	seen := func(into *string) doerFunc {
		return func(r *http.Request) (*http.Response, error) {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			*into = body["code"]
			return reply(200, `{"token":"t"}`)(r)
		}
	}
	var got string
	rig := newPairRig(t, seen(&got), home.Env{pairCodeEnv: "FROM-ENV"}, "FROM-STDIN\n")
	if _, err := RunPair(context.Background(), cli.Pair{App: "https://s", Code: ptr("FROM-FLAG")}, rig.deps); err != nil || got != "FROM-FLAG" {
		t.Errorf("flag: code = %q, err = %v", got, err)
	}
	rig = newPairRig(t, seen(&got), home.Env{pairCodeEnv: "FROM-ENV"}, "FROM-STDIN\n")
	if _, err := RunPair(context.Background(), cli.Pair{App: "https://s"}, rig.deps); err != nil || got != "FROM-ENV" {
		t.Errorf("env: code = %q, err = %v", got, err)
	}
	if rig.prompt.Len() != 0 {
		t.Errorf("prompted although the environment had the code: %q", rig.prompt.String())
	}
	rig = newPairRig(t, seen(&got), nil, "\n  \n  FROM-STDIN  \nlater\n")
	if _, err := RunPair(context.Background(), cli.Pair{App: "https://s"}, rig.deps); err != nil || got != "FROM-STDIN" {
		t.Errorf("stdin: code = %q, err = %v", got, err)
	}
	if rig.prompt.String() != "Pairing code: " {
		t.Errorf("prompt = %q", rig.prompt.String())
	}
}

func TestRunPairWithoutACodeSaysSo(t *testing.T) {
	rig := newPairRig(t, reply(200, `{"token":"t"}`), nil, "")
	_, err := RunPair(context.Background(), cli.Pair{App: "https://x"}, rig.deps)
	var pe *PairError
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "no pairing code provided") {
		t.Errorf("error = %v", err)
	}
}

func TestRunPairARefusedCodeReportsTheStatusAndSavesNothing(t *testing.T) {
	rig := newPairRig(t, reply(410, "code expired"), nil, "")
	_, err := RunPair(context.Background(), cli.Pair{App: "https://s", Code: ptr("OLD")}, rig.deps)
	if err == nil || !strings.Contains(err.Error(), "refused the pairing code (HTTP 410: code expired)") {
		t.Errorf("error = %v", err)
	}
	if _, statErr := os.Stat(rig.file); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a registry file was written")
	}
	if rig.out.Len() != 0 {
		t.Errorf("output = %q, want none", rig.out.String())
	}
}
