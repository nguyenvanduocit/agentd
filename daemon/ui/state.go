// Package ui is how the daemon shows its state and asks the user: through the Link and the Asker that agentd.app gives it
// (run.Deps.UI), and what it does with the user's choices (StartMenu).
//
// The pure part (states, choices, what opens a file) is in this file; the shell (the refresh loop) is in the others.
package ui

import (
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// State is what agentd.app shows: the menu and the settings window.
type State struct {
	Version string
	// Apps are the apps the daemon is linked to now. AppList is every saved app with its status, the linked ones and
	// the ones the daemon dropped.
	Apps    []string
	AppList []AppRow
	// Pages are the web pages allowed now; PageList the same with the name each gave itself and when its access ends.
	Pages    []string
	PageList []PageRow
	Sessions int
	// GatewayOff: no port was free, so web pages cannot reach the daemon.
	GatewayOff bool
	Agents     []pagetools.AgentInfo
	// Acp is how a program on this computer reaches the standard ACP endpoint; absent while there is no endpoint.
	Acp *AcpState
	// Notice is one plain sentence about a choice of the user that failed ("Could not disconnect ..."); absent when
	// there is none. It stays for NoticeFor, and any choice that works clears it.
	Notice string
}

// The statuses of a saved app.
const (
	// AppConnected: the link is up.
	AppConnected = "connected"
	// AppConnecting: linked and not connected yet, or reconnecting; also a saved app not linked yet.
	AppConnecting = "connecting"
	// AppStopped: the app's server sent Stop, so it stays disconnected until the next start.
	AppStopped = "stopped"
	// AppRefused: the app's server refused the token: it took the access back.
	AppRefused = "refused"
	// AppIncompatible: the app speaks a protocol this agentd does not.
	AppIncompatible = "incompatible"
)

// AppRow is one saved app and how it stands. Name is what the app called itself (empty for an app paired from the
// command line); Ends is when its access ends, zero when it never does. Reason is a plain sentence for the statuses that
// are not working.
type AppRow struct {
	Server string
	Name   string
	Ends   time.Time
	Status string
	Reason string
}

// PageRow is one web page allowed to run agents: Name and Ends as for an AppRow.
type PageRow struct {
	Origin string
	Name   string
	Ends   time.Time
}

// endsAt is the time access ends, zero when it never does (or cannot be read: such access is not listed as live).
func endsAt(end time.Time, ends, ok bool) time.Time {
	if ok && ends {
		return end
	}
	return time.Time{}
}

// PageRows are the pages whose access has not ended at now, in the order they were allowed.
func PageRows(grants []registry.BrowserGrant, now time.Time) []PageRow {
	rows := make([]PageRow, 0, len(grants))
	for _, g := range grants {
		if g.Live(now) {
			rows = append(rows, PageRow{Origin: g.Origin, Name: g.Name, Ends: endsAt(g.End())})
		}
	}
	return rows
}

// AppState is the status of one app and, when it is not working, why.
type AppState struct {
	Status string
	Reason string
}

// The sentences of the apps that are not linked. They say "app" and "this computer": the person never needs to know
// there is a daemon.
const (
	stoppedReason      = "The app stopped this connection."
	refusedReason      = "The app no longer accepts this computer: its access was taken back."
	incompatibleReason = "The app and agentd on this computer speak different versions: one of them needs an update."
)

// AppStateOf is the status of an app from what the hub knows of it: whether it is linked and connected, and, for an
// app it let go, why (hub.Drop: "stopped", "refused" or "incompatible") with the hub's sentence for the last. A reason
// it does not know is no reason: the app is on its way to being linked.
func AppStateOf(linked, connected bool, dropped, detail string) AppState {
	switch {
	case linked && connected:
		return AppState{Status: AppConnected}
	case linked:
		return AppState{Status: AppConnecting}
	}
	switch dropped {
	case "stopped":
		return AppState{Status: AppStopped, Reason: stoppedReason}
	case "refused":
		return AppState{Status: AppRefused, Reason: refusedReason}
	case "incompatible":
		if detail == "" {
			detail = incompatibleReason
		}
		return AppState{Status: AppIncompatible, Reason: detail}
	}
	return AppState{Status: AppConnecting}
}

// AppRows are the saved apps, in the order they are saved, each with its status. status says it for one app; nil, or an
// answer without a status, means the app is on its way to being linked.
func AppRows(apps []registry.App, status func(server string) AppState) []AppRow {
	rows := make([]AppRow, 0, len(apps))
	for _, app := range apps {
		server := app.Server
		st := AppState{}
		if status != nil {
			st = status(server)
		}
		if st.Status == "" {
			st.Status = AppConnecting
		}
		rows = append(rows, AppRow{Server: server, Name: app.Name, Ends: endsAt(app.End()), Status: st.Status, Reason: st.Reason})
	}
	return rows
}

// AcpState is the address of the standard ACP endpoint and the token that goes with it: the app copies
// Base+agent+"?token="+Token for the user to paste into another app. It carries the token, so it goes only to the app
// and is never written to a log.
type AcpState struct {
	Base   string
	Token  string
	Agents []string
}

// Facts are what a State is made of.
type Facts struct {
	Version string
	Apps    []string
	// AppList is every saved app with its status (AppRows); Notice the sentence about a choice that failed.
	AppList  []AppRow
	Notice   string
	Browsers []string
	// PageList is Browsers with each page's name and end (PageRows).
	PageList []PageRow
	Sessions int
	// GatewayPort is the port of the local gateway, 0 when none was free.
	GatewayPort int
	Agents      []pagetools.AgentInfo
	// Acp is the address of the ACP endpoint with its token; nil when there is none.
	Acp *AcpState
}

// NewState is the state for the facts. The lists are never nil.
func NewState(f Facts) State {
	return State{
		Version:    f.Version,
		Apps:       nonNil(f.Apps),
		AppList:    nonNil(f.AppList),
		Pages:      nonNil(f.Browsers),
		PageList:   nonNil(f.PageList),
		Sessions:   f.Sessions,
		GatewayOff: f.GatewayPort == 0,
		Agents:     nonNil(f.Agents),
		Acp:        f.Acp,
		Notice:     f.Notice,
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ActionKind is what the user chose in the app's menu or its settings window.
type ActionKind string

// The choices.
const (
	ActionRestart    ActionKind = "restart"
	ActionDisconnect ActionKind = "disconnect"
)

// Action is a choice. Origin is set for a disconnect.
type Action struct {
	Kind   ActionKind
	Origin string
}

// The choices that are a word of their own; a disconnect names its origin after disconnectPrefix.
var choices = map[string]Action{
	"restart": {Kind: ActionRestart},
}

const disconnectPrefix = "disconnect:"

// ActionOf is what the id of an entry of the menu or the settings window asks for; false for an id that is none of
// those choices.
func ActionOf(id string) (Action, bool) {
	if choice, known := choices[id]; known {
		return choice, true
	}
	rest, isDisconnect := strings.CutPrefix(id, disconnectPrefix)
	if !isDisconnect {
		return Action{}, false
	}
	origin, ok := gateway.OriginOf(rest)
	if !ok {
		return Action{}, false
	}
	return Action{Kind: ActionDisconnect, Origin: origin}, true
}
