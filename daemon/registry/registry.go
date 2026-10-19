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

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// App is a server app this daemon is paired with: it dials out to Server with Token.
type App struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// BrowserGrant is a web page origin the user allowed to run agents through the local gateway.
type BrowserGrant struct {
	Origin    string `json:"origin"`
	GrantedAt string `json:"grantedAt"`
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

const errNoApps = "has no apps list. Pair again: agentd pair --app <url> --code <code>"

// Parse reads daemon.json: {"apps":[{"server","token"}],"browsers":[{"origin","grantedAt"}]}. A missing or null
// "browsers" is an empty list. The error continues a sentence that starts with the file's name.
func Parse(text string) (Registry, error) {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return Registry{}, errors.New("is not valid JSON. Pair again: agentd pair --app <url> --code <code>")
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
	return App{Server: server, Token: token}, okServer && okToken && token != ""
}

func parseGrant(v any) (BrowserGrant, bool) {
	o, _ := v.(map[string]any)
	origin, okOrigin := o["origin"].(string)
	grantedAt, okAt := o["grantedAt"].(string)
	return BrowserGrant{Origin: origin, GrantedAt: grantedAt}, okOrigin && okAt
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

// AddBrowser grants the origin; an origin that is already granted keeps its first grantedAt.
func (r Registry) AddBrowser(origin, grantedAt string) Registry {
	if r.HasBrowser(origin) {
		return r
	}
	browsers := append(slices.Clone(r.Browsers), BrowserGrant{Origin: origin, GrantedAt: grantedAt})
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

// HasApp reports whether the registry holds an app with this server origin.
func (r Registry) HasApp(server string) bool {
	return slices.ContainsFunc(r.Apps, func(a App) bool { return a.Server == server })
}

// HasBrowser reports whether the registry grants this page origin.
func (r Registry) HasBrowser(origin string) bool {
	return slices.ContainsFunc(r.Browsers, func(g BrowserGrant) bool { return g.Origin == origin })
}

// Ended is what keeps running for origins a registry no longer has.
type Ended struct {
	Apps  []string
	Pages []string
}

// Withdrawn is what still runs for an origin that r no longer has, as `agentd apps remove` leaves it: of the linked
// app servers and the origins with open pages, those to end.
func (r Registry) Withdrawn(linked, pages []string) Ended {
	out := Ended{Apps: []string{}, Pages: []string{}}
	for _, s := range linked {
		if !r.HasApp(s) {
			out.Apps = append(out.Apps, s)
		}
	}
	for _, o := range pages {
		if !r.HasBrowser(o) {
			out.Pages = append(out.Pages, o)
		}
	}
	return out
}

// SelectApps is every app, or with a non-nil server only the app at that origin. It fails when that app is not paired.
func (r Registry) SelectApps(server *string) ([]App, error) {
	if server == nil {
		return r.Apps, nil
	}
	origin, err := home.ServerOrigin(*server)
	if err != nil {
		return nil, err
	}
	var found []App
	for _, a := range r.Apps {
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
func (r Registry) SelectApp(server *string) (App, error) {
	apps, err := r.SelectApps(server)
	if err != nil {
		return App{}, err
	}
	if len(apps) == 0 {
		return App{}, errors.New("not paired. Run: agentd pair --app <url> --code <code>")
	}
	if server == nil && len(r.Apps) > 1 {
		return App{}, errors.New("several apps are paired: pass --app <url> to say which one")
	}
	return apps[0], nil
}
