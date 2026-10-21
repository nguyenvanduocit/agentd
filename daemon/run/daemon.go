// Package run is `agentd run`: the daemon that serves agents to the apps a machine is paired with and to the web pages
// it allowed, until it is stopped (SIGTERM, SIGINT, a Stop from an app or from agentd.app), left by its last app or
// updated.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/link"
	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

const (
	// shutdownGrace is how long the pages' agents and the apps' agents get to end before they are killed.
	shutdownGrace = 2 * time.Second
)

// Deps is what the daemon touches outside its own logic.
type Deps struct {
	Log *proc.Logger
	// Host is the machine and its environment (the process's, in the entry).
	Host home.Host
	// Tracker holds the process groups that must not outlive the daemon; nil makes the daemon's own.
	Tracker *proc.Tracker
	// Executable is the real path of the agentd binary: the pages' and the apps' MCP wrappers run it, and the
	// installed layout is found beside it. Empty is this process's own.
	Executable string
	// UI opens the link to what shows the daemon's state and asks the user: agentd.app, which runs the daemon in its own
	// process. It calls onAction with each choice of the user. Nil: nothing shows the state, and no question is asked.
	UI func(onAction func(ui.Action)) ui.Link
}

// daemon is one run: what it started, so that the way out can end it in order.
type daemon struct {
	Deps
	env      home.Env // the daemon's environment
	agentEnv home.Env // env with the wrappers' directory first on PATH: what the agents' lookups see
	exe      string
	node     string
	file     string
	cwd      string
	root     string
	bin      string

	stopCh chan int

	state    *proc.SessionState
	store    *agents.Store
	browsers *gateway.BrowserSessions
	acp      *gateway.AcpSessions // the standard ACP endpoint's connections (/acp)
	acp0     acpState             // its token and the record of where it is (acp.go)
	hub      *link.AppHub
	gateway  *gateway.Gateway
	stopUI   func()
	// ui is the link to the app that shows the daemon's state, once it is started: what the
	// daemon asks the user through (see ask).
	ui atomic.Pointer[ui.Link]

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bg       sync.WaitGroup
}

// Daemon runs the daemon until it is stopped (ctx ends, SIGTERM and SIGINT in the entry), left by its last app or
// updated, and returns the exit code: 0 stays stopped, 75 starts again, 1 is a failure (told on stderr).
func Daemon(ctx context.Context, cmd cli.Run, deps Deps) int {
	if deps.Tracker == nil {
		deps.Tracker = proc.NewTracker()
	}
	deps.Log.StampLines()
	lock, stayOut := takeHome(deps, time.Now())
	if stayOut {
		return home.ExitStopped
	}
	defer func() { _ = lock.Close() }() // after the shutdown below: the home is free only once the agents are gone
	d := &daemon{Deps: deps, stopCh: make(chan int, 1)}
	d.bgCtx, d.bgCancel = context.WithCancel(context.Background())
	defer d.bgCancel()
	code, err := d.run(ctx, cmd)
	if err != nil {
		d.shutdown()
		d.Log.Warn(err.Error())
		return 1
	}
	d.shutdown()
	return code
}

// stop settles the exit code; the first call wins.
func (d *daemon) stop(code int) {
	select {
	case d.stopCh <- code:
	default:
	}
}

func (d *daemon) run(ctx context.Context, cmd cli.Run) (int, error) {
	d.Log.StampLines()
	d.file = registry.File(cmd.Config, d.Host)
	reg, err := registry.Load(d.file)
	if err != nil {
		return 0, err
	}
	apps, err := reg.SelectApps(cmd.App, time.Now())
	if err != nil {
		return 0, err
	}
	if d.cwd, err = workDir(cmd.Dir); err != nil {
		return 0, err
	}
	port, err := gateway.GatewayPort(d.Host.Env)
	if err != nil {
		return 0, err
	}
	if d.exe, err = executable(d.Executable); err != nil {
		return 0, err
	}
	if d.root, d.bin, err = makeTempDirs(); err != nil {
		return 0, err
	}
	d.state = proc.NewSessionState(home.SessionsFile(d.Host), d.bin, d.Log)
	d.state.Start()

	d.setEnvironment()
	// The adapters install in the background from now on, whatever the servers do with their sessions.
	d.store = agents.NewStore(agents.StoreOptions{Layout: home.LayoutOf(home.AgentdHome(d.Host)), Env: d.env, Log: d.Log, Tracker: d.Tracker})
	d.store.Start()

	if err := d.startBrowserSessions(); err != nil {
		return 0, err
	}
	d.startHub()
	d.store.OnChange(func() {
		d.hub.AgentsChanged(d.agentInfos())
		d.writeAcpInfo()
	})
	for _, app := range apps {
		if err := d.hub.Add(app); err != nil {
			return 0, err
		}
	}
	if d.gateway, err = d.startGateway(port); err != nil {
		return 0, err
	}
	d.publishAcp()
	d.announce(len(apps))
	if err := d.startExtras(); err != nil {
		return 0, err
	}

	select {
	case code := <-d.stopCh:
		return code, nil
	case <-ctx.Done():
		return home.ExitStopped, nil
	}
}

// shutdown ends what runs. The apps stop taking opens first. Their agents and the pages' sessions then end side by
// side, while the adapter installs and the extras (the UI link, registry checks) stop; the gateway closes after
// both, and the record of the agents is cleared last (it stays when an agent still runs, for the next start). Whatever
// did not start is skipped.
func (d *daemon) shutdown() {
	if d.hub != nil {
		d.hub.Drain()
	}
	if d.acp != nil {
		d.acp.Drain()
	}
	var ending sync.WaitGroup
	if d.hub != nil {
		ending.Add(1)
		go func() {
			defer ending.Done()
			d.hub.Shutdown(shutdownGrace)
		}()
	}
	if d.browsers != nil {
		ending.Add(1)
		go func() {
			defer ending.Done()
			d.browsers.Shutdown(shutdownGrace)
		}()
	}
	if d.acp != nil {
		ending.Add(1)
		go func() {
			defer ending.Done()
			d.acp.Shutdown(shutdownGrace)
		}()
	}
	if d.store != nil {
		d.store.Stop()
	}
	d.stopExtras()
	ending.Wait()
	if d.gateway != nil {
		_ = d.gateway.Close()
	}
	d.removeAcpInfo()
	if d.state != nil {
		d.state.Clear()
	}
	if d.root != "" {
		_ = os.RemoveAll(d.root)
	}
}

// workDir is where the agents run: --dir, or where the daemon was started. It must be a directory.
func workDir(dir *string) (string, error) {
	var cwd string
	if dir != nil {
		cwd = *dir
	} else {
		var err error
		// The kernel's answer, not $PWD's: a symlinked directory is named by its target, as process.cwd() does.
		if cwd, err = syscall.Getwd(); err != nil {
			return "", fmt.Errorf("find the working directory: %w", err)
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("find the working directory: %w", err)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", fmt.Errorf("--dir %s is not a directory", cwd)
	}
	return cwd, nil
}

// executable is the real path of the agentd binary.
func executable(given string) (string, error) {
	exe := given
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", fmt.Errorf("find the agentd binary: %w", err)
		}
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("find the agentd binary: %w", err)
	}
	return real, nil
}

// setEnvironment fixes what the agents and the adapters' npm installs run in: the daemon's environment, and with the
// wrappers' directory first on PATH for the agents; the Node.js the adapters run on is the first `node` of the PATH.
func (d *daemon) setEnvironment() {
	d.env = d.Host.Env
	d.node, _ = home.FindProgram("node", d.env)
	d.agentEnv = home.AgentEnv(d.env, d.bin, d.Host.Platform)
}

func (d *daemon) fleetOptions() agents.FleetOptions {
	return agents.FleetOptions{Cwd: d.cwd, Env: d.env, State: d.state, Adapter: d.store.State, Node: d.node, Log: d.Log}
}

// every runs fn after each interval until the extras end, one run at a time.
func (d *daemon) every(interval time.Duration, fn func()) {
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		defer d.Log.CrashOnPanic(d.Tracker)
		for {
			select {
			case <-d.bgCtx.Done():
				return
			case <-time.After(interval):
			}
			fn()
		}
	}()
}

// startBrowserSessions makes the pages' agents: their own wrapper (`bin/page/agentd-mcp`, which runs `mcp --page`) and
// the bridges' socket.
func (d *daemon) startBrowserSessions() error {
	binDir, err := home.InstallWrapper(filepath.Join(d.bin, "page"), runtime.GOOS, home.WrapperTarget{Exe: d.exe, Args: []string{"--page"}})
	if err != nil {
		return fmt.Errorf("write the pages' MCP wrapper: %w", err)
	}
	fleet := d.fleetOptions()
	fleet.BinDir = binDir
	d.browsers = gateway.NewBrowserSessions(gateway.SessionOptions{
		Fleet:         fleet,
		Hello:         d.pageHello,
		Relay:         d.listenForBridges(),
		BridgeCommand: filepath.Join(binDir, home.WrapperFileName(runtime.GOOS)),
		Folders:       d.browserFolders,
	})
	// The standard ACP endpoint has no bridge to a page's tools; its agents get the same wrapper directory because
	// every agent's PATH starts with one.
	d.acp = gateway.NewAcpSessions(gateway.AcpOptions{Fleet: fleet, Folders: d.browserFolders})
	d.setupAcp()
	return nil
}

// running is the number of agents running for apps, for web pages and for ACP connections: what ending the daemon ends.
func (d *daemon) running() int { return d.hub.Size() + d.browsers.Size() + d.acp.Size() }

// browserFolders are the folders the user allowed the page origin; the registry is read each time, so a grant that
// is made or removed applies to sockets that are already open.
func (d *daemon) browserFolders(origin string) ([]string, error) {
	reg, err := registry.Load(d.file)
	if err != nil {
		return nil, err
	}
	return reg.BrowserFolders(origin), nil
}

// appFolders are the folders the user allowed the app at server.
func (d *daemon) appFolders(server string) ([]string, error) {
	reg, err := registry.Load(d.file)
	if err != nil {
		return nil, err
	}
	return reg.AppFolders(server), nil
}

// listenForBridges opens the socket of the pages' `agentd-mcp --page` bridges; without it pages run agents but serve
// them no tools.
func (d *daemon) listenForBridges() *pagetools.McpRelay {
	relay, err := pagetools.Listen(filepath.Join(d.root, "mcp.sock"))
	if err != nil {
		d.Log.Warn("pages cannot serve MCP tools to their agents: " + err.Error())
		return nil
	}
	return relay
}

func (d *daemon) startHub() {
	d.hub = link.NewAppHub(link.AppHubOptions{
		Fleet:       d.fleetOptions(),
		WrapperRoot: d.bin,
		ConfigFile:  d.file,
		Executable:  d.exe,
		Platform:    runtime.GOOS,
		Folders:     d.appFolders,
		Hello:       d.hello,
		OnEmpty:     func(code int) { d.stopUnlessPagesMayConnect(code) },
		OnRestart:   func(server string) { d.restartRequested(server, d.running()) },
		Log:         d.Log,
	})
}

// stopUnlessPagesMayConnect: a page that may still ask to connect keeps the daemon up, and so does a program that is
// connected over /acp right now (an editor must not be cut off in the middle of a session); without either, an app that
// left has nothing to wait for.
func (d *daemon) stopUnlessPagesMayConnect(code int) {
	reg, err := registry.Load(d.file)
	acpAgents := 0
	if d.acp != nil {
		acpAgents = d.acp.Size()
	}
	if note := stayUpNote(err == nil && reg.HasLiveBrowsers(time.Now()), acpAgents); note != "" {
		d.Log.Say("the last app left; staying up for " + note)
		return
	}
	d.stop(code)
}

// stayUpNote is who the daemon stays up for when the last app has left, in words for the log; empty when for nobody.
func stayUpNote(pagesMayConnect bool, acpAgents int) string {
	const pages, programs = "the web pages that may connect", "the programs connected over /acp"
	switch {
	case pagesMayConnect && acpAgents > 0:
		return pages + " and " + programs
	case pagesMayConnect:
		return pages
	case acpAgents > 0:
		return programs
	}
	return ""
}

// restartRequested ends the daemon with the code that starts it again, when a service does; otherwise it says why not.
func (d *daemon) restartRequested(server string, sessions int) {
	outcome := home.ControlOutcomeOf(home.ControlRestart, home.IsManaged(d.env))
	if !outcome.Exit {
		d.Log.Say(outcome.Why)
		return
	}
	d.Log.Say(fmt.Sprintf("restart requested by the server %s: ending %d agent(s), exiting %d", server, sessions, outcome.Code))
	d.stop(outcome.Code)
}

func (d *daemon) startGateway(port int) (*gateway.Gateway, error) {
	gw, err := gateway.Start(d.gatewayDeps(), port, gateway.Options{Gate: consent.NewPeerGate(consent.SystemOwners(), os.Getuid(), d.Log.Warn), Say: d.Log.Say})
	if errors.Is(err, gateway.ErrPortsTaken) {
		return nil, nil
	}
	return gw, err
}

func (d *daemon) announce(apps int) {
	if d.gateway == nil {
		d.Log.Say("local gateway off: the ports 47615-47624 are taken; apps already paired still work")
	} else {
		d.Log.Say(fmt.Sprintf("local gateway on 127.0.0.1:%d", d.gateway.Port))
	}
	d.Log.Say(fmt.Sprintf("agentd %s (pid %d) serving agents in %s for %d app(s)", home.Version, os.Getpid(), d.cwd, apps))
}

// startExtras starts what runs beside the apps: the link to agentd.app that shows the state, and the registry checks.
func (d *daemon) startExtras() error {
	if d.UI != nil {
		d.stopUI = d.startUI(d.UI)
	}
	d.every(registryCheckEvery, d.newRegistryFollower().check)
	return nil
}

// stopExtras ends what startExtras started.
func (d *daemon) stopExtras() {
	d.bgCancel()
	d.bg.Wait()
	if d.stopUI != nil {
		d.stopUI()
	}
}
