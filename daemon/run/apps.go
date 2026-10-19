package run

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagemcp"
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
func (d *daemon) pageHello() pagemcp.Hello { return pagemcp.NewHello(home.Version, d.statuses()) }

func (d *daemon) agentInfos() []contract.AgentInfo { return d.hello().Agents }

// gatewayDeps is what the local gateway does on this daemon: the registry file is the truth, the hub the running apps.
func (d *daemon) gatewayDeps() gateway.Deps {
	return gateway.Deps{
		Version:  home.Version,
		Agents:   func() []pagemcp.AgentInfo { return d.pageHello().Agents },
		Registry: func() (registry.Registry, error) { return registry.Load(d.file) },
		Ask:      func(q consent.Question) consent.Answer { return consent.Ask(q, d.env, consent.TimeoutS) },
		Linked:   d.hub.Has,
		ConnectServer: func(server, code string) error {
			app, err := registry.Pair(context.Background(), http.DefaultClient, registry.CurrentMachine(), server, code, d.file)
			if err != nil {
				return err
			}
			// Pairing again replaced the saved token: the running link, if there is one, still holds the old one.
			d.hub.Detach(server, true)
			return d.hub.Add(app)
		},
		GrantBrowser: func(origin string) error {
			_, err := registry.Update(d.file, func(r registry.Registry) registry.Registry {
				return r.AddBrowser(origin, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
			})
			return err
		},
		Revoke:  d.disconnectOrigin,
		Upgrade: d.browsers.Upgrade,
	}
}

// disconnectOrigin forgets the app and the browser grant of origin and ends their sessions; the daemon stays up so it
// can be connected again.
func (d *daemon) disconnectOrigin(origin string) error {
	if _, err := registry.Update(d.file, func(r registry.Registry) registry.Registry { return r.RemoveOrigin(origin) }); err != nil {
		return err
	}
	d.browsers.EndOrigin(origin)
	d.hub.Detach(origin, true)
	return nil
}

// followRegistry ends what still runs for an origin that another process removed from the registry (`agentd apps
// remove`): its link and its pages' sockets, as Disconnect in the menu does. The daemon stays up.
func (d *daemon) followRegistry() {
	// What runs is taken before the file is read: an app or a page is saved before it is linked, so one linked by then
	// is in the file unless it was removed, and one paired while the file is read is not judged on old content.
	linked := d.hub.Servers()
	pages := d.browsers.Origins()
	reg, err := registry.Load(d.file)
	if err != nil {
		d.Log.Warn(fmt.Sprintf("cannot read %s: %s", d.file, err))
		return
	}
	gone := reg.Withdrawn(linked, pages)
	for _, origin := range gone.Pages {
		d.Log.Say(origin + " was disconnected: ending its pages' sessions")
		d.browsers.EndOrigin(origin)
	}
	for _, server := range gone.Apps {
		d.Log.Say(server + " was disconnected: ending its link and sessions")
		d.hub.Detach(server, true)
	}
}
