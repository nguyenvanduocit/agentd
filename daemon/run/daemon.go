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
	"syscall"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/gateway"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/link"
	"github.com/nguyenvanduocit/agentd/daemon/pagemcp"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/daemon/update"
)

const (
	// shutdownGrace is how long the pages' agents and the apps' agents get to end before they are killed.
	shutdownGrace = 2 * time.Second
	// logCheckEvery is how often the log file is looked at for a size over its limit.
	logCheckEvery = time.Minute
)

// Deps is what the daemon touches outside its own logic.
type Deps struct {
	Log *proc.Logger
	// Host is the machine and its environment (the process's, in the entry).
	Host home.Host
	// Tracker holds the process groups that must not outlive the daemon; nil makes the daemon's own.
	Tracker *proc.Tracker
	// Executable is the real path of the agentd binary: the pages' and the apps' MCP wrappers run it, and the
	// installed layout and the tray program are found beside it. Empty is this process's own.
	Executable string
}

// daemon is one run: what it started, so that the way out can end it in order.
type daemon struct {
	Deps
	env      home.Env // the daemon's environment, with a Node.js the installer downloaded first on PATH
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
	settings *home.SettingsStore
	layout   *home.Layout
	updates  *updates
	browsers *gateway.BrowserSessions
	hub      *link.AppHub
	gateway  *gateway.Gateway
	stopTray func()

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
	d.settings = home.OpenSettings(home.SettingsFile(d.file), d.Log.Warn)
	apps, err := reg.SelectApps(cmd.App)
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

	d.startUpdates()
	if err := d.startBrowserSessions(); err != nil {
		return 0, err
	}
	d.startHub()
	d.store.OnChange(func() { d.hub.AgentsChanged(d.agentInfos()) })
	for _, app := range apps {
		if err := d.hub.Add(app); err != nil {
			return 0, err
		}
	}
	if d.gateway, err = d.startGateway(port); err != nil {
		return 0, err
	}
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

// shutdown ends what runs, in order: the adapter installs, the extras (tray, updates, registry checks), the pages'
// sockets, the gateway, the apps' links. Whatever did not start is skipped.
func (d *daemon) shutdown() {
	if d.store != nil {
		d.store.Stop()
	}
	d.stopExtras()
	if d.browsers != nil {
		d.browsers.Shutdown(shutdownGrace)
	}
	if d.gateway != nil {
		_ = d.gateway.Close()
	}
	if d.hub != nil {
		d.hub.Shutdown(shutdownGrace)
	}
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

// setEnvironment fixes what the agents and the adapters' npm installs run in: the daemon's environment with a Node.js
// the installer downloaded (~/.agentd/node) first on PATH, so that the adapters and npm use it; the Node.js the
// adapters run on is the first `node` of that PATH.
func (d *daemon) setEnvironment() {
	d.env = d.Host.Env
	nodeBin := home.LayoutOf(home.AgentdHome(d.Host)).NodeDir + "/bin"
	if _, ok := home.FindProgram("node", home.Env{"PATH": nodeBin}); ok {
		if dirs := home.PathDirs(d.env, d.Host.Platform); len(dirs) == 0 || dirs[0] != nodeBin {
			d.env = home.AgentEnv(d.env, nodeBin, d.Host.Platform)
		}
	}
	d.node, _ = home.FindProgram("node", d.env)
	d.agentEnv = home.AgentEnv(d.env, d.bin, d.Host.Platform)
}

func (d *daemon) fleetOptions() agents.FleetOptions {
	return agents.FleetOptions{Cwd: d.cwd, Env: d.env, State: d.state, Adapter: d.store.State, Node: d.node, Log: d.Log}
}

// startUpdates finds the layout updates and log rotation work in; a daemon that is not installed says so.
func (d *daemon) startUpdates() {
	if l, ok := installedLayout(d.exe, d.Host, d.Log); ok {
		d.layout = &l
		d.keepLogSmall(l)
		return
	}
	d.Log.Say("updates are off: this daemon is not installed under ~/.agentd/daemon (the installer does that)")
}

// keepLogSmall keeps the log file of a service under its size limit: only when this process writes its stdout into
// that file.
func (d *daemon) keepLogSmall(l home.Layout) {
	check := func() {
		if proc.StdoutIs(l.LogFile) {
			proc.RotateLog(l.LogFile, proc.LogMaxBytes, proc.LogKeep)
		}
	}
	check()
	d.every(logCheckEvery, check)
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
	})
	return nil
}

// listenForBridges opens the socket of the pages' `agentd-mcp --page` bridges; without it pages run agents but serve
// them no tools.
func (d *daemon) listenForBridges() *pagemcp.McpRelay {
	relay, err := pagemcp.Listen(filepath.Join(d.root, "mcp.sock"))
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
		Hello:       d.hello,
		OnEmpty:     func(code int) { d.stopUnlessPagesMayConnect(code) },
		OnRestart:   func(server string) { d.restartRequested(server, d.hub.Size()) },
		Log:         d.Log,
	})
}

// stopUnlessPagesMayConnect: a page that may still ask to connect keeps the daemon up; without one an app that left has
// nothing to wait for.
func (d *daemon) stopUnlessPagesMayConnect(code int) {
	reg, err := registry.Load(d.file)
	if err == nil && len(reg.Browsers) > 0 {
		d.Log.Say("the last app left; staying up for the web pages that may connect")
		return
	}
	d.stop(code)
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
	gw, err := gateway.Start(d.gatewayDeps(), port, gateway.Options{Gate: consent.NewPeerGate(consent.SystemOwners(), os.Getuid(), d.Log.Warn)})
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

// startExtras starts what runs beside the apps: the updates, the icon in the menu bar (or the channel to agentd.app)
// and the registry checks.
func (d *daemon) startExtras() error {
	open, err := d.trayLink()
	if err != nil {
		return err
	}
	if d.updates, err = d.updateSwitch(); err != nil {
		return err
	}
	d.updates.turn(d.settings.Get().Updates)
	if open != nil {
		d.stopTray = d.startTray(open)
	}
	d.every(registryCheckEvery, d.followRegistry)
	return nil
}

// updateSwitch is the switch of the updates: off for a daemon that is not installed or has no release server.
func (d *daemon) updateSwitch() (*updates, error) {
	if d.layout == nil {
		return noUpdates(), nil
	}
	base, err := update.ReleaseBase(func(key string) (string, bool) { v, ok := d.Host.Env[key]; return v, ok }, home.ReleaseURL)
	if err != nil {
		return nil, err
	}
	if base == "" {
		d.Log.Say("updates are off: there is no release server (AGENTD_RELEASE_URL is empty)")
		return noUpdates(), nil
	}
	return &updates{
		updatable: true,
		layout:    *d.layout,
		base:      base,
		interval:  releaseCheckInterval(d.Host.Env),
		idle:      func() bool { return d.hub.Size()+d.browsers.Size() == 0 },
		exit:      d.stop,
		log:       d.Log,
	}, nil
}

// stopExtras ends what startExtras and the log check started.
func (d *daemon) stopExtras() {
	d.bgCancel()
	d.bg.Wait()
	if d.updates != nil {
		d.updates.stop()
	}
	if d.stopTray != nil {
		d.stopTray()
	}
}
