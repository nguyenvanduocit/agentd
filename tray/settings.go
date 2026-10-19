package main

import "github.com/egoist/mygo/ui"

// Settings is the window of the daemon's settings and controls. It shows the last state the daemon sent and reports
// what the user chooses through send, in the ids the menu reports.
type Settings struct {
	State State
	send  func(id string)
}

// View builds the window from the state, for every frame.
func (w *Settings) View(c *ui.Context) {
	ui.Scroll(c).Fill().Padding(20).Gap(20).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "agentd "+w.State.Version).FontSize(20).Bold()
			ui.Text(c, Summary(w.State)).TextColor(c.Theme().TextMuted)
		})
		w.connected(c)
		w.agents(c)
		w.updates(c)
		w.controls(c)
	})
}

func heading(c *ui.Context, s string) {
	ui.Text(c, s).Bold()
}

// connected lists each app and web page with its own Disconnect, which ends its sessions and forgets it.
func (w *Settings) connected(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Connected")
		if len(w.State.Apps)+len(w.State.Pages) == 0 {
			ui.Text(c, "None yet: an app or a web page asks to connect from its own page.").TextColor(c.Theme().TextMuted)
			return
		}
		for _, app := range w.State.Apps {
			w.origin(c, app, "app")
		}
		for _, page := range w.State.Pages {
			w.origin(c, page, "web page")
		}
	})
}

func (w *Settings) origin(c *ui.Context, origin, kind string) {
	ui.Row(c.Key(origin)).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, origin).SingleLine().Ellipsis("…")
			ui.Text(c, kind).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		if ui.Button(c, "Disconnect").Label("Disconnect " + origin).Clicked() {
			w.send("disconnect:" + origin)
		}
	})
}

// agents says which agents the daemon can start, and why one cannot.
func (w *Settings) agents(c *ui.Context) {
	if len(w.State.Agents) == 0 {
		return
	}
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Agents")
		for _, a := range w.State.Agents {
			ui.Row(c.Key(a.Name)).Gap(12).Children(func() {
				ui.Text(c, a.Title).Grow(1)
				if a.Ready {
					ui.Text(c, "Ready").TextColor(c.Theme().Success)
				} else {
					ui.Text(c, a.Reason).TextColor(c.Theme().TextMuted)
				}
			})
		}
	})
}

// updates is the one setting: the daemon installs the versions the release server offers by itself, or not at all.
func (w *Settings) updates(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Updates")
		on := w.State.Updates
		field := ui.Field(c, "Install updates automatically", func() {
			if ui.Switch(c, &on).Disabled(!w.State.Updatable).Changed() {
				w.State.Updates = on
				if on {
					w.send("updates:on")
				} else {
					w.send("updates:off")
				}
			}
		})
		if !w.State.Updatable {
			field.Description("This daemon cannot update itself: it is not installed under ~/.agentd, or it has no releases page.")
		}
	})
}

// controls are the menu's: the log (only when it is in a file), Restart (only when a service starts the daemon again)
// and Stop.
func (w *Settings) controls(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Open log").Disabled(!w.State.Log).Clicked() {
				w.send("logs")
			}
			ui.Spacer(c)
			if ui.Button(c, "Restart").Disabled(!w.State.Managed).Clicked() {
				w.send("restart")
			}
			if ui.Button(c, StopLabel(w.State)).Clicked() {
				w.send("stop")
			}
		})
		if !w.State.Log {
			ui.Text(c, "This daemon writes its log to the terminal it runs in.").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		if !w.State.Managed {
			ui.Text(c, "No service restarts this daemon, so Restart is off.").FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}
