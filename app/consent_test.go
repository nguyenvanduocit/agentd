package main

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestParseConsentReadsAQuestionAndNothingElse(t *testing.T) {
	line := `{"t":"consent","id":"abc","origin":"https://app.example.com","mode":"browser","agents":["claude","grok"],"title":"Let https://app.example.com run agents on this computer?","body":"b","timeoutS":300}`
	got, ok := ParseConsent([]byte(line))
	want := ConsentRequest{T: "consent", ID: "abc", Origin: "https://app.example.com", Mode: "browser", Agents: []string{"claude", "grok"}, Title: "Let https://app.example.com run agents on this computer?", Body: "b", TimeoutS: 300}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, ok)
	}
	for _, bad := range []string{`{"t":"state","version":"1"}`, `{"t":"consent"}`, `{"t":"consent","id":""}`, `{"t":"consent-end","id":"abc"}`, `not json`} {
		if _, ok := ParseConsent([]byte(bad)); ok {
			t.Errorf("%s parsed as a question", bad)
		}
	}
}

func TestParseConsentEndReadsTheWithdrawalOfAQuestion(t *testing.T) {
	if id, ok := ParseConsentEnd([]byte(`{"t":"consent-end","id":"abc"}`)); !ok || id != "abc" {
		t.Fatalf("got %q, %v", id, ok)
	}
	for _, bad := range []string{`{"t":"consent","id":"abc"}`, `{"t":"consent-end"}`, `{"t":"consent-end","id":""}`, `[]`} {
		if _, ok := ParseConsentEnd([]byte(bad)); ok {
			t.Errorf("%s parsed as a withdrawal", bad)
		}
	}
}

func TestTheLinesTheWindowSaysAreWhatTheDaemonReads(t *testing.T) {
	if got := string(ShownLine("abc")); got != `{"t":"consent-shown","id":"abc"}` {
		t.Errorf("shown = %s", got)
	}
	if got := string(AnswerLine("abc", true, []string{"/work/a", "/work/b"})); got != `{"t":"consent-answer","id":"abc","answer":"allow","folders":["/work/a","/work/b"]}` {
		t.Errorf("allow = %s", got)
	}
	if got := string(AnswerLine("abc", false, nil)); got != `{"t":"consent-answer","id":"abc","answer":"deny","folders":[]}` {
		t.Errorf("deny = %s", got)
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
		Request: ConsentRequest{T: "consent", ID: "abc", Title: "Let https://app.example.com run agents on this computer?", Body: "The app wants to connect."},
		Folders: folders,
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
