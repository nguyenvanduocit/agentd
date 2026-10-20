package main

import "github.com/egoist/mygo/ui"

// Settings is the window of the daemon's settings and controls. It shows the last state the daemon sent and reports
// what the user chooses through send, in the ids the menu reports.
type Settings struct {
	State State
	Host  Host
	send  func(id string)
}

// View builds the window from the state, for every frame. In agentd.app it also says why an engine that does not run
// stopped, offers Start, and has the login switch.
func (w *Settings) View(c *ui.Context) {
	ui.Scroll(c).Fill().Padding(20).Gap(20).Children(func() {
		if w.Host.App && !w.Host.Running {
			w.stopped(c)
			return
		}
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, Title(w.State)).FontSize(20).Bold()
			ui.Text(c, Summary(w.State)).TextColor(c.Theme().TextMuted)
			if note := GatewayNote(w.State); note != "" {
				ui.Text(c, note)
			}
		})
		w.connected(c)
		w.agents(c)
		if w.Host.App {
			w.login(c)
		} else {
			w.updates(c)
		}
		w.controls(c)
	})
}

// stopped is the app's window while its engine does not run: why, and Start.
func (w *Settings) stopped(c *ui.Context) {
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, "agentd").FontSize(20).Bold()
		ui.Text(c, w.Host.Note).TextColor(c.Theme().TextMuted)
	})
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "Start agentd").Clicked() {
			w.send(StartID)
		}
		ui.Spacer(c)
		if ui.Button(c, "Open log").Clicked() {
			w.send("logs")
		}
		if ui.Button(c, "Quit agentd").Clicked() {
			w.send(QuitID)
		}
	})
	w.login(c)
}

// login is the app's switch "Open at login": a login item starts agentd with the session, with no window. Updates of
// the app (engine included) are checked in the background; the button checks now.
func (w *Settings) login(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Start and updates")
		on := w.Host.OpenAtLogin
		ui.Field(c, "Open at login", func() {
			if ui.Switch(c, &on).Changed() {
				w.Host.OpenAtLogin = on
				if on {
					w.send(LoginOnID)
				} else {
					w.send(LoginOffID)
				}
			}
		}).Description("agentd starts when you log in, in the menu bar, so your apps find it.")
		if ui.Button(c, "Check for Updates…").Clicked() {
			w.send(CheckUpdatesID)
		}
	})
}

func heading(c *ui.Context, s string) {
	ui.Text(c, s).Bold()
}

// firstRunText is what the window says while nothing is connected: what agentd is, and the one thing to do.
const firstRunText = "agentd lets apps you approve run AI coding agents on this computer. Go back to the app that sent you here and choose Connect."

// connected lists each app and web page with its own Disconnect, which ends its sessions and forgets it. With none, it
// says what agentd is and where to go next, unless web pages cannot reach agentd: the line under the summary says
// that, and "choose Connect" would send the person to a page that cannot connect.
func (w *Settings) connected(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		if len(w.State.Apps)+len(w.State.Pages) == 0 {
			if GatewayNote(w.State) == "" {
				ui.Text(c, firstRunText)
			}
			return
		}
		heading(c, "Connected")
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
// and Stop; in agentd.app the log, Restart and Quit.
func (w *Settings) controls(c *ui.Context) {
	if w.Host.App {
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Open log").Clicked() {
				w.send("logs")
			}
			ui.Spacer(c)
			if ui.Button(c, RestartLabel(w.State)).Clicked() {
				w.send("restart")
			}
			if ui.Button(c, QuitLabel(w.State)).Clicked() {
				w.send(QuitID)
			}
		})
		return
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Open log").Disabled(!w.State.Log).Clicked() {
				w.send("logs")
			}
			ui.Spacer(c)
			if ui.Button(c, RestartLabel(w.State)).Disabled(!w.State.Managed).Clicked() {
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
