package registry

import (
	"fmt"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const grantedDateRunes = 10 // "2026-10-19" of an ISO timestamp

// RunApps is `apps [list]` and `apps remove <origin>`. It returns the exit code, meaningful when the error is nil.
// Tokens are never printed.
func RunApps(cmd cli.Apps, h home.Host, log *proc.Logger) (int, error) {
	file := File(cmd.Config, h)
	r, err := Load(file)
	if err != nil {
		return 0, err
	}
	if cmd.Action == cli.AppsList {
		for _, a := range r.Apps {
			log.Say(a.Server + "  app")
		}
		for _, g := range r.Browsers {
			day := []rune(g.GrantedAt)
			log.Say(fmt.Sprintf("%s  browser, granted %s", g.Origin, string(day[:min(len(day), grantedDateRunes)])))
		}
		return 0, nil
	}
	origin, err := home.ServerOrigin(cmd.Origin)
	if err != nil {
		return 0, err
	}
	if !r.HasApp(origin) && !r.HasBrowser(origin) {
		log.Warn("not connected to " + origin)
		return 1, nil
	}
	if err := Save(file, r.RemoveOrigin(origin)); err != nil {
		return 0, err
	}
	log.Say(fmt.Sprintf("disconnected %s; a running daemon ends its sessions within a few seconds", origin))
	return 0, nil
}
