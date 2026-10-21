// Package registry is what the daemon remembers between runs: the apps it is paired with and the web page origins the
// user allowed (daemon.json), how that file is read and written, `agentd pair` and `agentd apps`.
//
// The value operations of this file are pure: a Registry is never changed, an edit returns a new one.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// BrowserGrantTTL is how long the grant of a web page that names no end lasts, counted from the day it was made: the
// grants made before the user chose a time. A grant made now carries its own end (Expiry).
const BrowserGrantTTL = 30 * 24 * time.Hour

// Never is the expiresAt of access the user gave with no end. An app that names no end has none either.
const Never = "never"

// grantStampLayout is how grantedAt is written: the form `new Date().toISOString()` gave in the Node daemon's file.
const grantStampLayout = "2006-01-02T15:04:05.000Z"

// GrantStamp is grantedAt for a grant made at `at`.
func GrantStamp(at time.Time) string { return at.UTC().Format(grantStampLayout) }

// Expiry is the expiresAt of access given at `at` for `lasts`: Never when lasts is not positive.
func Expiry(at time.Time, lasts time.Duration) string {
	if lasts <= 0 {
		return Never
	}
	return at.Add(lasts).UTC().Format(time.RFC3339Nano)
}

// endOf reads an expiresAt: ends is false for access with no end; ok is false when the stamp is not a time, which
// access cannot be told young by.
func endOf(stamp string) (end time.Time, ends, ok bool) {
	if stamp == "" || stamp == Never {
		return time.Time{}, false, true
	}
	end, err := time.Parse(time.RFC3339Nano, stamp)
	return end, true, err == nil
}

func liveAt(end time.Time, ends, ok bool, now time.Time) bool {
	return ok && (!ends || now.Before(end))
}

// Access is what the user gave one origin when they allowed it: the name it gave itself, the folders agents may start
// in (none is no limit) and when it ends (see Expiry; empty is the end of the grants made before the user chose one).
type Access struct {
	Name      string
	Folders   []string
	ExpiresAt string
}

// App is a server app this daemon is paired with: it dials out to Server with Token. Name is what the app called
// itself when it connected. Folders are the folders the user allowed it (see agents.GuardLine); none is no limit, as
// for an app paired from the command line. ExpiresAt is when the access ends; an app that names none (paired from the
// command line, or before the user chose a time) has no end.
type App struct {
	Server    string   `json:"server"`
	Token     string   `json:"token"`
	Name      string   `json:"name,omitempty"`
	Folders   []string `json:"folders,omitempty"`
	ExpiresAt string   `json:"expiresAt,omitempty"`
}

// End is when the app's access ends; see endOf.
func (a App) End() (end time.Time, ends, ok bool) { return endOf(a.ExpiresAt) }

// Live reports whether the app's access has not ended at now. An app whose end cannot be read has.
func (a App) Live(now time.Time) bool {
	end, ends, ok := a.End()
	return liveAt(end, ends, ok, now)
}

// BrowserGrant is a web page origin the user allowed to run agents through the local gateway, in Folders (none is no
// limit, as for a grant that was made before folders existed), until ExpiresAt, or for BrowserGrantTTL after GrantedAt
// when it names no end.
type BrowserGrant struct {
	Origin    string   `json:"origin"`
	GrantedAt string   `json:"grantedAt"`
	Name      string   `json:"name,omitempty"`
	Folders   []string `json:"folders,omitempty"`
	ExpiresAt string   `json:"expiresAt,omitempty"`
}

// End is when the grant lapses; see endOf. A grant that names no end lapses BrowserGrantTTL after GrantedAt, and one
// whose GrantedAt cannot be read as a time is not ok.
func (g BrowserGrant) End() (end time.Time, ends, ok bool) {
	if g.ExpiresAt != "" {
		return endOf(g.ExpiresAt)
	}
	at, err := time.Parse(time.RFC3339Nano, g.GrantedAt)
	return at.Add(BrowserGrantTTL), true, err == nil
}

// Live reports whether the grant has not lapsed at now. A grant whose time cannot be read has.
func (g BrowserGrant) Live(now time.Time) bool {
	end, ends, ok := g.End()
	return liveAt(end, ends, ok, now)
}

// Registry is what daemon.json holds. The two lists are never nil in a value this package returns.
type Registry struct {
	Apps     []App          `json:"apps"`
	Browsers []BrowserGrant `json:"browsers"`
}

// Empty is the registry of a daemon with nothing paired.
func Empty() Registry {
	return Registry{Apps: []App{}, Browsers: []BrowserGrant{}}
}

const errNoApps = "has no apps list"

// damaged is the error of a registry file that cannot be read back, as Load gives it. `pair` reads the file before it
// writes it, so pairing again fails the same way: the way out is to move the file aside and connect afresh.
func damaged(file string) error {
	return fmt.Errorf("%s is damaged. Move it aside (mv %s %s), then connect again from the app's page.", file, shellWord(file), shellWord(file+".broken"))
}

// shellWord is s as one word of a shell command: as it is when nothing in it needs quoting, else in single quotes.
func shellWord(s string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !plain(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Parse reads daemon.json: {"apps":[{"server","token"}],"browsers":[{"origin","grantedAt"}]}. A missing or null
// "browsers" is an empty list. The error says what is wrong in words that continue a sentence starting with the
// file's name; Load does not show it, it says the file is damaged.
func Parse(text string) (Registry, error) {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return Registry{}, errors.New("is not valid JSON")
	}
	obj, _ := doc.(map[string]any)
	rawApps, ok := obj["apps"].([]any)
	if !ok {
		return Registry{}, errors.New(errNoApps)
	}
	r := Empty()
	for _, v := range rawApps {
		app, ok := parseApp(v)
		if !ok {
			return Registry{}, errors.New(errNoApps)
		}
		r.Apps = append(r.Apps, app)
	}
	if obj["browsers"] == nil {
		return r, nil
	}
	rawBrowsers, ok := obj["browsers"].([]any)
	if !ok {
		return Registry{}, errors.New(errNoApps)
	}
	for _, v := range rawBrowsers {
		grant, ok := parseGrant(v)
		if !ok {
			return Registry{}, errors.New(errNoApps)
		}
		r.Browsers = append(r.Browsers, grant)
	}
	return r, nil
}

func parseApp(v any) (App, bool) {
	o, _ := v.(map[string]any)
	server, okServer := o["server"].(string)
	token, okToken := o["token"].(string)
	folders, okFolders := parseFolders(o["folders"])
	name, okName := parseText(o["name"])
	expiresAt, okEnd := parseText(o["expiresAt"])
	return App{Server: server, Token: token, Name: name, Folders: folders, ExpiresAt: expiresAt},
		okServer && okToken && token != "" && okFolders && okName && okEnd
}

// parseText reads an optional text field: a missing or null one is empty; anything but text is malformed.
func parseText(v any) (string, bool) {
	if v == nil {
		return "", true
	}
	text, ok := v.(string)
	return text, ok
}

// parseFolders reads an optional "folders": a missing or null one is none; anything but a list of text is malformed.
func parseFolders(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var folders []string
	for _, item := range list {
		folder, ok := item.(string)
		if !ok {
			return nil, false
		}
		folders = append(folders, folder)
	}
	return folders, true
}

func parseGrant(v any) (BrowserGrant, bool) {
	o, _ := v.(map[string]any)
	origin, okOrigin := o["origin"].(string)
	grantedAt, okAt := o["grantedAt"].(string)
	folders, okFolders := parseFolders(o["folders"])
	name, okName := parseText(o["name"])
	expiresAt, okEnd := parseText(o["expiresAt"])
	return BrowserGrant{Origin: origin, GrantedAt: grantedAt, Name: name, Folders: folders, ExpiresAt: expiresAt},
		okOrigin && okAt && okFolders && okName && okEnd
}

// Marshal is the bytes of daemon.json: two-space indent, one trailing newline, "<", ">" and "&" as they are. A CLI
// install and this one read each other's file.
func (r Registry) Marshal() []byte {
	if r.Apps == nil {
		r.Apps = []App{}
	}
	if r.Browsers == nil {
		r.Browsers = []BrowserGrant{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		panic(err) // strings and slices of strings always encode
	}
	return b.Bytes()
}

// AddApp adds the app, or replaces the token of the app with the same server; the order of first pairing stays.
func (r Registry) AddApp(app App) Registry {
	apps := slices.Clone(r.Apps)
	if at := slices.IndexFunc(apps, func(a App) bool { return a.Server == app.Server }); at < 0 {
		apps = append(apps, app)
	} else {
		apps[at] = app
	}
	return Registry{Apps: apps, Browsers: r.Browsers}
}

// AddBrowser grants the origin its access, from `at`. An origin whose grant is still live at `at` keeps its first
// access; one that lapsed gets the new access in the place of the old grant.
func (r Registry) AddBrowser(origin string, at time.Time, a Access) Registry {
	if r.HasBrowser(origin, at) {
		return r
	}
	grant := BrowserGrant{Origin: origin, GrantedAt: GrantStamp(at), Name: a.Name, Folders: a.Folders, ExpiresAt: a.ExpiresAt}
	browsers := slices.Clone(r.Browsers)
	if i := slices.IndexFunc(browsers, func(g BrowserGrant) bool { return g.Origin == origin }); i >= 0 {
		browsers[i] = grant
	} else {
		browsers = append(browsers, grant)
	}
	return Registry{Apps: r.Apps, Browsers: browsers}
}

// RemoveOrigin drops the app whose server is origin and the browser grant of origin.
func (r Registry) RemoveOrigin(origin string) Registry {
	out := Empty()
	for _, a := range r.Apps {
		if a.Server != origin {
			out.Apps = append(out.Apps, a)
		}
	}
	for _, g := range r.Browsers {
		if g.Origin != origin {
			out.Browsers = append(out.Browsers, g)
		}
	}
	return out
}

// HasApp reports whether the registry pairs an app with this server origin at now: it has one whose access has not
// ended.
func (r Registry) HasApp(server string, now time.Time) bool {
	return slices.ContainsFunc(r.Apps, func(a App) bool { return a.Server == server && a.Live(now) })
}

// HasSavedApp reports whether the registry holds an app with this server origin, ended or not: what `agentd apps
// remove` can forget.
func (r Registry) HasSavedApp(server string) bool {
	return slices.ContainsFunc(r.Apps, func(a App) bool { return a.Server == server })
}

// HasBrowser reports whether the registry grants this page origin at now: it has a grant that has not lapsed.
func (r Registry) HasBrowser(origin string, now time.Time) bool {
	return slices.ContainsFunc(r.Browsers, func(g BrowserGrant) bool { return g.Origin == origin && g.Live(now) })
}

// HasGrant reports whether the registry holds a grant of this page origin, lapsed or not: what `agentd apps remove`
// can forget.
func (r Registry) HasGrant(origin string) bool {
	return slices.ContainsFunc(r.Browsers, func(g BrowserGrant) bool { return g.Origin == origin })
}

// HasLiveBrowsers reports whether any page origin is granted at now.
func (r Registry) HasLiveBrowsers(now time.Time) bool {
	return slices.ContainsFunc(r.Browsers, func(g BrowserGrant) bool { return g.Live(now) })
}

// AppFolders are the folders of the app with this server origin; none is no limit.
func (r Registry) AppFolders(server string) []string {
	for _, a := range r.Apps {
		if a.Server == server {
			return a.Folders
		}
	}
	return nil
}

// BrowserFolders are the folders of the page origin's grant, lapsed or not: a session that is still open when its grant
// lapses stays inside them until the daemon ends it. None is no limit.
func (r Registry) BrowserFolders(origin string) []string {
	for _, g := range r.Browsers {
		if g.Origin == origin {
			return g.Folders
		}
	}
	return nil
}

// Ended is what keeps running for origins a registry no longer has.
type Ended struct {
	Apps  []string
	Pages []string
}

// Withdrawn is what still runs for an origin that r no longer has at now, as `agentd apps remove` leaves it or access
// that ended: of the linked app servers and the origins with open pages, those to end.
func (r Registry) Withdrawn(linked, pages []string, now time.Time) Ended {
	out := Ended{Apps: []string{}, Pages: []string{}}
	for _, s := range linked {
		if !r.HasApp(s, now) {
			out.Apps = append(out.Apps, s)
		}
	}
	for _, o := range pages {
		if !r.HasBrowser(o, now) {
			out.Pages = append(out.Pages, o)
		}
	}
	return out
}

// SelectApps is every app whose access has not ended at now, or with a non-nil server only the app at that origin. It
// fails when that app is not paired.
func (r Registry) SelectApps(server *string, now time.Time) ([]App, error) {
	live := make([]App, 0, len(r.Apps))
	for _, a := range r.Apps {
		if a.Live(now) {
			live = append(live, a)
		}
	}
	if server == nil {
		return live, nil
	}
	origin, err := home.ServerOrigin(*server)
	if err != nil {
		return nil, err
	}
	var found []App
	for _, a := range live {
		if a.Server == origin {
			found = append(found, a)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("not paired with %s. Pair first: agentd pair --app %s --code <code>", origin, origin)
	}
	return found, nil
}

// SelectApp is the one app a process (the MCP bridge) talks to: the named one, or the only one.
func (r Registry) SelectApp(server *string, now time.Time) (App, error) {
	apps, err := r.SelectApps(server, now)
	if err != nil {
		return App{}, err
	}
	if len(apps) == 0 {
		return App{}, errors.New("not paired. Run: agentd pair --app <url> --code <code>")
	}
	if server == nil && len(apps) > 1 {
		return App{}, errors.New("several apps are paired: pass --app <url> to say which one")
	}
	return apps[0], nil
}
