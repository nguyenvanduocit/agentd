package consent

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestDecideSeamExitZeroAllowsAndGetsTheTitleAndTheBody(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	allow := script(t, dir, "allow", `printf '%s\n%s' "$1" "$2" > `+seen+"\nexit 0")
	if got := Decide(q, home.Env{"AGENTD_CONSENT": allow}, TimeoutS).Answer; got != AnswerAllow {
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

func TestDecideSeamNonZeroDenies(t *testing.T) {
	deny := script(t, t.TempDir(), "deny", "exit 1")
	if got := Decide(q, home.Env{"AGENTD_CONSENT": deny}, TimeoutS).Answer; got != AnswerDeny {
		t.Fatalf("answer = %s", got)
	}
}

func TestDecideSeamMissingProgramIsUnavailableNeverAllow(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got := Decide(q, home.Env{"AGENTD_CONSENT": missing}, TimeoutS).Answer; got != AnswerUnavailable {
		t.Fatalf("answer = %s", got)
	}
}

func TestDecideSeamProgramThatOutlivesTheTimeoutIsATimeout(t *testing.T) {
	// timeout + 5 s of grace in run(): a 1 s timeout makes the test wait about 6 s
	hang := script(t, t.TempDir(), "hang", "sleep 30")
	if got := Decide(q, home.Env{"AGENTD_CONSENT": hang}, 1).Answer; got != AnswerTimeout {
		t.Fatalf("answer = %s", got)
	}
}

func TestDecideSeamPrintsTheFoldersAndExitsZero(t *testing.T) {
	root := t.TempDir()
	allow := script(t, t.TempDir(), "allow", "printf '%s\\n\\n%s/\\nrelative\\n%s\\n' "+root+" "+root+"/sub "+root)
	got := Decide(q, home.Env{"AGENTD_CONSENT": allow}, TimeoutS)
	if got.Answer != AnswerAllow || !reflect.DeepEqual(got.Folders, []string{root, root + "/sub"}) {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideDenyHasNoFolders(t *testing.T) {
	deny := script(t, t.TempDir(), "deny", "echo /tmp\nexit 1")
	if got := Decide(q, home.Env{"AGENTD_CONSENT": deny}, TimeoutS); got.Answer != AnswerDeny || got.Folders != nil {
		t.Fatalf("decision = %+v", got)
	}
}
