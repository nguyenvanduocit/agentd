package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// foldersNote is what a listing says of a grant's folders: where it may work, or that nothing limits it.
func foldersNote(folders []string) string {
	if len(folders) == 0 {
		return ", any folder"
	}
	return ", in " + strings.Join(folders, ", ")
}

const grantedDateRunes = 10 // "2026-10-19" of an ISO timestamp

// endNote is what a listing says of access's end: when it ends, that it never does, or that it ended.
func endNote(end time.Time, ends, ok, live bool) string {
	switch {
	case !ok:
		return "expired"
	case !ends:
		return "no end"
	case !live:
		return "expired " + end.UTC().Format(time.DateOnly)
	}
	return "expires " + end.UTC().Format(time.DateOnly)
}

// appNote is what a listing says of an app's end: nothing for an app that names none (the way it was before the user
// chose a time), else the end.
func appNote(a App, now time.Time) string {
	if a.ExpiresAt == "" {
		return ""
	}
	end, ends, ok := a.End()
	return ", " + endNote(end, ends, ok, a.Live(now))
}

// browserNote is what a listing says of a page grant's time: when it was made and ends, or that it lapsed.
func browserNote(g BrowserGrant, now time.Time) string {
	end, ends, ok := g.End()
	note := endNote(end, ends, ok, g.Live(now))
	if !g.Live(now) {
		return note
	}
	day := []rune(g.GrantedAt)
	return fmt.Sprintf("granted %s, %s", string(day[:min(len(day), grantedDateRunes)]), note)
}

// label is the name an app gave itself in front of its origin, or the origin alone when it gave none.
func label(origin, name string) string {
	if name == "" {
		return origin
	}
	return name + " (" + origin + ")"
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
			log.Say(label(a.Server, a.Name) + "  app" + appNote(a, now) + foldersNote(a.Folders))
		}
		for _, g := range r.Browsers {
			log.Say(fmt.Sprintf("%s  browser, %s%s", label(g.Origin, g.Name), browserNote(g, now), foldersNote(g.Folders)))
		}
		return 0, nil
	}
	origin, err := home.ServerOrigin(cmd.Origin)
	if err != nil {
		return 0, err
	}
	if !r.HasSavedApp(origin) && !r.HasGrant(origin) {
		log.Warn("not connected to " + origin)
		return 1, nil
	}
	if err := Save(file, r.RemoveOrigin(origin)); err != nil {
		return 0, err
	}
	log.Say(fmt.Sprintf("disconnected %s; a running daemon ends its sessions within a few seconds", origin))
	return 0, nil
}
