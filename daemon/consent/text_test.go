package consent

import (
	"strings"
	"testing"
	"time"
)

const origin = "https://app.example.com"

var (
	agentTitles = []string{"Claude Code", "Codex"}
	serverText  = ConsentText(Question{Origin: origin, Mode: ModeServer, Agents: []string{"claude", "codex"}, Titles: agentTitles})
	browserText = ConsentText(Question{Origin: origin, Mode: ModeBrowser, Agents: []string{"claude", "codex"}, Titles: agentTitles})
)

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%q does not contain %q", s, p)
		}
	}
}

func TestConsentTextNamesTheOriginAndTheAgentsAsAUserReadsThem(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Title, origin)
		mustContain(t, text.Body, origin, "Claude Code and Codex")
		if strings.Contains(text.Body, "claude") || strings.Contains(text.Body, "codex") {
			t.Errorf("the body shows an internal agent name: %q", text.Body)
		}
	}
}

func TestConsentTextListsTheAgentsInPlainEnglish(t *testing.T) {
	for titles, want := range map[string]string{
		"":                         "No agent is ready yet.",
		"Claude Code":              "start Claude Code here",
		"Claude Code|Codex":        "start Claude Code and Codex here",
		"Claude Code|Codex|Grok":   "start Claude Code, Codex and Grok here",
		"Claude Code|Codex|Grok|Q": "start Claude Code, Codex, Grok and Q here",
	} {
		var list []string
		if titles != "" {
			list = strings.Split(titles, "|")
		}
		mustContain(t, ConsentText(Question{Origin: origin, Mode: ModeServer, Titles: list}).Body, want)
	}
}

func TestConsentTextSaysAllowIsFullAccess(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "run as you", "any of your files", "outside those folders", "run any program")
	}
}

func TestConsentTextSaysTheFoldersOnlyAreWhereAgentsStart(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "You choose the folders it may start agents in.", "only where they start")
		if strings.Contains(text.Body, "Next you choose") || strings.Contains(text.Body, "keeps the app to those folders") {
			t.Errorf("the body still promises more than the folders give: %q", text.Body)
		}
	}
}

func TestConsentTextServerKeepsAccessWithNoPageOpen(t *testing.T) {
	mustContain(t, serverText.Body, "at any time, even when none of its pages is open")
	mustContain(t, browserText.Body, "whenever one of its pages is open")
	if strings.Contains(browserText.Body, "at any time") {
		t.Error("a browser grant is only while a page is open")
	}
}

func TestConsentTextUsesTheWordsOfTheProduct(t *testing.T) {
	for _, text := range []Text{serverText, browserText, ConsentText(Question{Origin: origin, Mode: ModeServer})} {
		for _, word := range []string{"daemon", "engine", "origin", "server"} {
			if strings.Contains(strings.ToLower(text.Title+" "+text.Body), word) {
				t.Errorf("%q is a word of the code, not of the user: %q", word, text.Body)
			}
		}
	}
}

// Whether the access lasts is the user's choice, made in the same window: the text points at it.
func TestConsentTextSaysTheUserChoosesHowLongTheAccessLasts(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "You also choose how long the access lasts", "disconnect it sooner")
		if strings.Contains(text.Body, "until you disconnect it") || strings.Contains(text.Body, "30 days") {
			t.Errorf("the text decides a time the user has not chosen: %q", text.Body)
		}
	}
}

// A name is whatever the app calls itself, so the title carries the origin beside it: it is the part a site cannot choose.
func TestConsentTextNamesTheAppAndKeepsItsOriginInTheTitle(t *testing.T) {
	named := ConsentText(Question{Origin: origin, Mode: ModeServer, Name: "AIOcean"})
	mustContain(t, named.Title, "AIOcean", origin)
	mustContain(t, named.Body, "AIOcean", origin)
	mustContain(t, ConsentText(Question{Origin: origin, Mode: ModeBrowser, Name: "Docs"}).Body, "Docs", origin)
	if unnamed := ConsentText(Question{Origin: origin, Mode: ModeServer}); strings.Contains(unnamed.Title, "()") || !strings.Contains(unnamed.Title, origin) {
		t.Errorf("a question without a name still names the origin: %q", unnamed.Title)
	}
}

func TestTheChoicesOfHowLongAccessLastsEndWithNever(t *testing.T) {
	var labels []string
	for _, c := range LastsChoices {
		labels = append(labels, c.Label)
	}
	want := []string{"1 hour", "1 day", "7 days", "30 days", "Never"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("choices %v, want %v", labels, want)
	}
	if LastsChoices[len(LastsChoices)-1].Lasts != NoEnd {
		t.Error("the last choice has no end")
	}
	if got := LastsChoices[DefaultLastsChoice]; got.Lasts != DefaultLasts || got.Label != "30 days" {
		t.Errorf("the default choice is %+v, want 30 days", got)
	}
}

func TestADecisionThatChoseNoTimeLastsTheDefault(t *testing.T) {
	for _, tc := range []struct{ lasts, want time.Duration }{{0, DefaultLasts}, {time.Hour, time.Hour}, {NoEnd, NoEnd}} {
		if got := (Decision{Lasts: tc.lasts}).LastsOrDefault(); got != tc.want {
			t.Errorf("Lasts %v gives %v, want %v", tc.lasts, got, tc.want)
		}
	}
}

func TestConsentTextSaysHowToTakeTheAccessBack(t *testing.T) {
	for _, text := range []Text{serverText, browserText} {
		mustContain(t, text.Body, "agentd icon in the menu bar", "Only allow sites you trust.")
		if strings.Contains(text.Body, "agentd apps remove") {
			t.Errorf("the window's words carry a terminal command: %q", text.Body)
		}
	}
}
