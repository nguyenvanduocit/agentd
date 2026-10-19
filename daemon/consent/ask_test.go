package consent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

var q = Question{Origin: "https://app.example.com", Mode: ModeServer, Agents: []string{"claude"}}

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestAskSeamExitZeroAllowsAndGetsTheTitleAndTheBody(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	allow := script(t, dir, "allow", `printf '%s\n%s' "$1" "$2" > `+seen+"\nexit 0")
	if got := Ask(q, home.Env{"AGENTD_CONSENT": allow}, TimeoutS); got != AnswerAllow {
		t.Fatalf("answer = %s", got)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	title, body, _ := strings.Cut(string(b), "\n")
	mustContain(t, title, "https://app.example.com")
	mustContain(t, body, "files")
}

func TestAskSeamNonZeroDenies(t *testing.T) {
	deny := script(t, t.TempDir(), "deny", "exit 1")
	if got := Ask(q, home.Env{"AGENTD_CONSENT": deny}, TimeoutS); got != AnswerDeny {
		t.Fatalf("answer = %s", got)
	}
}

func TestAskSeamMissingProgramIsUnavailableNeverAllow(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got := Ask(q, home.Env{"AGENTD_CONSENT": missing}, TimeoutS); got != AnswerUnavailable {
		t.Fatalf("answer = %s", got)
	}
}

func TestAskSeamProgramThatOutlivesTheTimeoutIsATimeout(t *testing.T) {
	// timeout + 5 s of grace in run(): a 1 s timeout makes the test wait about 6 s
	hang := script(t, t.TempDir(), "hang", "sleep 30")
	if got := Ask(q, home.Env{"AGENTD_CONSENT": hang}, 1); got != AnswerTimeout {
		t.Fatalf("answer = %s", got)
	}
}

func TestAskNoToolIsUnavailable(t *testing.T) {
	empty := home.Env{"PATH": t.TempDir()}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		if got := askOn(platform, q, empty, TimeoutS); got != AnswerUnavailable {
			t.Errorf("%s: answer = %s", platform, got)
		}
	}
}

func TestAskOsascriptOnPathWithTheTextAsArgv(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script(t, dir, "osascript", `printf '%s\0' "$@" > `+argv+"\necho 'Allow,false'")
	hostile := Question{Origin: `https://x" & (do shell script "id") & "\`, Mode: ModeBrowser, Agents: []string{"claude"}}
	if got := askOn("darwin", hostile, home.Env{"PATH": dir}, TimeoutS); got != AnswerAllow {
		t.Fatalf("answer = %s", got)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	text := ConsentText(hostile)
	if n := len(args); n < 2 || args[n-2] != text.Title || args[n-1] != text.Body {
		t.Fatalf("the title and the body are not the last two arguments: %q", args)
	}
	for _, line := range scripts(args[:len(args)-2]) {
		if strings.Contains(line, "shell script") || strings.Contains(line, hostile.Origin) {
			t.Errorf("the origin reached the script: %q", line)
		}
	}
}

func TestAskDialogToolAnswersMapThroughAnswerOf(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "zenity", "exit 5")
	if got := askOn("linux", q, home.Env{"PATH": dir}, TimeoutS); got != AnswerTimeout {
		t.Errorf("zenity 5: answer = %s", got)
	}
	script(t, dir, "zenity", "exit 0")
	if got := askOn("linux", q, home.Env{"PATH": dir}, TimeoutS); got != AnswerAllow {
		t.Errorf("zenity 0: answer = %s", got)
	}
	script(t, dir, "zenity", "kill -9 $$")
	if got := askOn("linux", q, home.Env{"PATH": dir}, TimeoutS); got != AnswerUnavailable {
		t.Errorf("zenity killed: answer = %s", got)
	}
}
