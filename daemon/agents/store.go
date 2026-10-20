package agents

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	npmTimeout = 30 * time.Minute
	// stderrKeep is how much of npm's stderr is kept: the tail is what NpmTail reads.
	stderrKeep = 4000
)

// ReadInstalled is the executable of a complete install, or false when the directory is missing, has no valid
// marker, or lost its file.
func ReadInstalled(dir string) (string, bool) {
	text, err := os.ReadFile(filepath.Join(dir, Marker))
	if err != nil {
		return "", false
	}
	rel, ok := ParseMarker(string(text))
	if !ok {
		return "", false
	}
	bin := filepath.Join(dir, rel)
	if _, err := os.Stat(bin); err != nil {
		return "", false
	}
	return bin, true
}

// environ is an environment as the "NAME=value" list a program gets.
func environ(env home.Env) []string {
	list := make([]string, 0, len(env))
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	return list
}

// tail keeps the last `keep` bytes written to it.
type tail struct {
	mu   sync.Mutex
	keep int
	data []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > t.keep {
		t.data = append([]byte(nil), t.data[len(t.data)-t.keep:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}

// runNpm runs npm in its own process group; the error is the tail of npm's stderr.
func runNpm(npm string, args []string, dir string, env home.Env, tracker *proc.Tracker) error {
	cmd := exec.Command(npm, args...)
	cmd.Dir = dir
	cmd.Env = environ(env)
	stderr := &tail{keep: stderrKeep}
	cmd.Stderr = stderr
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	pgid := cmd.Process.Pid
	tracker.Track(pgid)
	timer := time.AfterFunc(npmTimeout, func() { proc.SignalGroup(pgid, true) })
	err := cmd.Wait()
	timer.Stop()
	tracker.Untrack(pgid)
	if err == nil {
		return nil
	}
	if detail := NpmTail(stderr.String()); detail != "" {
		return errors.New(detail)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return fmt.Errorf("npm exited with %d", exit.ExitCode())
	}
	return fmt.Errorf("npm: %w", err)
}

// removeAll removes paths best effort: an install directory that cannot be removed is retried by the next tidy.
func removeAll(paths ...string) {
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
}

func removePartials(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if IsPartial(e.Name()) {
			removeAll(filepath.Join(dir, e.Name()))
		}
	}
}

// tidy removes what an interrupted install left, and the versions older than the previous one.
func tidy(l home.Layout, a Adapter, keep string) {
	dir := AdapterAgentDir(l, a.Name)
	removePartials(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var installs []Entry
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
			installs = append(installs, Entry{Name: e.Name(), ModTime: info.ModTime()})
		}
	}
	for _, name := range OldInstalls(installs, keep) {
		removeAll(filepath.Join(dir, name))
	}
}

// partials are the directories installs are writing into now.
type partials struct {
	mu   sync.Mutex
	dirs map[string]struct{}
}

func (p *partials) add(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirs[dir] = struct{}{}
}

func (p *partials) delete(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dirs, dir)
}

func (p *partials) removeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for dir := range p.dirs {
		removeAll(dir)
	}
	p.dirs = map[string]struct{}{}
}

// StoreOptions are what a Store needs.
type StoreOptions struct {
	Layout home.Layout
	// Env is the environment npm and the lookups get (PATH, AGENTD_*).
	Env home.Env
	// Retry is the backoff of a failed install; the zero value is AdapterRetry.
	Retry home.BackoffOptions
	// Log is the daemon's log; nil discards.
	Log *proc.Logger
	// Tracker holds the process group of a running install, so the way out of the daemon can end it; nil makes the
	// store's own.
	Tracker *proc.Tracker
}

// Store holds the adapters of the agents that run one (claude, codex). Each is installed once, in the background of
// the daemon, the moment it starts: not by `open`, so no timeout of a chat session can cut an install. One install per
// agent at a time; a failure is kept as the agent's reason and retried with backoff.
type Store struct {
	o        StoreOptions
	partials partials

	mu       sync.Mutex
	states   map[AdapterName]AdapterState
	timers   map[*time.Timer]struct{}
	stopped  bool
	listener func()
}

// NewStore makes a store; Start begins the installs.
func NewStore(o StoreOptions) *Store {
	if o.Retry == (home.BackoffOptions{}) {
		o.Retry = AdapterRetry
	}
	if o.Log == nil {
		o.Log = proc.NewLogger(io.Discard, io.Discard)
	}
	if o.Tracker == nil {
		o.Tracker = proc.NewTracker()
	}
	return &Store{
		o:        o,
		partials: partials{dirs: map[string]struct{}{}},
		states:   map[AdapterName]AdapterState{},
		timers:   map[*time.Timer]struct{}{},
	}
}

// OnChange sets `fn`, which runs when an agent turns ready or its reason changes. It runs on the store's own
// goroutine, never under the store's lock.
func (s *Store) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listener = fn
}

// State is an adapter's state; false when it has none (its agent has a seam).
func (s *Store) State(name AdapterName) (AdapterState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[name]
	return state, ok
}

// Start marks what is installed already as ready (before the first hello) and installs the rest in the background.
func (s *Store) Start() {
	for _, name := range AdapterOrder {
		a := Adapters[name]
		if seamed(AgentName(name), s.o.Env) {
			continue
		}
		if AdaptersDisabled(s.o.Env) {
			s.put(name, AdapterState{Kind: AdapterFailed, Reason: DisabledReason})
			continue
		}
		if bin, ok := ReadInstalled(AdapterDir(s.o.Layout, name, AdapterKey(a, s.o.Env))); ok {
			s.put(name, AdapterState{Kind: AdapterReady, Bin: bin})
			continue
		}
		s.put(name, AdapterState{Kind: AdapterInstalling})
		s.o.Log.Say(InstallingReason(a))
		go s.run(a, 0)
	}
}

func (s *Store) put(name AdapterName, next AdapterState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[name] = next
}

func (s *Store) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Store) run(a Adapter, attempt int) {
	defer s.o.Log.CrashOnPanic(s.o.Tracker)
	bin, err := install(s.o, a, &s.partials)
	if s.isStopped() {
		return
	}
	if err == nil {
		s.o.Log.Say(fmt.Sprintf("the %s adapter is installed", a.Title))
		s.set(a.Name, AdapterState{Kind: AdapterReady, Bin: bin})
		return
	}
	delay := home.BackoffDelay(attempt, s.o.Retry, nil)
	reason := FailedReason(a, err.Error(), delay)
	s.o.Log.Warn(reason + " [" + err.Error() + "]") // the person reads the one plain line; the log keeps what npm said
	s.set(a.Name, AdapterState{Kind: AdapterFailed, Reason: reason})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		delete(s.timers, timer)
		s.mu.Unlock()
		s.run(a, attempt+1)
	})
	s.timers[timer] = struct{}{}
}

func (s *Store) set(name AdapterName, next AdapterState) {
	s.mu.Lock()
	s.states[name] = next
	listener := s.listener
	s.mu.Unlock()
	if listener != nil {
		listener()
	}
}

// Stop ends installs that are running (npm and what it started), removes what they had written, cancels the retries.
func (s *Store) Stop() {
	s.mu.Lock()
	s.stopped = true
	for t := range s.timers {
		t.Stop()
	}
	s.timers = map[*time.Timer]struct{}{}
	s.mu.Unlock()
	s.o.Tracker.KillAll()
	s.partials.removeAll()
}

// install installs the adapter once: `npm install` into a `.partial` directory, the marker, then one rename to the
// final name. A directory with the final name and no marker is a leftover and is removed first. It returns the
// adapter's executable.
func install(o StoreOptions, a Adapter, partials *partials) (string, error) {
	key := AdapterKey(a, o.Env)
	final := AdapterDir(o.Layout, a.Name, key)
	agentDir := AdapterAgentDir(o.Layout, a.Name)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", err
	}
	if done, ok := ReadInstalled(final); ok {
		return done, nil
	}
	removeAll(final)
	removePartials(agentDir)

	npm, ok := home.FindProgram("npm", o.Env)
	if !ok {
		return "", errors.New("npm not found: install Node.js (it comes with npm)")
	}
	part := PartialDir(o.Layout, a.Name, key, os.Getpid())
	if err := os.Mkdir(part, 0o755); err != nil {
		return "", err
	}
	partials.add(part)
	bin, err := installInto(o, a, npm, part, final)
	if err != nil {
		removeAll(part)
		partials.delete(part)
		return "", err
	}
	partials.delete(part)
	tidy(o.Layout, a, key)
	return bin, nil
}

// installInto runs npm into `part`, checks the package's bin, writes the marker and renames `part` to `final`.
func installInto(o StoreOptions, a Adapter, npm, part, final string) (string, error) {
	spec := AdapterSpec(a, o.Env)
	if err := runNpm(npm, NpmArgs(part, spec), part, o.Env, o.Tracker); err != nil {
		return "", err
	}
	manifest, err := os.ReadFile(filepath.Join(PackageDir(part, a.Pkg), "package.json"))
	if err != nil {
		return "", err
	}
	binRel, err := BinEntry(a.Pkg, manifest)
	if err != nil {
		return "", err
	}
	rel := path.Join("node_modules", a.Pkg, binRel)
	if _, err := os.Stat(filepath.Join(part, rel)); err != nil {
		return "", fmt.Errorf("%s installed without its bin %s", a.Pkg, binRel)
	}
	if err := os.WriteFile(filepath.Join(part, Marker), []byte(MarkerText(spec, rel)), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return filepath.Join(final, rel), nil
}
