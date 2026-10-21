package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const helloTimeout = 10 * time.Second

// detachGrace is how long Detach waits for the agents of an app to end before it closes the link.
const detachGrace = time.Second

// CloseGrace is how long closing a Link waits for the connection to close.
const CloseGrace = time.Second

var errStopping = errors.New("this daemon is stopping")

// AppHubOptions are what an AppHub needs.
type AppHubOptions struct {
	// Fleet is what every app's fleet shares: the working directory, the environment, the session record, the
	// adapters. The hub sets Sink and BinDir, and Log when it is nil.
	Fleet agents.FleetOptions
	// WrapperRoot is the directory that holds one <n>/agentd-mcp wrapper per app.
	WrapperRoot string
	// ConfigFile is daemon.json, which every wrapper passes to `mcp`.
	ConfigFile string
	// Executable is the agentd binary the wrappers run; empty is this process's own.
	Executable string
	// Platform is the OS the wrappers are written for; empty is runtime.GOOS.
	Platform string
	// Backoff and ConnectTimeout are those of every app's Link (see Options); the zero values are the defaults.
	Backoff        home.BackoffOptions
	ConnectTimeout time.Duration
	// Folders are the folders the user allowed the app at `server`, read when they matter
	// (agents.FleetOptions.Folders); nil sets no limit.
	Folders func(server string) ([]string, error)
	// Hello is what this daemon tells an app on every (re)connect.
	Hello func() contract.Hello
	// OnEmpty runs when an app left (Stop, or a refused token) and no link remains.
	OnEmpty func(code int)
	// OnRestart runs when an app (its origin) asked the daemon to restart.
	OnRestart func(server string)
	// Log is the daemon's log; nil discards.
	Log *proc.Logger
}

type entry struct {
	app   registry.App
	link  *Link
	fleet *agents.Fleet
}

// AppHub is the apps this daemon serves: one socket, one fleet of agents and one MCP wrapper each, so their sessions
// never mix.
type AppHub struct {
	o   AppHubOptions
	log *proc.Logger

	mu      sync.Mutex
	entries []*entry // in the order the apps were added
	n       int      // wrapper directories made so far
	// closed is set when the daemon begins to stop: nothing opens and no app is added after that.
	closed bool
	// detaches are the Detach calls started in the background, which Shutdown waits for.
	detaches sync.WaitGroup
	// adding is the number of Add calls in progress: an app being added is an app the hub is about to have, so the hub
	// is not empty while one is. (At start, a refused token detaches the first app while the second is still having
	// its wrapper written; counting only the entries made that look like "the last app left" and ended the daemon.)
	adding int
	// dropped is why the hub let an app go, for the rest of this run: the menu shows every saved app, and a dropped
	// one is linked again only at the next start. Adding the app again, or detaching it for the user, forgets it.
	dropped map[string]droppedApp
	// afterWrapper, when set (tests), runs in Add between writing the wrapper and putting the app in the hub.
	afterWrapper func()
}

// NewAppHub makes a hub that serves no app yet.
func NewAppHub(o AppHubOptions) *AppHub {
	log := o.Log
	if log == nil {
		log = proc.NewLogger(io.Discard, io.Discard)
	}
	return &AppHub{o: o, log: log, dropped: map[string]droppedApp{}}
}

// Drop is why the hub let an app go.
type Drop string

// The reasons an app is dropped.
const (
	// DropStopped: the app's server sent Stop.
	DropStopped Drop = "stopped"
	// DropRefused: the app's server refused the token, which it has revoked or no longer knows.
	DropRefused Drop = "refused"
	// DropIncompatible: the app speaks a protocol this daemon does not.
	DropIncompatible Drop = "incompatible"
)

type droppedApp struct {
	why    Drop
	detail string
}

// AppStatus is what the hub knows of one app now.
type AppStatus struct {
	// Linked: the hub has a link to the app (connected, or trying again). Connected: the link is up.
	Linked, Connected bool
	// Dropped says why the hub let the app go; empty while it is linked or was never dropped. Detail is a plain
	// sentence for DropIncompatible, naming both protocols.
	Dropped Drop
	Detail  string
}

// Status is what the hub knows of the app at server. A saved app that is neither linked nor dropped has not been
// linked yet.
func (h *AppHub) Status(server string) AppStatus {
	h.mu.Lock()
	e := h.find(server)
	d := h.dropped[server]
	h.mu.Unlock()
	if e == nil {
		return AppStatus{Dropped: d.why, Detail: d.detail}
	}
	return AppStatus{Linked: true, Connected: e.link.Connected()}
}

func (h *AppHub) snapshot() []*entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*entry(nil), h.entries...)
}

func (h *AppHub) find(server string) *entry {
	for _, e := range h.entries {
		if e.app.Server == server {
			return e
		}
	}
	return nil
}

// Size is the number of sessions running over all apps.
func (h *AppHub) Size() int {
	total := 0
	for _, e := range h.snapshot() {
		total += e.fleet.Size()
	}
	return total
}

// Linked is the number of apps with a link (connected or reconnecting).
func (h *AppHub) Linked() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// Has reports whether the app has a link.
func (h *AppHub) Has(server string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.find(server) != nil
}

// Servers are the servers of the apps with a link, in the order they were added.
func (h *AppHub) Servers() []string {
	entries := h.snapshot()
	servers := make([]string, len(entries))
	for i, e := range entries {
		servers[i] = e.app.Server
	}
	return servers
}

// Add connects to the app now; an app that has a link already is left as it is.
func (h *AppHub) Add(app registry.App) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errStopping
	}
	if h.find(app.Server) != nil {
		h.mu.Unlock()
		return nil
	}
	h.n++
	n := h.n
	h.adding++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.adding--
		h.mu.Unlock()
	}()

	binDir, err := h.installWrapper(filepath.Join(h.o.WrapperRoot, strconv.Itoa(n)), app.Server)
	if err != nil {
		return err
	}
	if h.afterWrapper != nil {
		h.afterWrapper()
	}
	e := &entry{app: app}
	link, err := New(Options{
		Server:         app.Server,
		Auth:           map[string]string{"role": "daemon", "token": app.Token},
		Backoff:        h.o.Backoff,
		ConnectTimeout: h.o.ConnectTimeout,
	}, handler{h: h, e: e}, h.eventsOf(e))
	if err != nil {
		return err
	}
	fleetOptions := h.o.Fleet
	fleetOptions.BinDir = binDir
	fleetOptions.Sink = link.Sink
	if h.o.Folders != nil {
		fleetOptions.Folders = func() ([]string, error) { return h.o.Folders(app.Server) }
	}
	if fleetOptions.Log == nil {
		fleetOptions.Log = h.log
	}
	e.link, e.fleet = link, agents.NewFleet(fleetOptions)

	h.mu.Lock()
	if h.closed { // the daemon began to stop while the wrapper was written
		h.mu.Unlock()
		return errStopping
	}
	if h.find(app.Server) != nil { // added by a concurrent Add while the wrapper was written
		h.mu.Unlock()
		return nil
	}
	h.entries = append(h.entries, e)
	delete(h.dropped, app.Server) // linked again (paired again, or the daemon started anew): no longer dropped
	h.mu.Unlock()
	link.Start()
	return nil
}

func (h *AppHub) eventsOf(e *entry) Events {
	server := e.app.Server
	return Events{
		OnConnect: func() { go h.sayHello(e) },
		OnDisconnect: func(reason string) {
			h.log.Say(fmt.Sprintf("disconnected from %s (%s); ending %d agent(s), reconnecting", server, reason, e.fleet.Size()))
			e.fleet.EndAll()
		},
		OnTrouble: func(message string) {
			h.log.Warn(fmt.Sprintf("cannot reach %s (%s); retrying", server, message))
		},
		OnRejected: func(message string) {
			h.log.Warn(fmt.Sprintf("%s refused this daemon (%s): the token is revoked or wrong, so this app stays stopped. To use it again: agentd pair --app %s --code <code>, then agentd service start", server, message, server))
			h.dropInBackground(server, droppedApp{why: DropRefused})
		},
	}
}

// handler answers the calls one app's server makes.
type handler struct {
	h *AppHub
	e *entry
}

func (a handler) HandleOpen(ctx context.Context, sessionID, agent string) (contract.Opened, error) {
	opened, err := a.e.fleet.OpenOn(ctx, sessionID, agent)
	if err != nil {
		return contract.Opened{}, err
	}
	return contract.Opened{Pid: float64(opened.PID), Cwd: opened.Cwd}, nil
}

func (a handler) HandleWrite(_ context.Context, sessionID, line string) (bool, error) {
	return a.e.fleet.Write(sessionID, line), nil
}

func (a handler) HandleChdir(ctx context.Context, sessionID, dir string) (contract.Opened, error) {
	moved, err := a.e.fleet.Chdir(ctx, sessionID, dir)
	if err != nil {
		return contract.Opened{}, err
	}
	return contract.Opened{Pid: float64(moved.PID), Cwd: moved.Cwd}, nil
}

func (a handler) HandleClose(_ context.Context, sessionID string) error {
	a.e.fleet.Close(sessionID)
	return nil
}

func (a handler) HandleRestart(context.Context) error {
	if a.h.o.OnRestart != nil {
		a.h.o.OnRestart(a.e.app.Server)
	}
	return nil
}

func (a handler) HandleStop(context.Context) error {
	a.h.log.Say(fmt.Sprintf("stop requested by the server %s: disconnecting it", a.e.app.Server))
	a.h.dropInBackground(a.e.app.Server, droppedApp{why: DropStopped})
	return nil
}

func (a handler) HandleMcpOut(context.Context, string) error {
	return errors.New("this socket is not an MCP bridge")
}

func describeAgents(list []contract.AgentInfo) string {
	parts := make([]string, len(list))
	for i, a := range list {
		if a.Ready {
			parts[i] = a.Name + " ready"
		} else {
			parts[i] = fmt.Sprintf("%s not ready (%s)", a.Name, a.Reason)
		}
	}
	return strings.Join(parts, ", ")
}

// sayHello tells the app who this daemon is, and leaves the app when it speaks another protocol.
func (h *AppHub) sayHello(e *entry) {
	hello := h.o.Hello()
	hello.ProtocolMin, hello.ProtocolMax = home.ProtocolMin, home.ProtocolMax
	ctx, cancel := context.WithTimeout(context.Background(), helloTimeout)
	defer cancel()
	reply, err := e.link.Hello(ctx, hello)
	if err != nil {
		var failure *contract.RpcError
		message := err.Error()
		if errors.As(err, &failure) {
			message = failure.Message
		}
		h.log.Warn(fmt.Sprintf("%s did not accept hello: %s", e.app.Server, message))
		return
	}
	if !home.SpeaksProtocol(reply.Protocol) {
		theirs, ours := strconv.FormatFloat(reply.Protocol, 'f', -1, 64), home.ProtocolRangeText(home.ProtocolMin, home.ProtocolMax)
		h.log.Warn(fmt.Sprintf("%s speaks protocol %s, this daemon speaks %s: disconnecting it. Update the daemon or the app.", e.app.Server, theirs, ours))
		h.dropInBackground(e.app.Server, droppedApp{why: DropIncompatible, detail: fmt.Sprintf("The app speaks protocol %s and agentd on this computer speaks %s: one of them needs an update.", theirs, ours)})
		return
	}
	h.log.Say(fmt.Sprintf("connected to %s (protocol %s); agents: %s", e.app.Server, strconv.FormatFloat(reply.Protocol, 'f', -1, 64), describeAgents(hello.Agents)))
}

// dropInBackground lets the app go for the reason given, on its own goroutine, which Shutdown waits for. Once the hub
// is closed Shutdown has the app already and there is nothing to start.
func (h *AppHub) dropInBackground(server string, why droppedApp) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.detaches.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.detaches.Done()
		h.detach(server, false, &why)
	}()
}

// Detach ends this app's sessions and closes its link for good; it calls OnEmpty when no app is left, unless quiet.
// It is what the user's Disconnect and the registry's removal of an app do, so the hub forgets any reason it had
// for dropping the app: the app is gone, not dropped.
func (h *AppHub) Detach(server string, quiet bool) { h.detach(server, quiet, nil) }

// detach is Detach; with why, the hub remembers the reason for the rest of the run, so that the menu can still show the
// app, and say why it is not linked.
func (h *AppHub) detach(server string, quiet bool, why *droppedApp) {
	h.mu.Lock()
	e := h.find(server)
	if why == nil {
		delete(h.dropped, server)
	}
	if e == nil {
		h.mu.Unlock()
		return
	}
	if why != nil {
		h.dropped[server] = *why
	}
	h.entries = removeEntry(h.entries, e)
	h.mu.Unlock()

	e.fleet.Shutdown(detachGrace)
	e.link.Close(CloseGrace)
	h.mu.Lock()
	empty := len(h.entries) == 0 && h.adding == 0
	h.mu.Unlock()
	if empty && !quiet && h.o.OnEmpty != nil {
		h.o.OnEmpty(home.ExitStopped)
	}
}

func removeEntry(entries []*entry, e *entry) []*entry {
	out := make([]*entry, 0, len(entries))
	for _, other := range entries {
		if other != e {
			out = append(out, other)
		}
	}
	return out
}

// AgentsChanged tells every connected app which agents are ready now.
func (h *AppHub) AgentsChanged(list []contract.AgentInfo) {
	for _, e := range h.snapshot() {
		if e.link.Connected() {
			e.link.Agents(list)
		}
	}
}

// Drain is the first step of stopping: no session opens and no app is added from now on. The agents that run are left
// to Shutdown.
func (h *AppHub) Drain() {
	h.mu.Lock()
	h.closed = true
	entries := append([]*entry(nil), h.entries...)
	h.mu.Unlock()
	for _, e := range entries {
		e.fleet.Drain()
	}
}

// Shutdown ends every agent of every app, closes every link and waits for the detaches that were already under way.
// No session opens after this.
func (h *AppHub) Shutdown(grace time.Duration) {
	h.Drain()
	h.mu.Lock()
	all := h.entries
	h.entries = nil
	h.mu.Unlock()
	var fleets sync.WaitGroup
	for _, e := range all {
		fleets.Add(1)
		go func() {
			defer fleets.Done()
			e.fleet.Shutdown(grace)
		}()
	}
	fleets.Wait()
	var links sync.WaitGroup
	for _, e := range all {
		links.Add(1)
		go func() {
			defer links.Done()
			e.link.Close(CloseGrace)
		}()
	}
	links.Wait()
	h.detaches.Wait()
}

// installWrapper writes, in dir, the wrapper agents find as `agentd-mcp`: it runs `agentd mcp --config <file> --app
// <server>` and the arguments the agent adds. It returns dir.
func (h *AppHub) installWrapper(dir, server string) (string, error) {
	exe := h.o.Executable
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("find the agentd binary the wrapper runs: %w", err)
		}
		exe = self
	}
	platform := h.o.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	return home.InstallWrapper(dir, platform, home.WrapperTarget{Exe: exe, Args: []string{"--config", h.o.ConfigFile, "--app", server}})
}
