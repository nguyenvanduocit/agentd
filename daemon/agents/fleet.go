package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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
	draining bool
}

// NewFleet makes a fleet with no agent running.
func NewFleet(o FleetOptions) *Fleet {
	if o.Log == nil {
		o.Log = proc.NewLogger(io.Discard, io.Discard)
	}
	return &Fleet{o: o, sessions: map[string]*Session{}}
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
	if connection.Err() != nil {
		return Opened{}, errors.New("the connection of this session ended before its agent started")
	}
	began := time.Now()
	plan := proc.PlanSpawn(file, args, runtime.GOOS)
	dir, err := f.dir()
	if err != nil {
		return Opened{}, err
	}
	cmd := exec.Command(file, args...)
	cmd.Dir = dir
	cmd.Env = environ(env)
	if plan.Detached {
		proc.Detach(cmd)
	}
	session, err := StartSession(id, cmd, f.o.Sink(), func() { f.ended(id) })
	if err != nil {
		return Opened{}, fmt.Errorf("cannot start %s: %w", file, err)
	}
	f.sessions[id] = session
	f.o.State.Add(session.PGID())
	f.o.Log.Say(fmt.Sprintf("started %s for session %s in %d ms", file, id, time.Since(began).Milliseconds()))
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

// ended is the agent of session `id` gone: its id may be opened again.
func (f *Fleet) ended(id string) {
	f.mu.Lock()
	session := f.sessions[id]
	delete(f.sessions, id)
	f.mu.Unlock()
	if session != nil {
		f.o.State.Remove(session.PGID())
	}
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
