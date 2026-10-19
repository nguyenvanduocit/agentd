package agents

import (
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
	// Cwd is the working directory of every agent.
	Cwd string
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
	return f.start(id, file, cmd.Args, env)
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
// slip in between; an agent that exits at once ends through onEnd, which waits for the lock.
func (f *Fleet) start(id, file string, args []string, env home.Env) (Opened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkLocked(id); err != nil {
		return Opened{}, err
	}
	began := time.Now()
	plan := proc.PlanSpawn(file, args, runtime.GOOS)
	cmd := exec.Command(file, args...)
	cmd.Dir = f.o.Cwd
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
	return Opened{PID: session.PGID(), Cwd: f.o.Cwd}, nil
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

// Write writes one line to the agent of session `id`; false when there is none or it can no longer read.
func (f *Fleet) Write(id, line string) bool {
	if s := f.session(id); s != nil {
		return s.Write(line)
	}
	return false
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

// Shutdown ends every agent and returns once they are gone, or after `grace` (the stragglers are SIGKILLed anyway).
// No session opens after this.
func (f *Fleet) Shutdown(grace time.Duration) {
	f.mu.Lock()
	f.draining = true
	f.mu.Unlock()
	f.EndAll()
	deadline := time.Now().Add(grace)
	for f.Size() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}
