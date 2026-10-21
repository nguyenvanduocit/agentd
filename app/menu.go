package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
)

// The state the daemon shows is the daemon's own: one definition, in daemon/ui.
type (
	State     = agentui.State
	AppRow    = agentui.AppRow
	Agent     = pagetools.AgentInfo
	AcpAccess = agentui.AcpState
)

// The entries the app handles itself and does not report: the settings window, Start, Quit and the login switch (the
// app owns the engine, its log and its login item).
const (
	SettingsID = "settings"
	StartID    = "start"
	QuitID     = "quit"
	LoginOnID  = "login:on"
	LoginOffID = "login:off"
	// CheckUpdatesID checks for a newer agentd.app now (the app updates itself, engine included).
	CheckUpdatesID = "check-updates"
)

// Host is what the app knows of its engine and itself: whether the engine runs (Note says why it does not), and
// whether the app is a login item.
type Host struct {
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
const gatewayOffText = "Websites can't reach agentd: another program is using its port. Apps still work."

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// GatewayNote is the line under the status that says websites cannot reach agentd; empty while they can, and before
// the first state, which says nothing about the gateway yet. It is a line of its own because it does not change what
// is connected: apps connect outbound and do not use the gateway.
func GatewayNote(s State) string {
	if s.Version != "" && s.GatewayOff {
		return gatewayOffText
	}
	return ""
}

// listedApp is one app the menu or the window draws, with the sentence under its name when it is not connected. name
// is what the app called itself, ends when its access ends (zero: never).
type listedApp struct {
	server     string
	name       string
	ends       time.Time
	note       string
	failed     bool // the app is not connected and note says why
	connecting bool
}

// listedApps is every saved app when the daemon sends AppList, and the linked apps when it does not.
func listedApps(s State) []listedApp {
	if s.AppList != nil {
		out := make([]listedApp, len(s.AppList))
		for i, row := range s.AppList {
			connecting := row.Reason == "" && row.Status == "connecting"
			note := row.Reason
			if connecting {
				note = "Connecting…"
			}
			out[i] = listedApp{server: row.Server, name: row.Name, ends: row.Ends, note: note, failed: row.Reason != "", connecting: connecting}
		}
		return out
	}
	out := make([]listedApp, len(s.Apps))
	for i, app := range s.Apps {
		out[i] = listedApp{server: app}
	}
	return out
}

// Headline is the first line of the menu and the window: whether agentd works, in one sentence. A mark opens it: ●
// when all is well, ! when something needs the user. What is wrong comes first, then what is happening.
func Headline(s State, h Host) string {
	if !h.Running {
		return "! agentd is not running"
	}
	if s.Version == "" {
		return "Starting…"
	}
	apps := listedApps(s)
	var failed []string
	connecting := ""
	for _, a := range apps {
		switch {
		case a.failed:
			failed = append(failed, a.title())
		case a.connecting:
			connecting = a.title()
		}
	}
	switch {
	case len(failed) == 1:
		return "! " + failed[0] + " can't connect"
	case len(failed) > 1:
		return fmt.Sprintf("! %d apps can't connect", len(failed))
	case connecting != "":
		return "● Connecting " + connecting + "…"
	case len(apps)+len(s.Pages) == 0:
		return "● agentd is ready"
	}
	return "● Working"
}

// Summary is the line under the headline: what is connected, and the agents working. Empty before the first state.
func Summary(s State) string {
	if s.Version == "" {
		return ""
	}
	var parts []string
	if n := len(s.Apps); n > 0 {
		parts = append(parts, plural(n, "app", "apps"))
	}
	if n := len(s.Pages); n > 0 {
		parts = append(parts, plural(n, "website", "websites"))
	}
	text := "No apps connected yet"
	if len(parts) > 0 {
		text = "Connected: " + strings.Join(parts, ", ")
	} else if s.Sessions > 0 {
		text = "No apps connected"
	}
	if s.Sessions > 0 {
		text += " · " + plural(s.Sessions, "agent", "agents") + " working"
	}
	return text
}

// title is what the app is called to the person: the name it gave itself, else its address.
func (a listedApp) title() string { return titleOf(a.server, a.name) }

func titleOf(origin, name string) string {
	if name == "" {
		return origin
	}
	return name
}

// listedPage is one website the menu or the window draws; name and ends as for a listedApp.
type listedPage struct {
	origin string
	name   string
	ends   time.Time
}

func (p listedPage) title() string { return titleOf(p.origin, p.name) }

// listedPages is every allowed website with its name and end when the daemon sends PageList, and the bare origins
// when it does not.
func listedPages(s State) []listedPage {
	if s.PageList != nil {
		out := make([]listedPage, len(s.PageList))
		for i, row := range s.PageList {
			out[i] = listedPage{origin: row.Origin, name: row.Name, ends: row.Ends}
		}
		return out
	}
	out := make([]listedPage, len(s.Pages))
	for i, page := range s.Pages {
		out[i] = listedPage{origin: page}
	}
	return out
}

// endsText says when access ends; empty when it never does.
func endsText(ends time.Time) string {
	if ends.IsZero() {
		return ""
	}
	return "ends " + ends.Local().Format("2 Jan, 15:04")
}

// whoLine is the line that tells an app or a website from another that gave itself the same name: its address, and
// when its access ends. Empty when it would say nothing the title does not.
func whoLine(origin, name string, ends time.Time) string {
	var parts []string
	if name != "" {
		parts = append(parts, origin)
	}
	if end := endsText(ends); end != "" {
		parts = append(parts, end)
	}
	return strings.Join(parts, " · ")
}

func appEntry(app listedApp) Entry {
	disconnect := Entry{ID: "disconnect:" + app.server, Label: "Disconnect"}
	var children []Entry
	if line := whoLine(app.server, app.name, app.ends); line != "" {
		children = append(children, Entry{Label: line, Disabled: true})
	}
	label := app.title()
	if app.note != "" {
		if app.failed {
			label = "! " + label
		}
		children = append(children, Entry{Label: app.note, Disabled: true})
	}
	return Entry{Label: label, Children: append(children, disconnect)}
}

// BuildMenu is the whole menu for a state: the headline, what is saved (each app, with why it is not connected when it
// is not, and each website, each with its own Disconnect), the address to copy for an editor, and the few controls
// that need no window: Settings…, and Quit (Restart too while websites cannot reach agentd). The rest lives in the
// window. While the engine does not run, the menu offers Start.
func BuildMenu(s State, h Host) []Entry {
	if !h.Running {
		return []Entry{
			{Label: Headline(s, h), Disabled: true},
			{Label: h.Note, Disabled: true},
			{Separator: true},
			{ID: StartID, Label: "Start agentd"},
			{ID: SettingsID, Label: "Settings…"},
			{ID: "logs", Label: "Open log"},
			{ID: QuitID, Label: "Quit agentd"},
		}
	}
	menu := []Entry{{Label: Headline(s, h), Disabled: true}}
	if detail := Summary(s); detail != "" {
		menu = append(menu, Entry{Label: detail, Disabled: true})
	}
	if note := GatewayNote(s); note != "" {
		menu = append(menu, Entry{Label: note, Disabled: true})
	}
	if s.Notice != "" {
		menu = append(menu, Entry{Label: s.Notice, Disabled: true})
	}
	menu = append(menu, Entry{Separator: true})
	apps := listedApps(s)
	for _, app := range apps {
		menu = append(menu, appEntry(app))
	}
	for _, page := range listedPages(s) {
		var children []Entry
		if line := whoLine(page.origin, page.name, page.ends); line != "" {
			children = append(children, Entry{Label: line, Disabled: true})
		}
		menu = append(menu, Entry{Label: page.title() + " (website)", Children: append(children, Entry{ID: "disconnect:" + page.origin, Label: "Disconnect"})})
	}
	if len(apps)+len(s.Pages) > 0 {
		menu = append(menu, Entry{Separator: true})
	}
	if ready := AcpReady(s); len(ready) > 0 {
		group := Entry{Label: "Use in an editor or tool"}
		for _, a := range ready {
			group.Children = append(group.Children, Entry{ID: CopyACPPrefix + a.Name, Label: "Copy address for " + a.Title})
		}
		menu = append(menu, group, Entry{Separator: true})
	}
	menu = append(menu, Entry{ID: SettingsID, Label: "Settings…"})
	if s.GatewayOff {
		menu = append(menu, Entry{ID: "restart", Label: RestartLabel(s)})
	}
	return append(menu, Entry{ID: QuitID, Label: QuitLabel(s)})
}

// stopping is what Quit, Restart and Stop cost, in the label: the agents that are running stop.
func stopping(s State) string {
	if s.Sessions == 0 {
		return ""
	}
	return " (stops " + plural(s.Sessions, "running agent", "running agents") + ")"
}

// QuitLabel names Quit and the agents it stops.
func QuitLabel(s State) string { return "Quit agentd" + stopping(s) }

// RestartLabel names Restart and the agents it stops: it stops every one of them, then starts agentd again.
func RestartLabel(s State) string { return "Restart agentd" + stopping(s) }
