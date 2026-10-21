package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

func TestTheWindowShowsTheDaemonsWordsForTheQuestion(t *testing.T) {
	q := consent.Question{Origin: "https://app.example.com", Mode: consent.ModeBrowser, Agents: []string{"claude"}, Titles: []string{"Claude Code"}}
	req := RequestOf(q, 300)
	text := consent.ConsentText(q)
	if req.Title != text.Title || req.Body != text.Body || req.TimeoutS != 300 {
		t.Fatalf("got %+v, want the daemon's words %+v and 300 s", req, text)
	}
	if !strings.Contains(req.Title, "https://app.example.com") {
		t.Errorf("the title does not name the origin it exactly as it came: %q", req.Title)
	}
}

func TestChoosingFolders(t *testing.T) {
	c := &Consent{}
	if c.CanAllow() {
		t.Fatal("Allow without a folder")
	}
	c.Add([]string{"/work/a/", "relative", "/work/b", "/work/a", ""})
	if want := []string{"/work/a", "/work/b"}; !reflect.DeepEqual(c.Folders, want) || !c.CanAllow() {
		t.Fatalf("folders = %v; want %v: absolute, clean, each once, in the order chosen", c.Folders, want)
	}
	c.Add([]string{"/work/c"})
	c.Remove(0)
	c.Remove(7) // no such folder: nothing
	c.Remove(-1)
	if want := []string{"/work/b", "/work/c"}; !reflect.DeepEqual(c.Folders, want) {
		t.Fatalf("folders = %v; want %v", c.Folders, want)
	}
	c.Remove(0)
	c.Remove(0)
	if c.CanAllow() {
		t.Fatal("Allow after every folder was removed")
	}
}

// window is the consent window of one question in a headless tester; answers collects what it says to the daemon.
func window(folders ...string) (*ui.Tester, *[]bool) {
	var answers []bool
	c := &Consent{
		Request: ConsentRequest{Title: "Let https://app.example.com run agents on this computer?", Body: "The app wants to connect."},
		Folders: folders,
		Lasts:   consent.DefaultLastsChoice,
		answer:  func(allow bool) { answers = append(answers, allow) },
	}
	return ui.NewTester(c.View, 480, 620), &answers
}

func TestEscapeDeniesWhateverIsChosen(t *testing.T) {
	for _, folders := range [][]string{nil, {"/work/a"}} {
		tt, answers := window(folders...)
		tt.Key(0, ui.KeyEscape)
		if !reflect.DeepEqual(*answers, []bool{false}) {
			t.Errorf("folders %v: Escape answered %v; want one Deny", folders, *answers)
		}
	}
}

func TestReturnPressesDenyNeverAllow(t *testing.T) {
	tt, answers := window("/work/a")
	tt.Key(0, ui.KeyEnter)
	if !reflect.DeepEqual(*answers, []bool{false}) {
		t.Errorf("Return answered %v; want one Deny: Deny is the default button", *answers)
	}
}

func TestAllowNeedsAFolderAndTheButtonsAnswer(t *testing.T) {
	tt, answers := window()
	_ = tt.Click("Allow")
	if len(*answers) != 0 {
		t.Fatalf("Allow answered %v with no folder", *answers)
	}
	tt, answers = window("/work/a")
	if err := tt.Click("Allow"); err != nil || !reflect.DeepEqual(*answers, []bool{true}) {
		t.Errorf("Allow with a folder: err %v, answers %v; want one Allow", err, *answers)
	}
	tt, answers = window("/work/a")
	if err := tt.Click("Deny"); err != nil || !reflect.DeepEqual(*answers, []bool{false}) {
		t.Errorf("Deny: err %v, answers %v; want one Deny", err, *answers)
	}
}

func TestTheWindowSaysWhatNoAnswerCountsAs(t *testing.T) {
	for seconds, want := range map[int]string{300: "No answer within 5 min counts as Deny.", 90: "No answer within 90 s counts as Deny.", 0: ""} {
		if got := timeoutLine(seconds); got != want {
			t.Errorf("timeoutLine(%d) = %q, want %q", seconds, got, want)
		}
	}
	c := &Consent{Request: ConsentRequest{Title: "t", Body: "b", TimeoutS: 300}, answer: func(bool) {}}
	tt := ui.NewTester(c.View, 480, 620)
	if !tt.HasText("No answer within 5 min counts as Deny.") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestTheWindowSaysWhyAllowIsOffAndWhatTheFoldersMean(t *testing.T) {
	tt, _ := window()
	for _, want := range []string{"Where agents start", "Agents start in the first folder. They can still reach files outside it.", "Add a folder to turn on Allow."} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	tt, _ = window("/work/a")
	if tt.HasText("Add a folder to turn on Allow.") {
		t.Errorf("the hint stays once a folder is chosen: %q", tt.Texts())
	}
}

func TestTheWindowOffersHowLongTheAccessLastsAndStartsOn30Days(t *testing.T) {
	var c *Consent
	c = newConsent(ConsentRequest{Title: "t", Body: "b"}, make(chan consent.Decision, 1))
	c.answer = func(bool) {}
	tt := ui.NewTester(c.View, 480, 700)
	for _, want := range []string{"How long", "1 hour", "1 day", "7 days", "30 days", "Never"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if c.LastsFor() != 30*24*time.Hour {
		t.Errorf("the window opens on %v, want 30 days", c.LastsFor())
	}
	if err := tt.Click("Never"); err != nil || c.LastsFor() != consent.NoEnd {
		t.Errorf("after Never: err %v, lasts %v", err, c.LastsFor())
	}
	if err := tt.Click("1 hour"); err != nil || c.LastsFor() != time.Hour {
		t.Errorf("after 1 hour: err %v, lasts %v", err, c.LastsFor())
	}
}

func TestTheWindowSaysWhenTheAccessEnds(t *testing.T) {
	now := time.Date(2026, 10, 21, 20, 40, 0, 0, time.Local)
	if got, want := endLine(24*time.Hour, now), "The access ends Thu 22 Oct, 20:40."; got != want {
		t.Errorf("a day: %q, want %q", got, want)
	}
	if got := endLine(consent.NoEnd, now); !strings.Contains(got, "no end") {
		t.Errorf("never: %q", got)
	}
}
