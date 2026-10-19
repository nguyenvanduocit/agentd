package consent

import (
	"slices"
	"strings"
	"testing"
)

const origin = "https://app.example.com"

var (
	serverText  = ConsentText(Question{Origin: origin, Mode: ModeServer, Agents: []string{"claude", "codex"}})
	browserText = ConsentText(Question{Origin: origin, Mode: ModeBrowser, Agents: []string{"claude", "codex"}})
)

func has(found ...string) func(string) bool {
	return func(cmd string) bool { return slices.Contains(found, cmd) }
}

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%q does not contain %q", s, p)
		}
	}
}

func TestConsentTextNamesTheOriginAndTheAgents(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Title, origin)
		mustContain(t, text.Body, origin, "claude, codex")
	}
}

func TestConsentTextSaysAllowIsFullAccess(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "run as you", "any of your files", "run any program")
	}
}

func TestConsentTextServerKeepsAccessWithNoPageOpen(t *testing.T) {
	mustContain(t, serverText.Body, "at any time, even when none of its pages is open")
	mustContain(t, browserText.Body, "whenever one of its pages is open")
	if strings.Contains(browserText.Body, "at any time") {
		t.Error("a browser grant is only while a page is open")
	}
}

func TestConsentTextSaysHowToTakeTheAccessBack(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "menu bar", "agentd apps remove "+origin)
	}
}

func TestConsentTextSaysWhenNoAgentIsReady(t *testing.T) {
	mustContain(t, ConsentText(Question{Origin: origin, Mode: ModeServer}).Body, "no agent is ready yet")
}

// scripts are the arguments that follow a -e.
func scripts(args []string) []string {
	var out []string
	for i, a := range args {
		if i > 0 && args[i-1] == "-e" {
			out = append(out, a)
		}
	}
	return out
}

func TestPlanMacOSOsascriptTheTextOnlyAsArguments(t *testing.T) {
	plan, ok := Plan("darwin", has("osascript"), serverText, 60)
	if !ok || plan.File != "osascript" || plan.Kind != KindOsascript {
		t.Fatalf("plan = %+v, %v", plan, ok)
	}
	s := scripts(plan.Args)
	if len(s) == 0 {
		t.Fatal("no script")
	}
	for _, line := range s {
		if strings.Contains(line, origin) {
			t.Errorf("script line contains the origin: %q", line)
		}
	}
	mustContain(t, strings.Join(s, "\n"), "giving up after 60", `default button "Deny"`)
	if got := plan.Args[len(plan.Args)-2:]; got[0] != serverText.Title || got[1] != serverText.Body {
		t.Errorf("trailing args = %q", got)
	}
}

// A quote, a backslash or AppleScript in an origin changes no line of the script.
func TestPlanAnOriginCannotAlterTheScript(t *testing.T) {
	hostile := `https://x" & (do shell script "touch /tmp/pwned") & "\` + "\n" + `end run`
	benign, _ := Plan("darwin", has("osascript"), serverText, 60)
	evil, ok := Plan("darwin", has("osascript"), ConsentText(Question{Origin: hostile, Mode: ModeServer}), 60)
	if !ok {
		t.Fatal("no plan")
	}
	if !slices.Equal(scripts(benign.Args), scripts(evil.Args)) {
		t.Errorf("scripts differ:\n%q\n%q", scripts(benign.Args), scripts(evil.Args))
	}
	if len(evil.Args) != len(benign.Args) {
		t.Errorf("argument count differs: %d vs %d", len(evil.Args), len(benign.Args))
	}
	if got := evil.Args[len(evil.Args)-2]; !strings.Contains(got, hostile) {
		t.Errorf("the title does not carry the origin as it is: %q", got)
	}
}

func TestPlanLinuxZenityThenKdialogElseNothing(t *testing.T) {
	z, ok := Plan("linux", has("zenity", "kdialog"), serverText, 60)
	if !ok || z.File != "zenity" {
		t.Fatalf("zenity plan = %+v", z)
	}
	if !slices.Contains(z.Args, "--no-markup") || !slices.Contains(z.Args, "--timeout=60") {
		t.Errorf("zenity args = %q", z.Args)
	}
	if k, ok := Plan("linux", has("kdialog"), serverText, 60); !ok || k.File != "kdialog" {
		t.Errorf("kdialog plan = %+v", k)
	}
	if _, ok := Plan("linux", has(), serverText, 60); ok {
		t.Error("a plan without a tool")
	}
	if _, ok := Plan("windows", has("zenity"), serverText, 60); ok {
		t.Error("a plan on Windows")
	}
	if _, ok := Plan("darwin", has("zenity"), serverText, 60); ok {
		t.Error("a zenity plan on macOS")
	}
}

func TestAnswerOf(t *testing.T) {
	cases := []struct {
		kind   DialogKind
		code   int
		stdout string
		want   Answer
	}{
		{KindOsascript, 0, "Allow,false\n", AnswerAllow},
		{KindOsascript, 0, "Deny,false\n", AnswerDeny},
		{KindOsascript, 0, ",true\n", AnswerTimeout},
		{KindOsascript, 1, "", AnswerDeny},
		{KindZenity, 0, "", AnswerAllow},
		{KindZenity, 1, "", AnswerDeny},
		{KindZenity, 5, "", AnswerTimeout},
		{KindZenity, 255, "", AnswerDeny},
		{KindKdialog, 0, "", AnswerAllow},
		{KindKdialog, 1, "", AnswerDeny},
	}
	for _, c := range cases {
		if got := AnswerOf(c.kind, c.code, c.stdout); got != c.want {
			t.Errorf("AnswerOf(%s, %d, %q) = %s, want %s", c.kind, c.code, c.stdout, got, c.want)
		}
	}
}
