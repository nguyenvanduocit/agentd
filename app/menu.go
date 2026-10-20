package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// State is what the daemon tells the tray, one JSON line at a time on stdin.
type State struct {
	T        string   `json:"t"`
	Version  string   `json:"version"`
	Apps     []string `json:"apps"`
	Pages    []string `json:"pages"`
	Sessions int      `json:"sessions"`
	Managed  bool     `json:"managed"`
	// GatewayOff: no port was free, so web pages cannot reach the daemon.
	GatewayOff bool `json:"gatewayOff"`
	// Log: the daemon writes its log to a file Open log can open; without one it writes to the terminal it runs in.
	Log    bool    `json:"log"`
	Agents []Agent `json:"agents"`
	// Updates is the setting "install updates automatically". Updatable: the daemon can update itself at all (it is
	// installed and has a release server); the setting changes nothing otherwise.
	Updates   bool `json:"updates"`
	Updatable bool `json:"updatable"`
	// Acp is how another program reaches the standard ACP endpoint; absent while there is none. It holds the token:
	// it is copied for the user and never drawn, logged or reported back.
	Acp *AcpAccess `json:"acp,omitempty"`
}

// AcpAccess is the address of the standard ACP endpoint (Base, ending in a slash) and its token, with the agents that
// are ready to be reached at Base+agent.
type AcpAccess struct {
	Base   string   `json:"base"`
	Token  string   `json:"token"`
	Agents []string `json:"agents"`
}

// Agent is an agent the daemon can start; Reason says why it cannot when it is not Ready.
type Agent struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// The entries the app handles itself and does not report: the settings window, and in agentd.app Start, Quit and the
// login switch (the app owns the engine, its log and its login item).
const (
	SettingsID = "settings"
	StartID    = "start"
	QuitID     = "quit"
	LoginOnID  = "login:on"
	LoginOffID = "login:off"
	// CheckUpdatesID checks for a newer agentd.app now (the app updates itself, engine included).
	CheckUpdatesID = "check-updates"
)

// Host is what shows the menu and the window: a tray the daemon started (the zero Host), or agentd.app, which starts
// the engine itself, keeps its log, and ends it on Quit.
type Host struct {
	App bool
	// Running: the app's engine runs; Note says why it does not.
	Running bool
	Note    string
	// OpenAtLogin: the app is a login item.
	OpenAtLogin bool
}

// Entry is one line of the tray menu. A non-empty ID is reported to the daemon when the entry is chosen.
type Entry struct {
	ID        string
	Label     string
	Disabled  bool
	Separator bool
	Children  []Entry
}

// ParseState reads one line of stdin; false when it is not a state message.
func ParseState(line []byte) (State, bool) {
	var s State
	if err := json.Unmarshal(line, &s); err != nil || s.T != "state" {
		return State{}, false
	}
	return s, true
}

// ActionLine is what the tray prints, one line per chosen entry, for the daemon to act on.
func ActionLine(id string) []byte {
	b, _ := json.Marshal(struct {
		T  string `json:"t"`
		ID string `json:"id"`
	}{"action", id})
	return b
}

// CopyACPPrefix starts the id of a choice to copy the address of an agent: the app handles it itself, the clipboard is
// the app's, and the token stays between the daemon and the app.
const CopyACPPrefix = "copy-acp:"

// acpAgent is a ready agent and what to call it on screen.
type acpAgent struct{ Name, Title string }

// AcpReady are the agents whose address can be copied, in the daemon's order.
func AcpReady(s State) []acpAgent {
	if s.Acp == nil || s.Acp.Base == "" || s.Acp.Token == "" {
		return nil
	}
	var ready []acpAgent
	for _, name := range s.Acp.Agents {
		title := name
		for _, a := range s.Agents {
			if a.Name == name && a.Title != "" {
				title = a.Title
			}
		}
		ready = append(ready, acpAgent{name, title})
	}
	return ready
}

// AcpAddress is the address to give another program for an agent that is ready: the endpoint, the agent, and the
// token in the query. Anything but a ready agent has none, so an id made up by a caller reaches nothing.
func AcpAddress(s State, agent string) (string, bool) {
	for _, a := range AcpReady(s) {
		if a.Name == agent {
			return strings.TrimSuffix(s.Acp.Base, "/") + "/" + agent + "?token=" + s.Acp.Token, true
		}
	}
	return "", false
}

// gatewayOffText is what the window and the menu say when no port was free for the local gateway.
const gatewayOffText = "Web pages can't reach agentd: its ports are in use by another program."

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Title is the heading of the menu and the window: agentd and its version. A state from the daemon always has its
// version; the placeholder before the first one has none.
func Title(s State) string {
	if s.Version == "" {
		return "agentd"
	}
	return "agentd " + s.Version
}

// GatewayNote is the line under the summary that says web pages cannot reach agentd; empty while they can, and before
// the first state, which says nothing about the gateway yet. It is a line of its own because it does not change what
// is connected: apps connect outbound and do not use the gateway.
func GatewayNote(s State) string {
	if s.Version != "" && s.GatewayOff {
		return gatewayOffText
	}
	return ""
}

// Summary is the status line under the title: what is connected, and the agents running.
func Summary(s State) string {
	if s.Version == "" {
		return "Starting…"
	}
	var parts []string
	if n := len(s.Apps); n > 0 {
		parts = append(parts, plural(n, "app", "apps"))
	}
	if n := len(s.Pages); n > 0 {
		parts = append(parts, plural(n, "web page", "web pages"))
	}
	if len(parts) == 0 {
		return "Nothing connected"
	}
	if s.Sessions > 0 {
		parts = append(parts, plural(s.Sessions, "agent", "agents")+" running")
	}
	return strings.Join(parts, ", ")
}

// BuildMenu is the whole menu for a state: the title and status, what is connected (each with its own Disconnect),
// and the controls. In agentd.app, Quit ends agentd, and an engine that does not run offers Start.
func BuildMenu(s State, h Host) []Entry {
	if h.App && !h.Running {
		return []Entry{
			{Label: "agentd", Disabled: true},
			{Label: h.Note, Disabled: true},
			{Separator: true},
			{ID: StartID, Label: "Start agentd"},
			{ID: SettingsID, Label: "Settings…"},
			{ID: CheckUpdatesID, Label: "Check for Updates…"},
			{ID: "logs", Label: "Open log"},
			{ID: QuitID, Label: "Quit agentd"},
		}
	}
	menu := []Entry{
		{Label: Title(s), Disabled: true},
		{Label: Summary(s), Disabled: true},
	}
	if note := GatewayNote(s); note != "" {
		menu = append(menu, Entry{Label: note, Disabled: true})
	}
	menu = append(menu, Entry{Separator: true})
	for _, app := range s.Apps {
		menu = append(menu, Entry{Label: app, Children: []Entry{{ID: "disconnect:" + app, Label: "Disconnect"}}})
	}
	for _, page := range s.Pages {
		menu = append(menu, Entry{Label: page + " (web page)", Children: []Entry{{ID: "disconnect:" + page, Label: "Disconnect"}}})
	}
	if len(s.Apps)+len(s.Pages) > 0 {
		menu = append(menu, Entry{Separator: true})
	}
	if ready := AcpReady(s); len(ready) > 0 {
		group := Entry{Label: "Other ACP apps"}
		for _, a := range ready {
			group.Children = append(group.Children, Entry{ID: CopyACPPrefix + a.Name, Label: "Copy ACP address: " + a.Title})
		}
		menu = append(menu, group, Entry{Separator: true})
	}
	menu = append(menu, Entry{ID: SettingsID, Label: "Settings…"})
	if h.App {
		return append(menu, Entry{ID: CheckUpdatesID, Label: "Check for Updates…"}, Entry{ID: "logs", Label: "Open log"}, Entry{ID: "restart", Label: RestartLabel(s)}, Entry{ID: QuitID, Label: QuitLabel(s)})
	}
	if s.Log {
		menu = append(menu, Entry{ID: "logs", Label: "Open log"})
	} else {
		menu = append(menu, Entry{ID: "logs", Label: "Open log (in the terminal)", Disabled: true})
	}
	if s.Managed {
		menu = append(menu, Entry{ID: "restart", Label: RestartLabel(s)})
	} else {
		menu = append(menu, Entry{ID: "restart", Label: "Restart (not managed)", Disabled: true})
	}
	return append(menu, Entry{ID: "stop", Label: StopLabel(s)})
}

// QuitLabel names Quit in agentd.app and the agents it ends.
func QuitLabel(s State) string {
	if s.Sessions == 0 {
		return "Quit agentd"
	}
	return "Quit agentd (ends " + plural(s.Sessions, "agent", "agents") + ")"
}

// RestartLabel names Restart and the agents it ends: it ends every one of them, then starts agentd again.
func RestartLabel(s State) string {
	if s.Sessions == 0 {
		return "Restart"
	}
	return "Restart (ends " + plural(s.Sessions, "agent", "agents") + ")"
}

// StopLabel names Stop and the agents it ends.
func StopLabel(s State) string {
	if s.Sessions == 0 {
		return "Stop daemon"
	}
	return "Stop daemon (ends " + plural(s.Sessions, "agent", "agents") + ")"
}
