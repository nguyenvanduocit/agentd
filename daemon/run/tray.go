package run

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/tray"
)

// trayLink is how the daemon shows its state: to the app that started it (agentd.app hands over a descriptor,
// AGENTD_UI_FD; the daemon ends when the app goes), else in a tray program of its own, else not at all (nil).
func (d *daemon) trayLink() (func(onAction func(tray.Action)) tray.Link, error) {
	fd, ok, err := tray.UIFd(d.env)
	if err != nil {
		return nil, err
	}
	if ok {
		conn, err := tray.FDConn(fd)
		if err != nil {
			return nil, fmt.Errorf("the channel to agentd.app (AGENTD_UI_FD=%d): %w", fd, err)
		}
		return func(onAction func(tray.Action)) tray.Link {
			return tray.NewAppLink(conn, onAction, func() {
				d.Log.Say("agentd.app closed its channel: stopping")
				d.stop(home.ExitStopped)
			}, d.Log)
		}, nil
	}
	program := tray.File(d.env, filepath.Dir(d.exe), runtime.GOOS, runtime.GOARCH, func(file string) bool {
		_, err := os.Stat(file)
		return err == nil
	})
	if program == "" {
		return nil, nil
	}
	return func(onAction func(tray.Action)) tray.Link { return tray.NewTray(program, onAction, d.Log) }, nil
}

// startTray shows the daemon's state through open and does what the user chooses; it returns what ends it.
func (d *daemon) startTray(open func(onAction func(tray.Action)) tray.Link) func() {
	logFile := ""
	if d.layout != nil {
		logFile = d.layout.LogFile
	}
	sessions := func() int { return d.hub.Size() + d.browsers.Size() }
	port := 0
	if d.gateway != nil {
		port = d.gateway.Port
	}
	return tray.StartMenu(tray.MenuDeps{
		Open:         open,
		RegistryFile: d.file,
		Hello:        d.trayHello,
		Linked:       d.hub.Has,
		Sessions:     sessions,
		GatewayPort:  port,
		LogFile:      logFile,
		Stop:         d.stop,
		Restart:      func() { d.restartRequested("the tray menu", sessions()) },
		Disconnect:   d.disconnectOrigin,
		Updates:      func() bool { return d.settings.Get().Updates },
		Updatable:    d.updates.updatable,
		SetUpdates: func(on bool) error {
			if _, err := d.settings.Change(home.SettingsPatch{Updates: &on}); err != nil {
				return err
			}
			d.updates.turn(on)
			return nil
		},
		Log: d.Log,
	})
}

func (d *daemon) trayHello() tray.Hello {
	return tray.Hello{Version: home.Version, Managed: home.IsManaged(d.env), Agents: d.pageHello().Agents}
}
