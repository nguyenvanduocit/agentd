// agentd is the app and the engine in one process. Started with no arguments it is agentd.app: it runs the engine, the
// agentd daemon, in a goroutine, shows its state in the menu bar and a window, and ends it on Quit (appmode.go). Started
// with a command (`mcp`, `acp`, `apps`, `pair`, `run`, `version`) it is agentd's command line (daemon/entry), which the
// agents' MCP wrappers and the user's terminal call.
package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/entry"
	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
)

// app is the icon, its menu and the settings window. Its fields are read and changed on the main thread only: by the
// menu, the window and the states RunOnMain hands over.
type app struct {
	tray     *mygo.Tray
	settings Settings
	win      *mygo.Window
	// own handles the entries the app keeps for itself; false hands one to report, the engine's.
	own    func(id string) bool
	report func(id string)
	// consent is the window of the question being asked, if any.
	consent *Consent
	// copy puts text on the clipboard; nil is the system's (a test replaces it).
	copy func(text string)
}

func (a *app) choose(id string) {
	if id == SettingsID {
		a.showSettings()
		return
	}
	if agent, ok := strings.CutPrefix(id, CopyACPPrefix); ok {
		a.copyAcp(agent)
		return
	}
	if a.own != nil && a.own(id) {
		return
	}
	a.report(id)
}

// copyAcp puts the address of an agent on the clipboard, for the user to paste into another app. The address holds the
// token, so it goes nowhere else: not to the daemon, not to a log, not onto the screen.
func (a *app) copyAcp(agent string) {
	address, ok := AcpAddress(a.settings.State, agent)
	if !ok {
		a.settings.Notice = "agentd has no address to copy yet. Try again in a moment."
		a.refresh()
		return
	}
	if a.copy != nil {
		a.copy(address)
	} else {
		mygo.Clipboard.WriteText(address)
	}
	title := agent
	for _, r := range AcpReady(a.settings.State) {
		if r.Name == agent {
			title = r.Title
		}
	}
	a.settings.Notice = "Copied the address for " + title + ". Paste it into the agent URL setting of your editor or tool. It works like a password: don't share it."
	a.refresh()
}

func (a *app) toItems(entries []Entry) []*mygo.MenuItem {
	items := make([]*mygo.MenuItem, 0, len(entries))
	for _, e := range entries {
		if e.Separator {
			items = append(items, mygo.Separator())
			continue
		}
		item := &mygo.MenuItem{Label: e.Label, Disabled: e.Disabled}
		if len(e.Children) > 0 {
			item.Submenu = a.toItems(e.Children)
		} else if e.ID != "" {
			id := e.ID
			item.Click = func(*mygo.MenuItem, *mygo.Window) { a.choose(id) }
		}
		items = append(items, item)
	}
	return items
}

func (a *app) menu() *mygo.Menu {
	return mygo.NewMenu(a.toItems(BuildMenu(a.settings.State, a.settings.Host)))
}

// showSettings opens the settings window, or brings it to the front. A closed window is gone: the next one starts anew.
func (a *app) showSettings() {
	if a.win == nil {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "agentd", Width: 440, Height: 600, Content: ui.View(a.settings.View)})
		a.win.OnClosed(func() { a.win = nil })
	}
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
}

// ask opens the window of a question; the decision goes to result. The daemon may ask while another question is open: it
// is a Deny, the window takes one at a time.
func (a *app) ask(req ConsentRequest, result chan<- consent.Decision) {
	if a.consent != nil {
		result <- consent.Decision{Answer: consent.AnswerDeny}
		return
	}
	c := newConsent(req, result)
	c.win = mygo.NewWindow(mygo.WindowOptions{Title: "agentd", Width: 480, Height: 700, AlwaysOnTop: true, DisableMinimize: true, Content: ui.View(c.View)})
	c.answer = func(allow bool) { a.answer(c, allow) }
	c.win.OnClosed(func() { a.answer(c, false) }) // closing the window is a Deny
	a.consent = c
	if req.TimeoutS > 0 {
		time.AfterFunc(time.Duration(req.TimeoutS)*time.Second, func() { mygo.RunOnMain(func() { a.lapse(c) }) })
	}
	c.win.Show()
	c.win.Focus()
	mygo.App.Focus()
}

// answer sends the user's decision once, and closes the window.
func (a *app) answer(c *Consent, allow bool) {
	a.decide(c, agentui.DecisionOf(allow && c.CanAllow(), c.Folders, c.LastsFor()))
}

// lapse ends a question nobody answered in time: the daemon is told so, and the window closes.
func (a *app) lapse(c *Consent) {
	a.decide(c, consent.Decision{Answer: consent.AnswerTimeout})
}

// decide hands the daemon the decision of a question, once, and closes its window.
func (a *app) decide(c *Consent, d consent.Decision) {
	if c.Answered {
		return
	}
	c.Answered = true
	c.result <- d
	a.closeConsent(c)
}

func (a *app) closeConsent(c *Consent) {
	if a.consent == c {
		a.consent = nil
	}
	c.win.Close()
}

// show takes a state from the engine into the menu and the window.
func (a *app) show(s State) {
	a.settings.State = s
	a.refresh()
}

// refresh draws the menu and the window again.
func (a *app) refresh() {
	if a.tray != nil {
		a.tray.SetMenu(a.menu())
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// makeTray shows the icon; false when the system has nowhere to show it.
func (a *app) makeTray() bool {
	tray, err := mygo.NewTray(mygo.TrayOptions{
		Icon:           icon,
		IconIsTemplate: iconIsTemplate,
		ToolTip:        "agentd",
		Menu:           a.menu(),
	})
	if err != nil {
		log.Println("tray:", err)
		return false
	}
	a.tray = tray
	return true
}

func main() {
	if entry.IsCommand(os.Args[1:]) {
		os.Exit(entry.Main(os.Args[1:])) // the agents' MCP wrappers and the user's terminal call this executable
	}
	if !startApp() {
		return // agentd.app runs already: it shows its window
	}
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
