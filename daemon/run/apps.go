package run

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// registryCheckEvery is how often the daemon reads its registry for an origin that another process took away.
const registryCheckEvery = 2 * time.Second

// The daemon tells its apps, its pages and its tray the same facts in three shapes.

func (d *daemon) statuses() []agents.AgentStatus {
	find := func(program string) (string, bool) { return home.FindProgram(program, d.agentEnv) }
	return agents.AllStatuses(d.agentEnv, find, d.store.State)
}

// hello is what the daemon tells an app on every (re)connect.
func (d *daemon) hello() contract.Hello {
	machine := registry.CurrentMachine()
	statuses := d.statuses()
	infos := make([]contract.AgentInfo, len(statuses))
	for i, s := range statuses {
		infos[i] = contract.AgentInfo{Name: s.Name, Title: s.Title, Ready: s.Ready, Reason: s.Reason}
	}
	return contract.Hello{Version: home.Version, Hostname: machine.Hostname, Os: machine.OS, Arch: machine.Arch, Agents: infos, Managed: home.IsManaged(d.env)}
}

// pageHello is what the daemon tells a page when its socket opens.
func (d *daemon) pageHello() pagetools.Hello { return pagetools.NewHello(home.Version, d.statuses()) }

func (d *daemon) agentInfos() []contract.AgentInfo { return d.hello().Agents }

// gatewayDeps is what the local gateway does on this daemon: the registry file is the truth, the hub the running apps.
func (d *daemon) gatewayDeps() gateway.Deps {
	return gateway.Deps{
		Version:  home.Version,
		Agents:   func() []pagetools.AgentInfo { return d.pageHello().Agents },
		Registry: func() (registry.Registry, error) { return registry.Load(d.file) },
		Ask:      d.ask,
		Linked:   d.hub.Has,
		ConnectServer: func(server, code string, folders []string) error {
			app, err := registry.Pair(context.Background(), http.DefaultClient, registry.CurrentMachine(), server, code, d.file, folders)
			if err != nil {
				return err
			}
			// Pairing again replaced the saved token: the running link, if there is one, still holds the old one.
			d.hub.Detach(server, true)
			return d.hub.Add(app)
		},
		GrantBrowser: func(origin string, folders []string) error {
			_, err := registry.Update(d.file, func(r registry.Registry) registry.Registry {
				return r.AddBrowser(origin, time.Now(), folders)
			})
			return err
		},
		Revoke:  d.disconnectOrigin,
		Upgrade: d.browsers.Upgrade,
		Acp:     d.acp.Serve,
		// The token a program on this computer presents to use /acp, read at each request.
		AcpToken: d.acpToken,
	}
}

// disconnectOrigin forgets the app and the browser grant of origin and ends their sessions; the daemon stays up so it
// can be connected again.
func (d *daemon) disconnectOrigin(origin string) error {
	if _, err := registry.Update(d.file, func(r registry.Registry) registry.Registry { return r.RemoveOrigin(origin) }); err != nil {
		return err
	}
	d.browsers.EndOrigin(origin)
	d.acp.EndOrigin(origin)
	d.hub.Detach(origin, true)
	return nil
}

// registryFollower ends what still runs for an origin that the registry no longer has, as it is now: removed by another
// process (`agentd apps remove`), or a page's grant that lapsed. It ends their link and pages' sockets, as Disconnect
// in the menu does, and the daemon stays up. It also tells a problem of the file once, not at every check.
type registryFollower struct {
	file    string
	log     *proc.Logger
	now     func() time.Time
	servers func() []string // the linked app servers
	pages   func() []string // the origins with an open page socket
	endPage func(origin string)
	endApp  func(server string)

	problem string // the last problem of the file that was told, "" when it read fine
}

func (d *daemon) newRegistryFollower() *registryFollower {
	return &registryFollower{
		file:    d.file,
		log:     d.Log,
		now:     time.Now,
		servers: d.hub.Servers,
		pages:   d.pageOrigins,
		endPage: d.endPageOrigin,
		endApp:  func(server string) { d.hub.Detach(server, true) },
	}
}

// pageOrigins are the web pages with an open socket of either kind (/v1/session or /acp).
func (d *daemon) pageOrigins() []string {
	origins := d.browsers.Origins()
	for _, o := range d.acp.Origins() {
		if !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	return origins
}

// endPageOrigin ends the sockets of a web page of either kind.
func (d *daemon) endPageOrigin(origin string) {
	d.browsers.EndOrigin(origin)
	d.acp.EndOrigin(origin)
}

func (f *registryFollower) check() {
	// What runs is taken before the file is read: an app or a page is saved before it is linked, so one linked by then
	// is in the file unless it was removed, and one paired while the file is read is not judged on old content.
	linked := f.servers()
	pages := f.pages()
	reg, err := registry.Load(f.file)
	if err != nil {
		if text := err.Error(); text != f.problem {
			f.problem = text
			f.log.Warn(text)
		}
		return
	}
	f.problem = ""
	gone := reg.Withdrawn(linked, pages, f.now())
	for _, origin := range gone.Pages {
		if reg.HasGrant(origin) {
			f.log.Say(fmt.Sprintf("%s access lapsed after %d days: ending its pages' sessions", origin, int(registry.BrowserGrantTTL.Hours()/24)))
		} else {
			f.log.Say(origin + " was disconnected: ending its pages' sessions")
		}
		f.endPage(origin)
	}
	for _, server := range gone.Apps {
		f.log.Say(server + " was disconnected: ending its link and sessions")
		f.endApp(server)
	}
}
