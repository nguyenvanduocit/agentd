// agentd-tray is the daemon's icon in the menu bar (macOS) or notification area (Windows, Linux). The daemon starts
// it and talks to it in JSON lines: it reads states on stdin and prints the entry the user chose on stdout. It ends
// when stdin does, so a daemon that dies takes its icon with it.
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/egoist/mygo"
)

var out sync.Mutex

func say(id string) {
	out.Lock()
	defer out.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", ActionLine(id))
}

func toItems(entries []Entry) []*mygo.MenuItem {
	items := make([]*mygo.MenuItem, 0, len(entries))
	for _, e := range entries {
		if e.Separator {
			items = append(items, mygo.Separator())
			continue
		}
		item := &mygo.MenuItem{Label: e.Label, Disabled: e.Disabled}
		if len(e.Children) > 0 {
			item.Submenu = toItems(e.Children)
		} else if e.ID != "" {
			id := e.ID
			item.Click = func(*mygo.MenuItem, *mygo.Window) { say(id) }
		}
		items = append(items, item)
	}
	return items
}

func menuFor(s State) *mygo.Menu { return mygo.NewMenu(toItems(BuildMenu(s))) }

func run() {
	tray, err := mygo.NewTray(mygo.TrayOptions{
		Icon:           icon,
		IconIsTemplate: iconIsTemplate,
		ToolTip:        "agentd",
		Menu:           menuFor(State{Version: "starting"}),
	})
	if err != nil {
		log.Println("tray:", err)
		mygo.App.Quit()
		return
	}
	go func() {
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for in.Scan() {
			if s, ok := ParseState(in.Bytes()); ok {
				tray.SetMenu(menuFor(s))
			}
		}
		mygo.App.Quit()
	}()
}

func main() {
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(run)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
