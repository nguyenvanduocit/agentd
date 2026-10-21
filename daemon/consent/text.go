// Package consent is what the daemon asks the user before it lets an origin run agents: the words of the question, its
// answer and the folders chosen, and the check that a connecting process belongs to the daemon's own OS user.
//
// The cooldown after a refusal and the lock after three refusals are the gateway's rules (core/local.ts), not
// this package's.
package consent

import (
	"fmt"
	"strings"
	"time"
)

// Mode is what an Allow gives.
type Mode string

const (
	// ModeServer gives the app's server a lasting token.
	ModeServer Mode = "server"
	// ModeBrowser lets the origin's pages run agents themselves.
	ModeBrowser Mode = "browser"
)

// NoEnd is a Lasts that never ends.
const NoEnd time.Duration = -1

// DefaultLasts is how long an Allow lasts when the answer chose no time: the grants of a web page before there was a
// choice lasted this long.
const DefaultLasts = 30 * 24 * time.Hour

// Lasting is one way an Allow can last: the words of the choice and its length.
type Lasting struct {
	Label string
	Lasts time.Duration
}

// LastsChoices are the times the consent window offers, shortest first.
var LastsChoices = []Lasting{
	{"1 hour", time.Hour},
	{"1 day", 24 * time.Hour},
	{"7 days", 7 * 24 * time.Hour},
	{"30 days", DefaultLasts},
	{"Never", NoEnd},
}

// DefaultLastsChoice is the index in LastsChoices that is chosen when the window opens.
const DefaultLastsChoice = 3

// Question is what the daemon asks the user before it lets an origin run agents.
type Question struct {
	// Origin is the page's origin, from the browser's Origin header.
	Origin string
	// Name is what the app calls itself. The app chooses it, so the question always shows the origin beside it.
	Name string
	Mode Mode
	// Agents are the agents that are ready now, by name.
	Agents []string
	// Titles are the same agents as a user reads them ("Claude Code"): the words of the question. Agents is what the
	// consent window's line carries; Titles never leave the daemon but in the text.
	Titles []string
}

// Text is the question's title and body.
type Text struct {
	Title string
	Body  string
}

// Answer is the outcome of asking.
type Answer string

const (
	AnswerAllow Answer = "allow"
	AnswerDeny  Answer = "deny"
	// AnswerTimeout is no answer in time; it counts as a Deny.
	AnswerTimeout Answer = "timeout"
	// AnswerUnavailable means nothing can ask (no app shows the question); it is never consent.
	AnswerUnavailable Answer = "unavailable"
)

// ConsentText is the question of the consent window, in the words of the user. It says who asks and when they can
// start agents, that the folders chosen are where agents start and no more (agents run as the user, so they can read
// and change any file and run any program), that the user also chooses how long the access lasts, and where to take it
// back. Whoever drives a session (the app's server, or the site's pages) chooses its folder, its MCP servers and its
// answers to permission requests; nothing on this machine narrows that. The two modes differ in when that access can be
// used.
func ConsentText(q Question) Text {
	agents, none := "agents", " No agent is ready yet."
	if len(q.Titles) > 0 {
		agents, none = joinTitles(q.Titles), ""
	}
	title := "Let " + q.Origin + " run agents on this computer?"
	if q.Name != "" {
		title = "Let " + q.Name + " (" + q.Origin + ") run agents on this computer?"
	}
	var who, when string
	if q.Mode == ModeServer {
		app := "The app at " + q.Origin
		if q.Name != "" {
			app = fmt.Sprintf("The app %q at %s", q.Name, q.Origin)
		}
		who = app + " wants to connect to agentd on this computer."
		when = fmt.Sprintf("If you allow it, the app can start %s here at any time, even when none of its pages is open.%s", agents, none)
	} else {
		pages := "The pages of " + q.Origin
		if q.Name != "" {
			pages = fmt.Sprintf("The pages of %s, which calls itself %q,", q.Origin, q.Name)
		}
		who = pages + " want to run agents on this computer."
		when = fmt.Sprintf("If you allow it, its pages can start %s here whenever one of its pages is open.%s", agents, none)
	}
	return Text{
		Title: title,
		Body: who + "\n\n" + when + "\n\n" +
			"You choose the folders it may start agents in. That is only where they start: agents run as you, so they can read and change any of your files, including files outside those folders, and run any program.\n\n" +
			"You also choose how long the access lasts; then the app has to ask again. You can disconnect it sooner, from the agentd icon in the menu bar.\n\n" +
			"Only allow sites you trust.",
	}
}

// joinTitles is a list in English: "A", "A and B", "A, B and C".
func joinTitles(titles []string) string {
	if len(titles) == 1 {
		return titles[0]
	}
	return strings.Join(titles[:len(titles)-1], ", ") + " and " + titles[len(titles)-1]
}
