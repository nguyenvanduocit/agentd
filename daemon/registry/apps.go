package registry

import (
	"fmt"
	"strings"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const grantedDateRunes = 10 // "2026-10-19" of an ISO timestamp

// foldersNote is what a listing says of a grant's folders: where it may work, or that nothing limits it.
func foldersNote(folders []string) string {
	if len(folders) == 0 {
		return ", any folder"
	}
	return ", in " + strings.Join(folders, ", ")
}

// RunApps is `apps [list]` and `apps remove <origin>`. It returns the exit code, meaningful when the error is nil.
// Tokens are never printed.
func RunApps(cmd cli.Apps, h home.Host, log *proc.Logger) (int, error) {
	file := File(cmd.Config, h)
	r, err := Load(file)
	if err != nil {
		return 0, err
	}
	if cmd.Action == cli.AppsList {
		if len(r.Apps) == 0 && len(r.Browsers) == 0 {
			log.Say("Nothing is connected. An app asks to connect from its own page.")
			return 0, nil
		}
		for _, a := range r.Apps {
			log.Say(a.Server + "  app" + foldersNote(a.Folders))
		}
		for _, g := range r.Browsers {
			day := []rune(g.GrantedAt)
			log.Say(fmt.Sprintf("%s  browser, granted %s%s", g.Origin, string(day[:min(len(day), grantedDateRunes)]), foldersNote(g.Folders)))
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
