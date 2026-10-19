package service

import (
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// loop is the way without a service manager: the launcher runs in the background and restarts the daemon with
// backoff. It does not outlive a reboot.
type loop struct{ Deps }

// readPid is the process id in a file, when a process with that id is alive.
func (l loop) readPid(file string) int {
	text, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil || pid <= 1 || !l.Procs.Alive(pid) {
		return 0
	}
	return pid
}

func loopEnv(env map[string]string) []string {
	merged := map[string]string{}
	for k, v := range env {
		merged[k] = v
	}
	merged["AGENTD_RESTART"] = "loop"
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (l loop) spawn(c ctx) error {
	if err := os.MkdirAll(path.Dir(c.layout.LogFile), 0o777); err != nil {
		return err
	}
	log, err := os.OpenFile(c.layout.LogFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }() // the child has its own descriptor
	dir := c.workDir
	if dir == "" {
		dir = c.home
	}
	if err := l.Procs.StartDetached(c.layout.Launcher, []string{"run"}, dir, loopEnv(c.env), log); err != nil {
		return err
	}
	for i := 0; i < 30 && l.readPid(c.layout.LoopPidFile) == 0; i++ {
		l.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (l loop) end(c ctx) error {
	pid := l.readPid(c.layout.LoopPidFile)
	if pid == 0 {
		return nil
	}
	if err := l.Procs.Terminate(pid); err != nil {
		return err
	}
	for i := 0; i < 100 && l.Procs.Alive(pid); i++ {
		l.Sleep(100 * time.Millisecond)
	}
	return removeFile(c.layout.LoopPidFile)
}

func (l loop) lastExit(c ctx) *int {
	text, err := os.ReadFile(c.layout.LastExitFile)
	if err != nil {
		return nil
	}
	return parseInt(string(text))
}

func (l loop) install(c ctx) ([]string, error) {
	if err := l.end(c); err != nil {
		return nil, err
	}
	if err := l.spawn(c); err != nil {
		return nil, err
	}
	return []string{"no service manager here: the daemon runs in the background and restarts when it crashes, but it does not start at login or after a reboot; run `agentd service install` again then"}, nil
}

func (l loop) uninstall(c ctx) error { return l.end(c) }

func (l loop) status(c ctx) *Running {
	if pid := l.readPid(c.layout.LoopPidFile); pid != 0 {
		return &Running{Running: true, PID: pid}
	}
	last := l.lastExit(c)
	if last == nil {
		return nil
	}
	return &Running{LastExit: last, Pending: *last != 0}
}

func (l loop) start(c ctx) error {
	if l.readPid(c.layout.LoopPidFile) == 0 {
		return l.spawn(c)
	}
	return nil
}

func (l loop) restart(c ctx) error {
	if err := l.end(c); err != nil {
		return err
	}
	return l.spawn(c)
}
