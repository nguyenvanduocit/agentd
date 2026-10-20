package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// result is what a command did: its exit code and what it printed.
type result struct {
	code   int
	stdout string
	stderr string
}

// sandbox points the config and the home of the commands at a temporary directory (they read this process's
// environment) and returns the registry file.
func sandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("AGENTD_HOME", filepath.Join(root, "mc"))
	t.Setenv("AGENTD_PAIR_CODE", "") // restored at the end of the test
	if err := os.Unsetenv("AGENTD_PAIR_CODE"); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "config", "agentd", "daemon.json")
}

func execute(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	p := process{stdin: strings.NewReader(stdin), stdout: &stdout, stderr: &stderr, log: proc.NewLogger(&stdout, &stderr), tracker: proc.NewTracker()}
	code := dispatch(args, p)
	return result{code, stdout.String(), stderr.String()}
}

func TestVersionPrintsTheVersionBakedInAtBuildTime(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		r := execute(t, "", arg)
		if r.code != 0 || r.stdout != home.Version+"\n" || r.stderr != "" {
			t.Fatalf("%s: %+v", arg, r)
		}
	}
}

func TestHelpPrintsTheUsageOnStdoutAndExits0(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		r := execute(t, "", arg)
		if r.code != 0 || !strings.Contains(r.stdout, "agentd pair --app") || r.stderr != "" {
			t.Fatalf("%s: %+v", arg, r)
		}
	}
}

func TestAnUnknownOptionSaysSoAndPointsAtHelp(t *testing.T) {
	r := execute(t, "", "--nope")
	if r.code != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, `unknown option --nope. Run "agentd --help".`+"\n\nagentd: runs your agents") {
		t.Fatalf("%+v", r)
	}
}

func TestAMistakePrintsItAndTheUsageOnStderrAndExits2(t *testing.T) {
	r := execute(t, "", "frobnicate")
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, `unknown command "frobnicate"`) || !strings.Contains(r.stderr, "\n\nagentd: runs your agents") || !strings.Contains(r.stderr, "usage:") {
		t.Fatalf("%+v", r)
	}
}

func TestRunningForAnAppThatIsNotPairedSaysHowToPairAndExits1(t *testing.T) {
	sandbox(t)
	r := execute(t, "", "--app", "https://nowhere.example")
	if r.code != 1 || !strings.Contains(r.stderr, "not paired with https://nowhere.example") || !strings.Contains(r.stderr, "agentd pair --app") {
		t.Fatalf("%+v", r)
	}
}

func TestAppsListsEveryAppAndBrowserGrantAndRemovesOneOrigin(t *testing.T) {
	file := sandbox(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"apps":[{"server":"https://a.example","token":"ta"},{"server":"https://b.example","token":"tb"}],"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z"}]}`
	if err := os.WriteFile(file, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	list := execute(t, "", "apps")
	for _, want := range []string{"https://a.example  app", "https://b.example  app", "https://c.example  browser, granted 2026-10-19"} {
		if list.code != 0 || !strings.Contains(list.stdout, want) {
			t.Fatalf("apps: %+v, want %q", list, want)
		}
	}
	if strings.Contains(list.stdout, "ta") {
		t.Fatalf("a token was printed: %q", list.stdout)
	}
	if r := execute(t, "", "apps", "remove", "https://a.example"); r.code != 0 {
		t.Fatalf("remove: %+v", r)
	}
	var saved struct {
		Apps []struct{ Server, Token string }
	}
	data, _ := os.ReadFile(file)
	if err := json.Unmarshal(data, &saved); err != nil || len(saved.Apps) != 1 || saved.Apps[0].Server != "https://b.example" {
		t.Fatalf("saved %s (%v)", data, err)
	}
	gone := execute(t, "", "apps", "remove", "https://a.example")
	if gone.code != 1 || !strings.Contains(gone.stderr, "not connected to https://a.example") {
		t.Fatalf("remove again: %+v", gone)
	}
}

// pairServer answers POST /agentd/pair with handle and returns its origin and the body of the last request.
func pairServer(t *testing.T, handle func(w http.ResponseWriter)) (string, func() map[string]string) {
	t.Helper()
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agentd/pair" {
			http.NotFound(w, r)
			return
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &got)
		handle(w)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() map[string]string { return got }
}

func TestPairTradesTheCodeForATokenAndSavesItWithMode0600(t *testing.T) {
	file := sandbox(t)
	server, body := pairServer(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":"tok-123"}`) })
	r := execute(t, "", "pair", "--app", server, "--code", "ABCD-1234")
	if r.code != 0 || r.stdout != "Paired with "+server+".\n" {
		t.Fatalf("%+v", r)
	}
	if got := body(); got["code"] != "ABCD-1234" || got["os"] == "" || got["arch"] == "" || got["hostname"] == "" {
		t.Fatalf("the server got %v", got)
	}
	data, _ := os.ReadFile(file)
	var saved struct {
		Apps []struct{ Server, Token string }
	}
	if err := json.Unmarshal(data, &saved); err != nil || len(saved.Apps) != 1 || saved.Apps[0].Token != "tok-123" || saved.Apps[0].Server != server {
		t.Fatalf("saved %s (%v)", data, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v (%v)", info.Mode(), err)
	}
	if info, err := os.Stat(filepath.Dir(file)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v (%v)", info.Mode(), err)
	}
}

func TestPairWithARefusedCodeSaysWhatToDoAndSavesNothing(t *testing.T) {
	file := sandbox(t)
	server, _ := pairServer(t, func(w http.ResponseWriter) { http.Error(w, "code expired", http.StatusGone) })
	r := execute(t, "", "pair", "--app", server, "--code", "OLD")
	if r.code != 1 || !strings.Contains(r.stderr, "refused the pairing code (HTTP 410: code expired)") || !strings.Contains(r.stderr, "copy a fresh command from the app's web UI") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the registry was written (%v)", err)
	}
}

func TestPairWithAServerThatIsNotThereNamesIt(t *testing.T) {
	sandbox(t)
	r := execute(t, "", "pair", "--app", "http://127.0.0.1:9", "--code", "X")
	if r.code != 1 || !strings.Contains(r.stderr, "cannot reach http://127.0.0.1:9") {
		t.Fatalf("%+v", r)
	}
}

func TestPairNeedsAnAppAndACode(t *testing.T) {
	sandbox(t)
	if r := execute(t, "", "pair", "--code", "X"); !strings.Contains(r.stderr, "pair needs --app") {
		t.Fatalf("%+v", r)
	}
	r := execute(t, "", "pair", "--app", "https://x")
	if r.code != 1 || !strings.Contains(r.stderr, "no pairing code provided") {
		t.Fatalf("%+v", r)
	}
}

func TestPairReadsTheCodeFromStdin(t *testing.T) {
	sandbox(t)
	server, body := pairServer(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":"t"}`) })
	if r := execute(t, "FROM-STDIN\n", "pair", "--app", server); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if body()["code"] != "FROM-STDIN" {
		t.Fatalf("the server got %v", body())
	}
}

// `agentd acp` is a command of the entry: it reads agentd's own home, prints addresses to stdout and the rest to stderr.
func TestAcpCommandReadsAgentdsHome(t *testing.T) {
	sandbox(t)
	if r := execute(t, "", "acp"); r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "has not run on this computer yet") {
		t.Fatalf("before agentd has run: %+v", r)
	}
	r := execute(t, "", "acp", "--rotate", "--token")
	token := strings.TrimSpace(r.stdout)
	if r.code != 0 || len(token) != 43 {
		t.Fatalf("acp --rotate --token: %+v", r)
	}
	r = execute(t, "", "acp", "claude")
	if r.code != 0 || !strings.HasSuffix(r.stdout, "/acp/claude?token="+token+"\n") || !strings.Contains(r.stderr, "agentd is not running") {
		t.Fatalf("acp claude: %+v (token %s)", r, token)
	}
}

// `agentd --token` used to start a daemon: the flag was accepted and ignored. It is a mistake, told as one. The command
// runs in a goroutine and is waited for a moment only: a parser that let it through would start a daemon that never
// returns, and the test must fail, not hang.
func TestTokenWithoutAcpIsAMistakeAndStartsNothing(t *testing.T) {
	sandbox(t)
	for _, args := range [][]string{{"--token"}, {"--rotate"}, {"run", "--token"}} {
		done := make(chan result, 1)
		go func() { done <- execute(t, "", args...) }()
		select {
		case r := <-done:
			flag := args[len(args)-1]
			if r.code != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, flag+` only works with "agentd acp"`+"\n\nagentd: runs your agents") {
				t.Errorf("agentd %v: %+v", args, r)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("agentd %v did not return: it started a daemon", args)
		}
	}
}
