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

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Summary is the status line under the title.
func Summary(s State) string {
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
	if s.GatewayOff {
		parts = append(parts, "web pages cannot connect")
	}
	return strings.Join(parts, ", ")
}

// BuildMenu is the whole menu for a state: the title and status, what is connected (each with its own Disconnect),
// and the controls.
func BuildMenu(s State) []Entry {
	menu := []Entry{
		{Label: "agentd " + s.Version, Disabled: true},
		{Label: Summary(s), Disabled: true},
		{Separator: true},
	}
	for _, app := range s.Apps {
		menu = append(menu, Entry{Label: app, Children: []Entry{{ID: "disconnect:" + app, Label: "Disconnect"}}})
	}
	for _, page := range s.Pages {
		menu = append(menu, Entry{Label: page + " (web page)", Children: []Entry{{ID: "disconnect:" + page, Label: "Disconnect"}}})
	}
	if len(s.Apps)+len(s.Pages) > 0 {
		menu = append(menu, Entry{Separator: true})
	}
	menu = append(menu, Entry{ID: "logs", Label: "Open log"})
	if s.Managed {
		menu = append(menu, Entry{ID: "restart", Label: "Restart"})
	} else {
		menu = append(menu, Entry{ID: "restart", Label: "Restart (not managed)", Disabled: true})
	}
	stop := "Stop daemon"
	if s.Sessions > 0 {
		stop += " (ends " + plural(s.Sessions, "agent", "agents") + ")"
	}
	return append(menu, Entry{ID: "stop", Label: stop})
}
