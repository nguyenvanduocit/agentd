// agentd-tray is agentd's face, in one of two modes. As agentd.app (no arguments) it is the app: it starts the engine,
// the agentd daemon, shows its state in the menu bar and a window, and ends it on Quit (appmode.go). As `agentd-tray
// child`, the tray a daemon installed from the terminal starts, it talks JSON lines: it reads states on stdin and
// prints what the user chose, in the menu or the window, on stdout, and ends when stdin does.
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var out sync.Mutex

func say(id string) {
	out.Lock()
	defer out.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", ActionLine(id))
}

// app is the icon, its menu and the settings window. Its fields are read and changed on the main thread only: by the
// menu, the window and the states RunOnMain hands over.
type app struct {
	tray     *mygo.Tray
	settings Settings
	win      *mygo.Window
	// own handles the entries the app keeps for itself (agentd.app); false hands one to report, the daemon's.
	own    func(id string) bool
	report func(id string)
}

func (a *app) choose(id string) {
	if id == SettingsID {
		a.showSettings()
		return
	}
	if a.own != nil && a.own(id) {
		return
	}
	a.report(id)
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
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "agentd", Width: 440, Height: 520, Content: ui.View(a.settings.View)})
		a.win.OnClosed(func() { a.win = nil })
	}
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
}

// show takes a state from the daemon into the menu and the window.
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

// runChild is the tray of a daemon installed from the terminal: states on stdin, choices on stdout.
func runChild() {
	a := &app{report: say}
	a.settings = Settings{State: State{Version: "starting"}, send: a.choose}
	if !a.makeTray() {
		mygo.App.Quit()
		return
	}
	go func() {
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for in.Scan() {
			if s, ok := ParseState(in.Bytes()); ok {
				mygo.RunOnMain(func() { a.show(s) })
			}
		}
		mygo.App.Quit()
	}()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
		mygo.App.OnWindowAllClosed(func() {})
		mygo.App.WhenReady(runChild)
	} else if !startApp() {
		return // agentd.app runs already: it shows its window
	}
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
