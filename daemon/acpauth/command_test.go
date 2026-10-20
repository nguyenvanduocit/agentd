package acpauth

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
)

type run struct {
	code           int
	stdout, stderr string
	dialled        []string
}

// runACP runs `agentd acp` over home; the daemon answers on its port when answers is true.
func runACP(t *testing.T, home string, cmd cli.ACP, answers bool) run {
	t.Helper()
	var out, errOut bytes.Buffer
	var r run
	d := Deps{Home: home, Stdout: &out, Stderr: &errOut, Dial: func(addr string, _ time.Duration) error {
		r.dialled = append(r.dialled, addr)
		if answers {
			return nil
		}
		return errors.New("connection refused")
	}}
	r.code = RunACP(cmd, d)
	r.stdout, r.stderr = out.String(), errOut.String()
	return r
}

// running is a home with a token and the record of a daemon on port 47617 with claude and grok ready.
func running(t *testing.T) (home, token string) {
	t.Helper()
	home = t.TempDir()
	var err error
	if token, err = Ensure(home); err != nil {
		t.Fatal(err)
	}
	if err := WriteInfo(home, Info{Address: "ws://127.0.0.1:47617/acp", Agents: []string{"claude", "grok"}, Updated: "x"}); err != nil {
		t.Fatal(err)
	}
	return home, token
}

func TestAgentdAcpPrintsOneAddressForEachReadyAgent(t *testing.T) {
	home, token := running(t)
	r := runACP(t, home, cli.ACP{}, true)
	want := "ws://127.0.0.1:47617/acp/claude?token=" + token + "\nws://127.0.0.1:47617/acp/grok?token=" + token + "\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d, stdout %q\nwant %q", r.code, r.stdout, want)
	}
	if strings.Contains(r.stderr, token) || !strings.Contains(r.stderr, `Authorization: Bearer <token>`) || strings.Contains(r.stderr, "not running") {
		t.Errorf("stderr %q: it should name the header form with a placeholder, never the token, and not warn", r.stderr)
	}
	if len(r.dialled) != 1 || r.dialled[0] != "127.0.0.1:47617" {
		t.Errorf("it asked whether the daemon answers by dialling %v", r.dialled)
	}
}

func TestAgentdAcpWithANamedAgentPrintsThatOne(t *testing.T) {
	home, token := running(t)
	r := runACP(t, home, cli.ACP{Agent: "grok"}, true)
	if r.code != 0 || r.stdout != "ws://127.0.0.1:47617/acp/grok?token="+token+"\n" {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
	// An agent that is not ready is still given, with a word about it: an editor can be configured before it is.
	r = runACP(t, home, cli.ACP{Agent: "codex"}, true)
	if r.code != 0 || r.stdout != "ws://127.0.0.1:47617/acp/codex?token="+token+"\n" || !strings.Contains(r.stderr, "codex is not ready") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r = runACP(t, home, cli.ACP{Agent: "nosuch"}, true)
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, `unknown agent "nosuch": choose claude, codex or grok`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestAgentdAcpTokenPrintsTheTokenAlone(t *testing.T) {
	home, token := running(t)
	r := runACP(t, home, cli.ACP{Token: true}, true)
	if r.code != 0 || r.stdout != token+"\n" || strings.Contains(r.stderr, token) {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the token and a newline on stdout and nothing of it elsewhere", r.code, r.stdout, r.stderr)
	}
}

func TestAgentdAcpRotateMakesANewTokenAndPrintsIt(t *testing.T) {
	home, old := running(t)
	r := runACP(t, home, cli.ACP{Rotate: true, Agent: "claude"}, true)
	current, err := Load(home)
	if err != nil || current == old {
		t.Fatalf("after --rotate the file holds %q (%v); want a token other than %q", current, err, old)
	}
	if r.code != 0 || r.stdout != "ws://127.0.0.1:47617/acp/claude?token="+current+"\n" || strings.Contains(r.stdout, old) {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
	r = runACP(t, home, cli.ACP{Rotate: true, Token: true}, true)
	again, _ := Load(home)
	if r.stdout != again+"\n" || again == current {
		t.Errorf("--rotate --token printed %q, the file holds %q (before: %q)", r.stdout, again, current)
	}
}

func TestAgentdAcpRotateMakesTheFirstTokenToo(t *testing.T) {
	home := t.TempDir()
	r := runACP(t, home, cli.ACP{Rotate: true, Token: true}, false)
	token, err := Load(home)
	if err != nil || r.code != 0 || r.stdout != token+"\n" {
		t.Fatalf("exit %d, stdout %q, file %q (%v)", r.code, r.stdout, token, err)
	}
}

func TestAgentdAcpSaysWhenAgentdHasNotRunYet(t *testing.T) {
	home := t.TempDir()
	r := runACP(t, home, cli.ACP{}, false)
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "has not run on this computer yet") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(Dir(home)); err == nil {
		t.Error("asking for the address made a token")
	}
}

func TestAgentdAcpWarnsButStillPrintsWhenAgentdDoesNotAnswer(t *testing.T) {
	home, token := running(t)
	r := runACP(t, home, cli.ACP{Agent: "claude"}, false)
	if r.code != 0 || r.stdout != "ws://127.0.0.1:47617/acp/claude?token="+token+"\n" {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "agentd is not running on port 47617") {
		t.Errorf("stderr %q: it should say agentd does not answer on the recorded port", r.stderr)
	}
}

func TestAgentdAcpWithNoRecordAssumesThePortAndSaysSo(t *testing.T) {
	home := t.TempDir()
	token, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	r := runACP(t, home, cli.ACP{}, false)
	want := ""
	for _, a := range []string{"claude", "codex", "grok"} {
		want += "ws://127.0.0.1:47615/acp/" + a + "?token=" + token + "\n"
	}
	if r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d, stdout %q\nwant %q", r.code, r.stdout, want)
	}
	if !strings.Contains(r.stderr, "agentd is not running") || !strings.Contains(r.stderr, "47615") || len(r.dialled) != 0 {
		t.Errorf("stderr %q, dialled %v: it should say agentd is not running and which port the address assumes", r.stderr, r.dialled)
	}
}

func TestAgentdAcpWithNoAgentReadyHasNothingToPrint(t *testing.T) {
	home := t.TempDir()
	if _, err := Ensure(home); err != nil {
		t.Fatal(err)
	}
	if err := WriteInfo(home, Info{Address: "ws://127.0.0.1:47615/acp", Agents: nil, Updated: "x"}); err != nil {
		t.Fatal(err)
	}
	r := runACP(t, home, cli.ACP{}, true)
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "no agent is ready yet") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestAgentdAcpDoesNotUseATokenFileOthersCouldRead(t *testing.T) {
	unixOnly(t)
	home, _ := running(t)
	if err := os.Chmod(TokenFile(home), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runACP(t, home, cli.ACP{}, true)
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "agentd acp --rotate") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want a refusal that points at --rotate", r.code, r.stdout, r.stderr)
	}
	r = runACP(t, home, cli.ACP{Rotate: true, Token: true}, true)
	if r.code != 0 || !Valid(strings.TrimSpace(r.stdout)) {
		t.Fatalf("--rotate did not repair it: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestAgentdAcpSpeaksPlainly(t *testing.T) {
	home := t.TempDir()
	messages := runACP(t, home, cli.ACP{}, false).stderr
	r := runACP(t, home, cli.ACP{Agent: "nosuch"}, false)
	messages += r.stderr
	home2, _ := running(t)
	messages += runACP(t, home2, cli.ACP{}, false).stderr
	for _, jargon := range []string{"daemon", "engine", "origin"} {
		if strings.Contains(strings.ToLower(messages), jargon) {
			t.Errorf("a message says %q:\n%s", jargon, messages)
		}
	}
}
