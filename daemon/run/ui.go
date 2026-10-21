package run

import (
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// startUI shows the daemon's state through open and does what the user chooses; it returns what ends it.
func (d *daemon) startUI(open func(onAction func(ui.Action)) ui.Link) func() {
	opened := open
	open = func(onAction func(ui.Action)) ui.Link {
		link := opened(onAction)
		d.ui.Store(&link)
		return link
	}
	sessions := d.running
	port := 0
	if d.gateway != nil {
		port = d.gateway.Port
	}
	return ui.StartMenu(ui.MenuDeps{
		Open:         open,
		RegistryFile: d.file,
		Hello:        d.uiHello,
		Linked:       d.hub.Has,
		AppStatus: func(server string) ui.AppState {
			s := d.hub.Status(server)
			return ui.AppStateOf(s.Linked, s.Connected, string(s.Dropped), s.Detail)
		},
		Sessions:    sessions,
		GatewayPort: port,
		Restart:     func() { d.restartRequested("the app", sessions()) },
		Disconnect:  d.disconnectOrigin,
		Acp:         d.acpAccess,
		Log:         d.Log,
	})
}

func (d *daemon) uiHello() ui.Hello {
	return ui.Hello{Version: home.Version, Agents: d.pageHello().Agents}
}

// ask asks the user in the app's consent window. AGENTD_CONSENT (a test seam) stands for that window. With neither,
// there is no way to ask, and the origin is not granted.
func (d *daemon) ask(q consent.Question) consent.Decision {
	if d.env["AGENTD_CONSENT"] != "" {
		return consent.Decide(q, d.env, consent.TimeoutS)
	}
	if link := d.ui.Load(); link != nil {
		if asker, ok := (*link).(ui.Asker); ok {
			if decision, asked := asker.Ask(q, consent.TimeoutS); asked {
				return decision
			}
		}
	}
	return consent.Decision{Answer: consent.AnswerUnavailable}
}
