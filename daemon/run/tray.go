package run

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// trayLink is how the daemon shows its state: to the app that started it (agentd.app hands over a descriptor,
// AGENTD_UI_FD; the daemon ends when the app goes), else in a tray program of its own, else not at all (nil).
func (d *daemon) trayLink() (func(onAction func(ui.Action)) ui.Link, error) {
	fd, ok, err := ui.UIFd(d.env)
	if err != nil {
		return nil, err
	}
	if ok {
		conn, err := ui.FDConn(fd)
		if err != nil {
			return nil, fmt.Errorf("the channel to agentd.app (AGENTD_UI_FD=%d): %w", fd, err)
		}
		return func(onAction func(ui.Action)) ui.Link {
			return ui.NewAppLink(conn, onAction, func() {
				d.Log.Say("agentd.app closed its channel: stopping")
				d.stop(home.ExitStopped)
			}, d.Log)
		}, nil
	}
	program := ui.File(d.env, filepath.Dir(d.exe), runtime.GOOS, runtime.GOARCH, func(file string) bool {
		_, err := os.Stat(file)
		return err == nil
	})
	if program == "" {
		return nil, nil
	}
	return func(onAction func(ui.Action)) ui.Link { return ui.NewTray(program, onAction, d.Log) }, nil
}

// startTray shows the daemon's state through open and does what the user chooses; it returns what ends it.
func (d *daemon) startTray(open func(onAction func(ui.Action)) ui.Link) func() {
	opened := open
	open = func(onAction func(ui.Action)) ui.Link {
		link := opened(onAction)
		d.ui.Store(&link)
		return link
	}
	logFile := ""
	if d.layout != nil {
		logFile = d.layout.LogFile
	}
	sessions := d.running
	port := 0
	if d.gateway != nil {
		port = d.gateway.Port
	}
	return ui.StartMenu(ui.MenuDeps{
		Open:         open,
		RegistryFile: d.file,
		Hello:        d.trayHello,
		Linked:       d.hub.Has,
		AppStatus: func(server string) ui.AppState {
			s := d.hub.Status(server)
			return ui.AppStateOf(s.Linked, s.Connected, string(s.Dropped), s.Detail)
		},
		Sessions:    sessions,
		GatewayPort: port,
		LogFile:     logFile,
		Stop:        d.stop,
		Restart:     func() { d.restartRequested("the tray menu", sessions()) },
		Disconnect:  d.disconnectOrigin,
		Updates:     func() bool { return d.settings.Get().Updates },
		Updatable:   d.updates.updatable,
		SetUpdates: func(on bool) error {
			if _, err := d.settings.Change(home.SettingsPatch{Updates: &on}); err != nil {
				return err
			}
			d.updates.turn(on)
			return nil
		},
		Acp: d.acpAccess,
		Log: d.Log,
	})
}

func (d *daemon) trayHello() ui.Hello {
	return ui.Hello{Version: home.Version, Managed: home.IsManaged(d.env), Agents: d.pageHello().Agents}
}

// ask asks the user: in the consent window of the app or the tray program when it shows one, else in the native
// dialogs. AGENTD_CONSENT (a test seam) always stands for the dialogs.
func (d *daemon) ask(q consent.Question) consent.Decision {
	if d.env["AGENTD_CONSENT"] == "" {
		if link := d.ui.Load(); link != nil {
			if asker, ok := (*link).(ui.Asker); ok {
				if decision, asked := asker.Ask(q, consent.PickTimeoutS); asked {
					return decision
				}
			}
		}
	}
	return consent.Decide(q, d.env, consent.TimeoutS)
}
