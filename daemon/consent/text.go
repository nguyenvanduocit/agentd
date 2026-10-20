// Package consent is what the daemon asks the user before it lets an origin run agents: the dialog's text, the native
// dialog tool and its answer, and the check that a connecting process belongs to the daemon's own OS user.
//
// The cooldown after a refusal and the lock after three refusals are the gateway's rules (core/local.ts), not
// this package's.
package consent

import (
	"fmt"
	"strings"
)

// Mode is what an Allow gives.
type Mode string

const (
	// ModeServer gives the app's server a lasting token.
	ModeServer Mode = "server"
	// ModeBrowser lets the origin's pages run agents themselves.
	ModeBrowser Mode = "browser"
)

// Question is what the daemon asks the user before it lets an origin run agents.
type Question struct {
	// Origin is the page's origin, from the browser's Origin header.
	Origin string
	Mode   Mode
	// Agents are the agents that are ready now, by name.
	Agents []string
	// Titles are the same agents as a user reads them ("Claude Code"): the words of the question. Agents is what the
	// consent window's line carries; Titles never leave the daemon but in the text.
	Titles []string
}

// Text is the dialog's title and body.
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
	// AnswerUnavailable means the machine has no way to ask (no tool, no graphical session); it is never consent.
	AnswerUnavailable Answer = "unavailable"
)

// ConsentText is the question of the consent window, in the words of the user. It says who asks and when they can
// start agents, that the folders chosen are where agents start and no more (agents run as the user, so they can read
// and change any file and run any program), and where to take the access back. Whoever drives a session (the app's
// server, or the site's pages) chooses its folder, its MCP servers and its answers to permission requests; nothing on
// this machine narrows that. The two modes differ in when that access can be used.
func ConsentText(q Question) Text { return text(q, false) }

// DialogText is ConsentText for the native dialogs, which also name the terminal command that disconnects: a machine
// that has to fall back to them may have no agentd icon in its menu bar.
func DialogText(q Question) Text { return text(q, true) }

func text(q Question, terminal bool) Text {
	agents, none := "agents", " No agent is ready yet."
	if len(q.Titles) > 0 {
		agents, none = joinTitles(q.Titles), ""
	}
	var who, when string
	if q.Mode == ModeServer {
		who = fmt.Sprintf("The app at %s wants to connect to agentd on this computer.", q.Origin)
		when = fmt.Sprintf("If you allow it, the app can start %s here at any time, even when none of its pages is open.%s", agents, none)
	} else {
		who = fmt.Sprintf("The pages of %s want to run agents on this computer.", q.Origin)
		when = fmt.Sprintf("If you allow it, its pages can start %s here whenever one of its pages is open.%s", agents, none)
	}
	disconnect := "You can disconnect it whenever you want, from the agentd icon in the menu bar."
	if terminal {
		disconnect = "You can disconnect it whenever you want, from the agentd icon in the menu bar or in a terminal with: agentd apps remove " + q.Origin
	}
	return Text{
		Title: "Let " + q.Origin + " run agents on this computer?",
		Body: who + "\n\n" + when + "\n\n" +
			"You choose the folders it may start agents in. That is only where they start: agents run as you, so they can read and change any of your files, including files outside those folders, and run any program.\n\n" +
			disconnect + "\n\n" +
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

// DialogKind is the tool that shows the dialog.
type DialogKind string

const (
	KindOsascript DialogKind = "osascript"
	KindZenity    DialogKind = "zenity"
	KindKdialog   DialogKind = "kdialog"
)

// DialogPlan is the command that shows the dialog.
type DialogPlan struct {
	Kind DialogKind
	File string
	Args []string
}

// The text travels as arguments (`argv` for osascript), never inside the script: an origin cannot change the script.
var osascript = []string{
	"on run argv",
	`set r to display dialog (item 2 of argv) with title (item 1 of argv) buttons {"Deny", "Allow"} default button "Deny" cancel button "Deny" with icon caution giving up after TIMEOUT`,
	`return (button returned of r) & "," & (gave up of r)`,
	"end run",
}

// Plan is the command that shows the dialog on `platform` (a GOOS value), or false when the machine has no tool to
// show one. has reports whether a program is on the PATH.
//
// Deny is the button that has the focus when the dialog opens, so a stray Return denies: osascript says so with
// `default button "Deny"`, zenity with `--default-cancel`. kdialog documents no option that does it, so there the
// toolkit decides which button has the focus.
func Plan(platform string, has func(cmd string) bool, t Text, timeoutS int) (DialogPlan, bool) {
	if platform == "darwin" && has("osascript") {
		args := make([]string, 0, 2*len(osascript)+2)
		for _, line := range osascript {
			args = append(args, "-e", strings.Replace(line, "TIMEOUT", fmt.Sprint(timeoutS), 1))
		}
		return DialogPlan{Kind: KindOsascript, File: "osascript", Args: append(args, t.Title, t.Body)}, true
	}
	if platform != "linux" {
		return DialogPlan{}, false
	}
	if has("zenity") {
		return DialogPlan{Kind: KindZenity, File: "zenity", Args: []string{
			"--question", "--no-markup", "--title=" + t.Title, "--text=" + t.Body,
			"--ok-label=Allow", "--cancel-label=Deny", "--default-cancel", fmt.Sprintf("--timeout=%d", timeoutS),
		}}, true
	}
	if has("kdialog") {
		return DialogPlan{Kind: KindKdialog, File: "kdialog", Args: []string{"--title", t.Title, "--yes-label", "Allow", "--no-label", "Deny", "--yesno", t.Body}}, true
	}
	return DialogPlan{}, false
}

// AnswerOf is what the user did, from the dialog tool's exit code and output. Anything unexpected is a Deny.
func AnswerOf(kind DialogKind, code int, stdout string) Answer {
	if kind == KindOsascript {
		if code != 0 {
			return AnswerDeny
		}
		button, rest, _ := strings.Cut(strings.TrimSpace(stdout), ",")
		gaveUp, _, _ := strings.Cut(rest, ",")
		if gaveUp == "true" {
			return AnswerTimeout
		}
		if button == "Allow" {
			return AnswerAllow
		}
		return AnswerDeny
	}
	if code == 0 {
		return AnswerAllow
	}
	if kind == KindZenity && code == 5 {
		return AnswerTimeout
	}
	return AnswerDeny
}
