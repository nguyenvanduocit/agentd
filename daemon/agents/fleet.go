package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// GroupRecorder is the record of the process groups of live agents (proc.SessionState).
type GroupRecorder interface {
	Add(pgid int)
	Remove(pgid int)
}

// FleetOptions are what a Fleet needs.
type FleetOptions struct {
	// Cwd is the working directory of every agent, unless Folders names folders.
	Cwd string
	// Folders, when set, is the folders the user allowed this fleet's app, read each time they matter: an agent starts
	// in the first of them that exists, and `session/...` requests that name a place outside them are refused
	// (GuardLine). Nil, or no folder, is no limit; an error (the list cannot be read) stops the open and every such
	// request, never lifts the limit.
	Folders func() ([]string, error)
	// BinDir is the directory that holds the agentd-mcp wrapper; it goes first on every agent's PATH.
	BinDir string
	// Env is the daemon's environment.
	Env home.Env
	// Adapter is what the adapter installs of claude and codex are doing (Store.State).
	Adapter AdapterLookup
	// Node is the Node.js the adapters run on; empty finds `node` on the agent's PATH.
	Node  string
	State GroupRecorder
	// Sink is the sink for the connection that is current now.
	Sink func() FrameSink
	// Ended, when set, is told that a session's agent is gone (it exited or was ended); its id may be opened again.
	Ended func(id string)
	// Log is the daemon's log; nil discards.
	Log *proc.Logger
}

// Opened is what the server learns of a session that started.
type Opened struct {
	PID int
	Cwd string
}

// Fleet is the agents running on this machine, by session id.
type Fleet struct {
	o FleetOptions

	mu       sync.Mutex
	sessions map[string]*Session
	launches map[string]launch
	draining bool
}

// launch is how a session's agent was started: what Chdir runs again in another directory.
type launch struct {
	file string
	args []string
	env  home.Env
}

// NewFleet makes a fleet with no agent running.
func NewFleet(o FleetOptions) *Fleet {
	if o.Log == nil {
		o.Log = proc.NewLogger(io.Discard, io.Discard)
	}
	return &Fleet{o: o, sessions: map[string]*Session{}, launches: map[string]launch{}}
}

// Size is the number of agents running.
func (f *Fleet) Size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// Open starts the agent `agent` for session `id`.
func (f *Fleet) Open(id, agent string) (Opened, error) {
	return f.OpenOn(context.Background(), id, agent)
}

// OpenOn is Open for a session that lives and dies with a connection: connection is done when that connection is, and
// an agent that was not registered by then is not started. EndAll after the connection ended therefore finds every
// agent the connection opened, and an open still on its way starts nothing.
func (f *Fleet) OpenOn(connection context.Context, id, agent string) (Opened, error) {
	if err := f.check(id); err != nil {
		return Opened{}, err
	}
	if !IsAgentName(agent) {
		names := make([]string, len(AgentNames))
		for i, n := range AgentNames {
			names[i] = string(n)
		}
		return Opened{}, fmt.Errorf("unknown agent %q: this daemon runs %s", agent, strings.Join(names, ", "))
	}
	name := AgentName(agent)
	env := home.AgentEnv(f.o.Env, f.o.BinDir, runtime.GOOS)
	find := func(program string) (string, bool) { return home.FindProgram(program, env) }
	if refusal, refused := OpenRefusal(name, env, find, f.o.Adapter); refused {
		return Opened{}, errors.New(refusal)
	}
	node := f.o.Node
	if node == "" {
		node = "node"
	}
	cmd, err := Command(name, env, f.o.Adapter, node)
	if err != nil {
		return Opened{}, err
	}
	// Resolved against the agent's PATH here, not left to exec: it looks in the daemon's PATH.
	file, ok := find(cmd.Command)
	if !ok {
		return Opened{}, fmt.Errorf("agent %s: command %q not found in PATH", name, cmd.Command)
	}
	return f.start(connection, id, file, cmd.Args, env)
}

// check is why a session `id` cannot open now, or nil.
func (f *Fleet) check(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkLocked(id)
}

func (f *Fleet) checkLocked(id string) error {
	switch {
	case f.draining:
		return errors.New("this daemon is stopping")
	case id == "":
		return errors.New("empty session id")
	}
	if _, exists := f.sessions[id]; exists {
		return fmt.Errorf("session %s exists", id)
	}
	return nil
}

// start holds the lock from the draining check to the registration, so a Shutdown or a session of the same id cannot
// slip in between; an agent that exits at once ends through onEnd, which waits for the lock. The connection's end is
// checked under the same lock: EndAll takes it after the connection ended, so an open either registers before EndAll
// looks (and is ended by it) or sees the connection done here.
func (f *Fleet) start(connection context.Context, id, file string, args []string, env home.Env) (Opened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkLocked(id); err != nil {
		return Opened{}, err
	}
	dir, err := f.dir()
	if err != nil {
		return Opened{}, err
	}
	return f.spawnLocked(connection, id, launch{file, args, env}, dir)
}

// spawnLocked starts the agent of session `id` in dir and registers it; f.mu is held.
func (f *Fleet) spawnLocked(connection context.Context, id string, l launch, dir string) (Opened, error) {
	if connection.Err() != nil {
		return Opened{}, errors.New("the connection of this session ended before its agent started")
	}
	began := time.Now()
	plan := proc.PlanSpawn(l.file, l.args, runtime.GOOS)
	cmd := exec.Command(l.file, l.args...)
	cmd.Dir = dir
	cmd.Env = environ(l.env)
	if plan.Detached {
		proc.Detach(cmd)
	}
	var self atomic.Pointer[Session] // set below, under the lock that ended takes first
	session, err := StartSession(id, cmd, f.o.Sink(), func() { f.ended(id, &self) })
	if err != nil {
		return Opened{}, fmt.Errorf("cannot start %s: %w", l.file, err)
	}
	self.Store(session)
	f.sessions[id] = session
	f.launches[id] = l
	f.o.State.Add(session.PGID())
	f.o.Log.Say(fmt.Sprintf("started %s for session %s in %d ms", l.file, id, time.Since(began).Milliseconds()))
	return Opened{PID: session.PGID(), Cwd: dir}, nil
}

// folders is what the user allowed this fleet's app; empty is no limit.
func (f *Fleet) folders() ([]string, error) {
	if f.o.Folders == nil {
		return nil, nil
	}
	return f.o.Folders()
}

// dir is the directory an agent starts in.
func (f *Fleet) dir() (string, error) {
	folders, err := f.folders()
	if err != nil {
		return "", err
	}
	if len(folders) > 0 {
		return firstDir(folders)
	}
	return f.o.Cwd, nil
}

// ended is the agent `self` of session `id` gone: its id may be opened again.
func (f *Fleet) ended(id string, self *atomic.Pointer[Session]) {
	f.mu.Lock()
	gone := self.Load()
	if f.sessions[id] == gone {
		delete(f.sessions, id)
		delete(f.launches, id)
	}
	f.mu.Unlock()
	f.o.State.Remove(gone.PGID())
	if f.o.Ended != nil {
		f.o.Ended(id)
	}
}

func (f *Fleet) session(id string) *Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

// Write writes one line to the agent of session `id`; false when there is none or it can no longer read. A request
// for a folder the user did not allow is answered with an error to the client instead (GuardLine): the line was taken.
func (f *Fleet) Write(id, line string) bool {
	s := f.session(id)
	if s == nil {
		return false
	}
	folders, err := f.folders()
	if reply, refused := GuardLine(folders, line, err); refused {
		if reply != "" {
			f.o.Sink().Send(id, FrameOut, reply, 0)
		}
		return true
	}
	return s.Write(line)
}

// Chdir moves session `id` to dir: its agent is ended without a word to the client, and the same program starts again
// in dir under the same id, on the same stream of frames. The new agent is a new process that has not been
// initialized; nothing of the old one's conversation is in it, so a client speaks ACP to it from the start (and loads
// the session it wants back). dir must be an absolute path to a directory the user allowed this fleet's app, as the
// folders of GuardLine decide, and the session stays where it is when it is not. An agent that cannot start in dir is
// gone and told as any agent that ended is: the error comes back, and so does an exit frame.
func (f *Fleet) Chdir(connection context.Context, id, dir string) (Opened, error) {
	if err := f.allowedDir(dir); err != nil {
		return Opened{}, err
	}
	f.mu.Lock()
	old, l := f.sessions[id], f.launches[id]
	draining := f.draining
	f.mu.Unlock()
	switch {
	case draining:
		return Opened{}, errors.New("this daemon is stopping")
	case old == nil:
		return Opened{}, fmt.Errorf("session %s is not running", id)
	case !old.Silence():
		return Opened{}, fmt.Errorf("session %s is ending or already moving", id)
	}
	<-old.Dead()

	f.mu.Lock()
	delete(f.sessions, id)
	delete(f.launches, id)
	f.o.State.Remove(old.PGID())
	opened, err := Opened{}, error(nil)
	if f.draining {
		err = errors.New("this daemon is stopping")
	} else {
		opened, err = f.spawnLocked(connection, id, l, dir)
	}
	f.mu.Unlock()
	if err != nil {
		if f.o.Ended != nil {
			f.o.Ended(id)
		}
		f.o.Sink().Send(id, FrameExit, "", 1)
		return Opened{}, err
	}
	return opened, nil
}

// allowedDir is nil when dir is an existing directory the user allowed this fleet's app: any, when no folders are set.
func (f *Fleet) allowedDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not an absolute path", dir)
	}
	folders, err := f.folders()
	if err != nil {
		return fmt.Errorf("cannot check %s against the folders you allowed this app: %w", dir, err)
	}
	if len(folders) > 0 && !Within(folders, dir) {
		return fmt.Errorf("%s is outside the folders you allowed this app: %s", dir, strings.Join(folders, ", "))
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// Close ends the agent of session `id`.
func (f *Fleet) Close(id string) {
	if s := f.session(id); s != nil {
		s.End()
	}
}

// EndAll: the socket is gone, and with it the server's way to reach the agents: end them all.
func (f *Fleet) EndAll() {
	f.mu.Lock()
	all := make([]*Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		all = append(all, s)
	}
	f.mu.Unlock()
	for _, s := range all {
		s.End()
	}
}

// killWait is how long Shutdown waits, after it killed the stragglers, for their leaders to be gone: SIGKILL cannot
// be ignored, so this is the time the kernel and Wait need.
const killWait = time.Second

// Drain refuses every open from now on and leaves the running agents as they are.
func (f *Fleet) Drain() {
	f.mu.Lock()
	f.draining = true
	f.mu.Unlock()
}

// Shutdown ends every agent and returns once they are gone. An agent that is still running after `grace` is
// SIGKILLed at once and waited for (up to killWait), because the daemon may exit as soon as this returns and a timer
// would die with it. No session opens after this.
func (f *Fleet) Shutdown(grace time.Duration) {
	f.Drain()
	f.EndAll()
	deadline := time.Now().Add(grace)
	for f.Size() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	stragglers := make([]*Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		stragglers = append(stragglers, s)
	}
	f.mu.Unlock()
	for _, s := range stragglers {
		s.Kill()
	}
	wait := time.NewTimer(killWait)
	defer wait.Stop()
	for _, s := range stragglers {
		select {
		case <-s.Dead():
		case <-wait.C:
			return
		}
	}
}
