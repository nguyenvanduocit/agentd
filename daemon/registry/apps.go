package registry

import (
	"fmt"
	"strings"
	"time"

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

// browserNote is what a listing says of a page grant's time: when it was made and ends, or that it lapsed.
func browserNote(g BrowserGrant, now time.Time) string {
	end, ok := g.ExpiresAt()
	switch {
	case !ok:
		return "expired"
	case !g.Live(now):
		return "expired " + end.UTC().Format(time.DateOnly)
	}
	day := []rune(g.GrantedAt)
	return fmt.Sprintf("granted %s, expires %s", string(day[:min(len(day), grantedDateRunes)]), end.UTC().Format(time.DateOnly))
}

// RunApps is RunAppsAt at the present time.
func RunApps(cmd cli.Apps, h home.Host, log *proc.Logger) (int, error) {
	return RunAppsAt(cmd, h, log, time.Now())
}

// RunAppsAt is `apps [list]` and `apps remove <origin>` at time now. It returns the exit code, meaningful when the error
// is nil. Tokens are never printed.
func RunAppsAt(cmd cli.Apps, h home.Host, log *proc.Logger, now time.Time) (int, error) {
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
			log.Say(fmt.Sprintf("%s  browser, %s%s", g.Origin, browserNote(g, now), foldersNote(g.Folders)))
		}
		return 0, nil
	}
	origin, err := home.ServerOrigin(cmd.Origin)
	if err != nil {
		return 0, err
	}
	if !r.HasApp(origin) && !r.HasGrant(origin) {
		log.Warn("not connected to " + origin)
		return 1, nil
	}
	if err := Save(file, r.RemoveOrigin(origin)); err != nil {
		return 0, err
	}
	log.Say(fmt.Sprintf("disconnected %s; a running daemon ends its sessions within a few seconds", origin))
	return 0, nil
}
