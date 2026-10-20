package proc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sync"
)

// wrapperName is how installWrapper's parent directory is named: agentd- and six characters.
var wrapperName = regexp.MustCompile(`^agentd-[0-9A-Za-z]{6}$`)

// wrapperDir is the directory the daemon made for bin: <tmp>/agentd-XXXXXX/bin. The state file can name any path;
// nothing else is removed. It returns "" for any other path.
func wrapperDir(bin, tmp string) string {
	dir := filepath.Dir(bin)
	if filepath.Base(bin) == "bin" && filepath.Dir(dir) == filepath.Clean(tmp) && wrapperName.MatchString(filepath.Base(dir)) {
		return dir
	}
	return ""
}

func psTable() []PsRow {
	out := Exec("ps", []string{"-axo", "pid=,pgid=,lstart="}, append(os.Environ(), "LC_ALL=C"))
	if out.Code != 0 {
		return nil
	}
	return ParsePs(out.Stdout)
}

// SessionState is the record of the process groups of live agents, kept in a file. SIGKILL gives the daemon no chance
// to end its agents; the next start reads the file and ends the groups that are still running. Windows has no process
// groups to list, so the record is not kept there.
type SessionState struct {
	file    string
	log     *Logger
	enabled bool
	pid     int

	mu    sync.Mutex
	state State
}

// NewSessionState records into file; bin is the directory that holds the agentd-mcp wrapper.
func NewSessionState(file, bin string, log *Logger) *SessionState {
	pid := os.Getpid()
	return &SessionState{
		file:    file,
		log:     log,
		enabled: runtime.GOOS != "windows",
		pid:     pid,
		state:   State{Daemon: DaemonRef{PID: pid}, Bin: bin, Groups: []StateGroup{}},
	}
}

// Start ends what a dead predecessor left, then records this daemon.
func (s *SessionState) Start() {
	if !s.enabled {
		return
	}
	table := psTable()
	if prev, ok := s.read(); ok {
		s.reap(prev, table)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(table, func(r PsRow) bool { return r.PID == s.pid }); i >= 0 {
		s.state.Daemon.Lstart = table[i].Lstart
	}
	s.save()
}

func (s *SessionState) read() (State, bool) {
	text, err := os.ReadFile(s.file)
	if err != nil {
		return State{}, false
	}
	return ParseState(string(text))
}

func (s *SessionState) reap(prev State, table []PsRow) {
	result := Sweep(prev, table, s.pid)
	if result.PredecessorAlive {
		return
	}
	for _, g := range result.Kill {
		SignalGroup(g.Pgid, true)
	}
	if dir := wrapperDir(prev.Bin, os.TempDir()); dir != "" {
		_ = os.RemoveAll(dir)
	}
	if len(result.Kill) > 0 {
		procs := 0
		for _, g := range result.Kill {
			procs += g.Members
		}
		s.log.Say(fmt.Sprintf("reaped %d orphan group(s), %d process(es) from a previous daemon", len(result.Kill), procs))
	}
}

// Add records a new agent group. The leader's start time follows once `ps` has answered.
func (s *SessionState) Add(pgid int) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	s.state.Groups = append(s.state.Groups, StateGroup{Pgid: pgid})
	s.save()
	s.mu.Unlock()
	go func() {
		table := psTable()
		s.mu.Lock()
		defer s.mu.Unlock()
		row := slices.IndexFunc(table, func(r PsRow) bool { return r.PID == pgid })
		group := slices.IndexFunc(s.state.Groups, func(g StateGroup) bool { return g.Pgid == pgid })
		if row >= 0 && group >= 0 {
			s.state.Groups[group].Lstart = table[row].Lstart
			s.save()
		}
	}()
}

func (s *SessionState) Remove(pgid int) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Groups = slices.DeleteFunc(s.state.Groups, func(g StateGroup) bool { return g.Pgid == pgid })
	s.save()
}

// Clear is a clean exit: nothing is left to reap, unless the record lists a group that still runs (an agent that
// ignored SIGTERM and was not yet killed). That group stays in the record: the next start finds it there and ends it,
// and nothing else knows of it.
func (s *SessionState) Clear() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running := slices.DeleteFunc(slices.Clone(s.state.Groups), func(g StateGroup) bool { return !GroupAlive(g.Pgid) })
	if len(running) == 0 {
		_ = os.Remove(s.file)
		return
	}
	s.state.Groups = running
	s.save()
	s.log.Warn(fmt.Sprintf("%d agent group(s) still running at exit; the next start ends them", len(running)))
}

// save writes the record; the caller holds s.mu. The record is best effort: a full disk must not stop an agent from
// starting.
func (s *SessionState) save() {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(s.state) != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.file), 0o700) != nil {
		return
	}
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, bytes.TrimSuffix(buf.Bytes(), []byte("\n")), 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, s.file)
}
