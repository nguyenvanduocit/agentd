// Package ui is how the daemon shows its state and asks the user: to agentd.app over the descriptor it handed over (AppLink), or to a
// tray program of its own that it starts (Tray), and what it does with the user's choices (StartMenu). The wire is the
// tray's protocol (app/menu.go): JSON lines, states from the daemon and choices ("action" lines) from the tray.
//
// The pure part (states, choices, where the tray program sits, what opens a file, which descriptor is the app's) is in
// this file; the shell (processes, descriptors, the refresh loop) is in the others.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
)

// State is the state message the tray reads (app/menu.go State). The field order is the order on the wire.
type State struct {
	T       string `json:"t"`
	Version string `json:"version"`
	// Apps are the apps the daemon is linked to now. AppList is every saved app with its status, the linked ones and
	// the ones the daemon dropped: a tray program that knows AppList draws that, one that does not draws Apps.
	Apps     []string `json:"apps"`
	AppList  []AppRow `json:"appList"`
	Pages    []string `json:"pages"`
	Sessions int      `json:"sessions"`
	Managed  bool     `json:"managed"`
	// GatewayOff: no port was free, so web pages cannot reach the daemon.
	GatewayOff bool `json:"gatewayOff"`
	// Log: whether Open log has a file to open; without one the daemon writes to its terminal.
	Log    bool                  `json:"log"`
	Agents []pagetools.AgentInfo `json:"agents"`
	// Updates is the setting "install updates automatically"; Updatable: whether this daemon can update itself at all.
	Updates   bool `json:"updates"`
	Updatable bool `json:"updatable"`
	// Acp is how a program on this computer reaches the standard ACP endpoint; absent while there is no endpoint.
	Acp *AcpState `json:"acp,omitempty"`
	// Notice is one plain sentence about a choice of the user that failed ("Could not disconnect ..."); absent when
	// there is none. It stays for NoticeFor, and any choice that works clears it.
	Notice string `json:"notice,omitempty"`
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

// AppRow is one saved app and how it stands. Reason is a plain sentence for the statuses that are not working.
type AppRow struct {
	Server string `json:"server"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
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
func AppRows(servers []string, status func(server string) AppState) []AppRow {
	rows := make([]AppRow, 0, len(servers))
	for _, server := range servers {
		st := AppState{}
		if status != nil {
			st = status(server)
		}
		if st.Status == "" {
			st.Status = AppConnecting
		}
		rows = append(rows, AppRow{Server: server, Status: st.Status, Reason: st.Reason})
	}
	return rows
}

// AcpState is the address of the standard ACP endpoint and the token that goes with it: the app copies
// Base+agent+"?token="+Token for the user to paste into another app. It carries the token, so it goes only to the
// channel the daemon shares with its own app or tray, and is never written to a log.
type AcpState struct {
	Base   string   `json:"base"`
	Token  string   `json:"token"`
	Agents []string `json:"agents"`
}

// Facts are what a State is made of.
type Facts struct {
	Version string
	Apps    []string
	// AppList is every saved app with its status (AppRows); Notice the sentence about a choice that failed.
	AppList  []AppRow
	Notice   string
	Browsers []string
	Sessions int
	Managed  bool
	// GatewayPort is the port of the local gateway, 0 when none was free.
	GatewayPort int
	// LogFile is the daemon's log file, when it runs as an installed service; "" otherwise.
	LogFile   string
	Agents    []pagetools.AgentInfo
	Updates   bool
	Updatable bool
	// Acp is the address of the ACP endpoint with its token; nil when there is none.
	Acp *AcpState
}

// NewState is the state message for the facts. The lists are never null on the wire.
func NewState(f Facts) State {
	return State{
		T:          "state",
		Version:    f.Version,
		Apps:       nonNil(f.Apps),
		AppList:    nonNil(f.AppList),
		Pages:      nonNil(f.Browsers),
		Sessions:   f.Sessions,
		Managed:    f.Managed,
		GatewayOff: f.GatewayPort == 0,
		Log:        f.LogFile != "",
		Agents:     nonNil(f.Agents),
		Updates:    f.Updates,
		Updatable:  f.Updatable,
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

// line is the state as one line of JSON, without its newline. Like JSON.stringify it leaves <, > and & as they are.
func (s State) line() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a State always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// ActionKind is what the user chose in the tray menu or its settings window.
type ActionKind string

// The choices.
const (
	ActionLogs       ActionKind = "logs"
	ActionRestart    ActionKind = "restart"
	ActionStop       ActionKind = "stop"
	ActionDisconnect ActionKind = "disconnect"
	ActionUpdates    ActionKind = "updates"
)

// Action is a choice. Origin is set for a disconnect, On for updates.
type Action struct {
	Kind   ActionKind
	Origin string
	On     bool
}

// The choices that are a word of their own; a disconnect names its origin after disconnectPrefix.
var choices = map[string]Action{
	"logs":        {Kind: ActionLogs},
	"restart":     {Kind: ActionRestart},
	"stop":        {Kind: ActionStop},
	"updates:on":  {Kind: ActionUpdates, On: true},
	"updates:off": {Kind: ActionUpdates, On: false},
}

const disconnectPrefix = "disconnect:"

// ParseLine is what the user chose in the tray menu or its settings window; false for a line that is none of those
// choices.
func ParseLine(line string) (Action, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return Action{}, false
	}
	if m["t"] != "action" {
		return Action{}, false
	}
	id, ok := m["id"].(string)
	if !ok {
		return Action{}, false
	}
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

// Binary is where the tray program of a platform sits beside the engine, in dir; "" when there is no build for it.
// goos and goarch are runtime.GOOS and runtime.GOARCH values (scripts/build-tray.sh).
func Binary(dir, goos, goarch string) string {
	switch goos {
	case "darwin", "linux":
		if goarch != "amd64" && goarch != "arm64" {
			return ""
		}
		return path.Join(dir, "tray", goos+"-"+goarch, "agentd-tray")
	case "windows":
		if goarch != "amd64" {
			return ""
		}
		return strings.TrimRight(dir, `\/`) + `\tray\windows-amd64\agentd-tray.exe`
	}
	return ""
}

// Command is a program and its arguments.
type Command struct {
	File string
	Args []string
}

// OpenCommand is the command that opens a file with whatever the user opens such files with.
func OpenCommand(goos, file string) Command {
	switch goos {
	case "darwin":
		return Command{File: "open", Args: []string{file}}
	case "windows":
		return Command{File: "cmd", Args: []string{"/c", "start", "", file}}
	}
	return Command{File: "xdg-open", Args: []string{file}}
}

// UIFd is the descriptor on which the app that started the daemon (agentd.app) talks to it: $AGENTD_UI_FD. ok is
// false when it is not set, and a tray program of the daemon's own shows its state instead. The standard streams are
// not one: they are the daemon's log.
func UIFd(env home.Env) (fd int, ok bool, err error) {
	raw := env["AGENTD_UI_FD"]
	if raw == "" {
		return 0, false, nil
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil || n < 3 {
		return 0, false, fmt.Errorf("AGENTD_UI_FD %q is not a descriptor of 3 or more", raw)
	}
	return n, true, nil
}
