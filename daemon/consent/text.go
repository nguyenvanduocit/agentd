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
	// Agents are the agents that are ready now.
	Agents []string
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

// ConsentText says what Allow is: full access to the user's account. Whoever drives a session (the app's server, or
// the origin's page) chooses its folder, its MCP servers and its answers to permission requests, and agents run as the
// user; nothing on this machine narrows that. The two modes differ in when that access can be used.
func ConsentText(q Question) Text {
	agents := "no agent is ready yet"
	if len(q.Agents) > 0 {
		agents = strings.Join(q.Agents, ", ")
	}
	var when string
	if q.Mode == ModeServer {
		when = fmt.Sprintf("%s wants to connect its server to this computer.\n\nIf you allow it, that server can start your agents (%s) here at any time, even when none of its pages is open.", q.Origin, agents)
	} else {
		when = fmt.Sprintf("%s wants to run agents on this computer from its pages.\n\nIf you allow it, its pages can start your agents (%s) here whenever one of its pages is open.", q.Origin, agents)
	}
	return Text{
		Title: "Give " + q.Origin + " access to this computer?",
		Body: when + " The agents run as you: they can read and change any of your files and run any program.\n\n" +
			"It keeps that access until you disconnect it, from the daemon's icon in the menu bar or with: agentd apps remove " + q.Origin + "\n\n" +
			"Only allow apps you trust.",
	}
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
			"--ok-label=Allow", "--cancel-label=Deny", fmt.Sprintf("--timeout=%d", timeoutS),
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
