package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Settings is the window of the app's settings and controls. It shows the last state the daemon sent and reports what
// the user chooses through send, in the ids the menu reports.
type Settings struct {
	State State
	Host  Host
	send  func(id string)
	// Notice is the sentence under the addresses to copy: what the last copy did.
	Notice string
	// More: the part of the window under the More button is open (the address to copy, the log, Restart, Quit).
	More bool
}

// View builds the window from the state, for every frame: the status first, then who is connected, the agents, the
// startup settings, and under More what is rarely needed. While the engine does not run it says why, and offers Start.
func (w *Settings) View(c *ui.Context) {
	ui.Scroll(c).Fill().Padding(20).Gap(20).Children(func() {
		if !w.Host.Running {
			w.stopped(c)
			return
		}
		w.header(c)
		w.connected(c)
		w.agents(c)
		w.login(c)
		w.more(c)
	})
}

// header is the status: agentd and its version, the headline, what is connected, and what needs a look.
func (w *Settings) header(c *ui.Context) {
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "agentd").FontSize(20).Bold()
			ui.Spacer(c)
			if v := w.State.Version; v != "" {
				ui.Text(c, "v"+v).FontSize(11).TextColor(c.Theme().TextMuted)
			}
		})
		headline(c, w.State, w.Host)
		if detail := Summary(w.State); detail != "" {
			ui.Text(c, detail).TextColor(c.Theme().TextMuted)
		}
		if note := GatewayNote(w.State); note != "" {
			ui.Text(c, note)
		}
		if w.State.Notice != "" {
			ui.Text(c, w.State.Notice)
		}
	})
}

// stopped is the app's window while its engine does not run: why, and Start.
func (w *Settings) stopped(c *ui.Context) {
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, "agentd").FontSize(20).Bold()
		headline(c, w.State, w.Host)
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

// login is the app's switch "Start when I log in": a login item starts agentd with the session, with no window. Updates
// of the app (engine included) are checked in the background; the button checks now.
func (w *Settings) login(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Startup and updates")
		on := w.Host.OpenAtLogin
		ui.Field(c, "Start when I log in", func() {
			if ui.Switch(c, &on).Changed() {
				w.Host.OpenAtLogin = on
				if on {
					w.send(LoginOnID)
				} else {
					w.send(LoginOffID)
				}
			}
		}).Description("agentd opens in the menu bar when you log in, so your apps can always reach it.")
		if ui.Button(c, "Check for Updates…").Clicked() {
			w.send(CheckUpdatesID)
		}
	})
}

func heading(c *ui.Context, s string) {
	ui.Text(c, s).Bold()
}

// headline draws the status sentence: green behind a ●, the warning color behind a !, so a problem is seen before it
// is read.
func headline(c *ui.Context, s State, h Host) {
	text := Headline(s, h)
	line := ui.Text(c, text).Bold()
	switch {
	case strings.HasPrefix(text, "!"):
		line.TextColor(c.Theme().Danger)
	case strings.HasPrefix(text, "●"):
		line.TextColor(c.Theme().Success)
	}
}

// firstRunText is what the window says while nothing is connected: what agentd is, and the one thing to do.
const firstRunText = "agentd runs AI coding agents, such as Claude Code and Codex, on this computer for the apps you approve. To start, open the app or website you want to use and click Connect there."

// connected lists each saved app and website with its own Disconnect, which ends its sessions and forgets it. An app
// the daemon dropped stays, with the sentence that says why. With none, it says what agentd is and where to go next,
// unless websites cannot reach agentd: the line under the status says that, and "click Connect" would send the person
// to a page that cannot connect.
func (w *Settings) connected(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		apps, pages := listedApps(w.State), listedPages(w.State)
		if len(apps)+len(pages) == 0 {
			if w.State.Version != "" && GatewayNote(w.State) == "" {
				heading(c, "How to start")
				ui.Text(c, firstRunText)
			}
			return
		}
		heading(c, "Apps that can use your agents")
		for _, app := range apps {
			kind := app.note
			if kind == "" {
				kind = "app"
			}
			w.origin(c, app.server, app.name, kind, app.ends)
		}
		for _, page := range pages {
			w.origin(c, page.origin, page.name, "website", page.ends)
		}
	})
}

// origin is one saved app or website: its name (its address when it gave none), then a muted line with the address, the
// kind or the reason it is not connected, and when its access ends. Disconnect names it as the person reads it and
// sends its address.
func (w *Settings) origin(c *ui.Context, origin, name, kind string, ends time.Time) {
	title := titleOf(origin, name)
	line := []string{kind}
	if name != "" {
		line = []string{origin, kind}
	}
	if end := endsText(ends); end != "" {
		line = append(line, end)
	}
	ui.Row(c.Key(origin)).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title).SingleLine().Ellipsis("…")
			ui.Text(c, strings.Join(line, " · ")).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		if ui.Button(c, "Disconnect").Label("Disconnect " + title).Clicked() {
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
		heading(c, "Your agents")
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

// more is the button that opens the rarely needed part of the window.
func (w *Settings) more(c *ui.Context) {
	ui.Column(c).Gap(20).Children(func() {
		label := "More"
		if w.More {
			label = "Less"
		}
		if ui.Button(c, label).Clicked() {
			w.More = !w.More
		}
		if w.More {
			w.acp(c)
			w.controls(c)
		}
	})
}

// acpIntro is what the section over the addresses says: who can use an address, where it goes, and that it is a secret.
var acpIntro = []string{
	"Code editors and other tools that support ACP (a standard way for tools to talk to coding agents) can use your agents through agentd.",
	"Click Copy address, then paste it into the tool's setting for the agent's URL.",
	"The address contains a secret key. Treat it like a password and don't share it.",
}

// acp offers the address of each ready agent to copy for another program. The address holds a token, so it is copied
// and never drawn: a window can be seen over a shoulder, in a screen share and in a screenshot.
func (w *Settings) acp(c *ui.Context) {
	ready := AcpReady(w.State)
	if len(ready) == 0 {
		return
	}
	ui.Column(c).Gap(8).Children(func() {
		heading(c, "Use agentd in an editor or other tool")
		for _, line := range acpIntro {
			ui.Text(c, line)
		}
		for _, a := range ready {
			ui.Row(c.Key("acp-" + a.Name)).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.Title).Grow(1)
				if ui.Button(c, "Copy address").Label("Copy address for " + a.Title).Clicked() {
					w.send(CopyACPPrefix + a.Name)
				}
			})
		}
		if w.Notice != "" {
			ui.Text(c, w.Notice).FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}

// controls are the menu's: Open log, Restart and Quit.
func (w *Settings) controls(c *ui.Context) {
	ui.Column(c).Gap(8).Children(func() {
		if ui.Button(c, "Open log").Clicked() {
			w.send("logs")
		}
		if ui.Button(c, RestartLabel(w.State)).Clicked() {
			w.send("restart")
		}
		if ui.Button(c, QuitLabel(w.State)).Clicked() {
			w.send(QuitID)
		}
	})
}
