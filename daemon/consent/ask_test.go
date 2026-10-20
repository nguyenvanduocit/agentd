package consent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	text := DialogText(hostile)
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

func TestDecideAfterAllowTheUserPicksFolders(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script(t, dir, "osascript", `case "$*" in *"choose folder"*) printf '%s\0' "$@" > `+argv+`; printf '/Users/me/a/\n/Users/me/b/\n';; *) echo 'Allow,false';; esac`)
	hostile := Question{Origin: `https://x" & (do shell script "id") & "\`, Mode: ModeBrowser, Agents: []string{"claude"}}
	got := decideOn("darwin", hostile, home.Env{"PATH": dir}, TimeoutS)
	if got.Answer != AnswerAllow || !reflect.DeepEqual(got.Folders, []string{"/Users/me/a", "/Users/me/b"}) {
		t.Fatalf("decision = %+v", got)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	if last := args[len(args)-1]; last != PickPrompt(hostile.Origin) {
		t.Errorf("the prompt is not the last argument: %q", last)
	}
	for _, line := range scripts(args[:len(args)-1]) {
		if strings.Contains(line, "shell script") || strings.Contains(line, hostile.Origin) {
			t.Errorf("the origin reached the script: %q", line)
		}
	}
}

func TestDecidePickerThatGivesNoFolderIsADeny(t *testing.T) {
	for name, picker := range map[string]string{
		"cancelled":    "exit 1",
		"empty choice": "exit 0",
		"not a path":   "echo not-a-path",
	} {
		dir := t.TempDir()
		script(t, dir, "zenity", `case "$*" in *--file-selection*) `+picker+`;; *) exit 0;; esac`)
		if got := decideOn("linux", q, home.Env{"PATH": dir}, TimeoutS); got.Answer != AnswerDeny || got.Folders != nil {
			t.Errorf("%s: decision = %+v", name, got)
		}
	}
}

func TestDecideDeniedDialogNeverOpensThePicker(t *testing.T) {
	dir := t.TempDir()
	opened := filepath.Join(dir, "opened")
	script(t, dir, "zenity", `case "$*" in *--file-selection*) touch `+opened+`; echo /tmp;; *) exit 1;; esac`)
	if got := decideOn("linux", q, home.Env{"PATH": dir}, TimeoutS); got.Answer != AnswerDeny {
		t.Fatalf("decision = %+v", got)
	}
	if _, err := os.Stat(opened); err == nil {
		t.Error("the picker opened after a Deny")
	}
}

func TestDecideWithoutAPickerToolIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "osascript", "echo 'Allow,false'")
	// osascript is on the PATH for the dialog, but its folder picker needs it too: simulate a platform that cannot pick.
	if got := pickOn("windows", "https://app.example.com", home.Env{"PATH": dir}, TimeoutS); got.Answer != AnswerUnavailable {
		t.Fatalf("decision = %+v", got)
	}
}

func TestPickPlanPerPlatform(t *testing.T) {
	found := func(cmds ...string) func(string) bool { return func(c string) bool { return slices.Contains(cmds, c) } }
	if p, ok := PickPlan("linux", found("zenity", "kdialog"), "p"); !ok || p.File != "zenity" || !slices.Contains(p.Args, "--multiple") {
		t.Errorf("zenity plan = %+v, %v", p, ok)
	}
	if p, ok := PickPlan("linux", found("kdialog"), "p"); !ok || p.File != "kdialog" {
		t.Errorf("kdialog plan = %+v, %v", p, ok)
	}
	if _, ok := PickPlan("linux", found(), "p"); ok {
		t.Error("a plan without a tool")
	}
	if _, ok := PickPlan("windows", found("zenity"), "p"); ok {
		t.Error("a plan for windows")
	}
}
