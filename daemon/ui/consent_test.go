package ui

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

var question = consent.Question{Origin: "https://app.example.com", Mode: consent.ModeBrowser, Agents: []string{"claude", "grok"}, Titles: []string{"Claude Code", "Grok"}}

// uiEnd is the UI at the other end of a relay: it records the lines it is sent and answers through the relay.
type uiEnd struct {
	mu    sync.Mutex
	lines []string
	up    bool
	relay *Relay
	react func(u *uiEnd, line string)
}

func newUI(react func(u *uiEnd, line string)) *uiEnd {
	u := &uiEnd{up: true, react: react}
	u.relay = NewRelay(func(line string) bool {
		u.mu.Lock()
		if !u.up {
			u.mu.Unlock()
			return false
		}
		u.lines = append(u.lines, line)
		u.mu.Unlock()
		if u.react != nil {
			go u.react(u, line)
		}
		return true
	})
	u.relay.ackWait = 200 * time.Millisecond
	return u
}

func (u *uiEnd) sent() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.lines...)
}

func requestOf(t *testing.T, line string) consentRequest {
	t.Helper()
	var r consentRequest
	if json.Unmarshal([]byte(line), &r) != nil || r.T != "consent" {
		t.Fatalf("not a consent request: %s", line)
	}
	return r
}

// idOf is the id of a request line; for the UI's goroutine, which has no *testing.T.
func idOf(line string) string {
	var r consentRequest
	_ = json.Unmarshal([]byte(line), &r)
	return r.ID
}

// answering is a UI that shows its window at once and answers with answer and folders.
func answering(answer string, folders ...string) func(*uiEnd, string) {
	return func(u *uiEnd, line string) {
		if !strings.Contains(line, `"t":"consent"`) {
			return
		}
		id := idOf(line)
		u.relay.Deliver(Reply{Kind: replyShown, ID: id})
		u.relay.Deliver(Reply{Kind: replyAnswer, ID: id, Answer: answer, Folders: folders})
	}
}

func TestAskShowsTheQuestionAndReturnsTheFoldersTheUserChose(t *testing.T) {
	ui := newUI(answering("allow", "/work/a/", "/work/b", "/work/a", "relative"))
	got, asked := ui.relay.Ask(question, 5)
	if !asked || got.Answer != consent.AnswerAllow || strings.Join(got.Folders, ",") != "/work/a,/work/b" {
		t.Fatalf("decision = %+v, asked = %v; want Allow in the two clean absolute folders", got, asked)
	}
	req := requestOf(t, ui.sent()[0])
	text := consent.ConsentText(question)
	if req.Origin != question.Origin || req.Mode != "browser" || strings.Join(req.Agents, ",") != "claude,grok" || req.Title != text.Title || req.Body != text.Body || req.TimeoutS != 5 || len(req.ID) != 32 {
		t.Errorf("request = %+v", req)
	}
	if !strings.Contains(req.Body, "Claude Code and Grok") || strings.Contains(req.Body, "agentd apps remove") {
		t.Errorf("the window's body = %q; want the agents as a user reads them and no terminal command", req.Body)
	}
}

func TestAskAnswersThatAreNotAnAllowWithAFolderAreADeny(t *testing.T) {
	for name, react := range map[string]func(*uiEnd, string){
		"deny":                  answering("deny", "/work"),
		"allow with no folder":  answering("allow"),
		"allow, only relatives": answering("allow", "work", "./x"),
		"something else":        answering("yes please", "/work"),
	} {
		got, asked := newUI(react).relay.Ask(question, 5)
		if !asked || got.Answer != consent.AnswerDeny || got.Folders != nil {
			t.Errorf("%s: decision = %+v, asked = %v", name, got, asked)
		}
	}
}

func TestAskWithNoUiIsNotAsked(t *testing.T) {
	r := NewRelay(func(string) bool { return false })
	if got, asked := r.Ask(question, 5); asked || got.Answer != "" {
		t.Fatalf("decision = %+v, asked = %v", got, asked)
	}
}

func TestAskOfAUiThatDoesNotShowTheWindowIsNotAskedAndTheRequestIsWithdrawn(t *testing.T) {
	ui := newUI(nil) // reads the request, shows nothing: a UI that does not know "consent"
	if _, asked := ui.relay.Ask(question, 5); asked {
		t.Fatal("asked")
	}
	lines := ui.sent()
	if len(lines) != 2 || !strings.Contains(lines[1], `"t":"consent-end"`) || !strings.Contains(lines[1], requestOf(t, lines[0]).ID) {
		t.Fatalf("lines = %v; want the request, then its end", lines)
	}
}

func TestAskNoAnswerInTimeIsATimeoutAndTheWindowIsClosed(t *testing.T) {
	ui := newUI(func(u *uiEnd, line string) {
		if strings.Contains(line, `"t":"consent"`) {
			u.relay.Deliver(Reply{Kind: replyShown, ID: idOf(line)})
		}
	})
	got, asked := ui.relay.Ask(question, 1)
	if !asked || got.Answer != consent.AnswerTimeout {
		t.Fatalf("decision = %+v, asked = %v", got, asked)
	}
	if lines := ui.sent(); !strings.Contains(lines[len(lines)-1], `"t":"consent-end"`) {
		t.Fatalf("lines = %v; the window should be told to close", lines)
	}
}

func TestAskOfAUiThatGoesAwayIsNotAsked(t *testing.T) {
	ui := newUI(func(u *uiEnd, line string) {
		if strings.Contains(line, `"t":"consent"`) {
			u.relay.Deliver(Reply{Kind: replyShown, ID: idOf(line)})
			time.Sleep(50 * time.Millisecond)
			u.relay.Drop()
		}
	})
	start := time.Now()
	if _, asked := ui.relay.Ask(question, 30); asked {
		t.Fatal("asked")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the wait outlived the channel")
	}
}

func TestAReplyThatDoesNotCarryTheRequestsIdIsNotHeard(t *testing.T) {
	ui := newUI(func(u *uiEnd, line string) {
		if strings.Contains(line, `"t":"consent"`) {
			id := idOf(line)
			u.relay.Deliver(Reply{Kind: replyShown, ID: id})
			u.relay.Deliver(Reply{Kind: replyAnswer, ID: "guessed", Answer: "allow", Folders: []string{"/etc"}})
		}
	})
	if got, _ := ui.relay.Ask(question, 1); got.Answer != consent.AnswerTimeout {
		t.Fatalf("decision = %+v; an answer with another id decided it", got)
	}
}

func TestParseReply(t *testing.T) {
	good := map[string]Reply{
		`{"t":"consent-shown","id":"a"}`:                                         {Kind: replyShown, ID: "a"},
		`{"t":"consent-answer","id":"a","answer":"allow","folders":["/x","/y"]}`: {Kind: replyAnswer, ID: "a", Answer: "allow", Folders: []string{"/x", "/y"}},
		`{"t":"consent-answer","id":"a","answer":"deny"}`:                        {Kind: replyAnswer, ID: "a", Answer: "deny"},
	}
	for line, want := range good {
		got, ok := ParseReply(line)
		if !ok || got.Kind != want.Kind || got.ID != want.ID || got.Answer != want.Answer || strings.Join(got.Folders, ",") != strings.Join(want.Folders, ",") {
			t.Errorf("%s: got %+v, %v", line, got, ok)
		}
	}
	for _, line := range []string{`{"t":"action","id":"logs"}`, `{"t":"consent-answer"}`, `{"t":"consent-shown","id":""}`, `not json`, `{"t":"state"}`} {
		if _, ok := ParseReply(line); ok {
			t.Errorf("%s parsed", line)
		}
	}
}

// The relay over a real link: the app's end of an AppLink shows the window and answers.
func TestAppLinkAsksThroughTheChannelAndHearsTheAnswer(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	l := NewAppLink(conn, func(Action) {}, func() {}, log)
	l.Start()
	go func() {
		for i := 0; i < 100; i++ {
			for _, line := range app.got.all() {
				if strings.Contains(line, `"t":"consent"`) {
					id := idOf(line)
					app.conn.Write([]byte(`{"t":"consent-shown","id":"` + id + `"}` + "\n"))
					app.conn.Write([]byte(`{"t":"consent-answer","id":"` + id + `","answer":"allow","folders":["/work"]}` + "\n"))
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	got, asked := l.Ask(question, 5)
	if !asked || got.Answer != consent.AnswerAllow || len(got.Folders) != 1 || got.Folders[0] != "/work" {
		t.Fatalf("decision = %+v, asked = %v", got, asked)
	}
}

func TestAppLinkThatClosesItsChannelEndsTheAsk(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	l := NewAppLink(conn, func(Action) {}, func() {}, log)
	l.Start()
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = app.conn.Close()
	}()
	if _, asked := l.Ask(question, 30); asked {
		t.Fatal("asked")
	}
}
