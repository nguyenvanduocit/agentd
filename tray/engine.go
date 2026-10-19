//go:build !windows

package main

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// maxLog is how large the engine's log may grow before the app starts a new one at the next start of the engine.
const maxLog = 5 << 20

// EngineFiles are what the app runs: the engine (agentd's Go binary), the Node.js it runs the ACP adapters on, and where
// its log goes. Node is empty when the app carries none; the engine then reports its adapters not ready.
type EngineFiles struct {
	Engine, Node, Home, Log string
}

// NodeBin is the directory of the Node.js the app carries, or "" without one.
func (f EngineFiles) NodeBin() string {
	if f.Node == "" {
		return ""
	}
	return filepath.Dir(f.Node)
}

// FindEngine finds what the app carries in its resources: engine/agentd, and node/bin/node for the adapters. Both are
// merged into the app's resources by mygo build from the platform's directory, and stay in a directory of their own
// under go run, so each is looked for in the resources and then in the platform's.
// AGENTD_APP_ENGINE and AGENTD_APP_NODE name others (development, tests); AGENTD_HOME moves the engine's home. An app
// without an engine cannot run; one without Node.js runs, and its adapters are not ready.
func FindEngine(resources, home string, env func(string) string) (EngineFiles, error) {
	f := EngineFiles{Engine: env("AGENTD_APP_ENGINE"), Node: env("AGENTD_APP_NODE"), Home: env("AGENTD_HOME")}
	if f.Home == "" {
		f.Home = filepath.Join(home, ".agentd")
	}
	f.Log = filepath.Join(f.Home, "logs", "daemon.log")
	if f.Engine == "" {
		f.Engine = carried(resources, "engine", "agentd")
	}
	if f.Node == "" {
		f.Node = carried(resources, "node", "bin", "node")
	}
	if f.Engine == "" || !exists(f.Engine) {
		return f, fmt.Errorf("this app carries no engine (looked for %q)", cmp.Or(f.Engine, filepath.Join(resources, "engine", "agentd")))
	}
	return f, nil
}

// carried is the path of a file the app ships, in the resources or else in the platform's directory of them; "" when it
// is in neither.
func carried(resources string, rel ...string) string {
	for _, dir := range []string{resources, filepath.Join(resources, runtime.GOOS+"-"+runtime.GOARCH)} {
		if p := filepath.Join(append([]string{dir}, rel...)...); exists(p) {
			return p
		}
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Engine is the agentd daemon the app runs, with a channel on its fd 3 (AGENTD_UI_FD): its states come in, the user's
// choices go out. Closing the channel ends it, with its agents.
type Engine struct {
	cmd     *exec.Cmd
	conn    net.Conn
	started time.Time
	write   sync.Mutex
	done    chan struct{}
	code    int
}

// StartEngine runs the engine in the user's home with `path` as its PATH, in a process group of its own, its output
// in its log. onState gets each state it sends.
func StartEngine(f EngineFiles, home, path string, onState func(State)) (*Engine, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("the engine's channel: %w", err)
	}
	theirs := os.NewFile(uintptr(fds[1]), "agentd-ui-engine")
	defer func() { _ = theirs.Close() }()
	// A net.Conn, on the poller: closing it ends a read in flight, so the engine sees its channel close.
	file := os.NewFile(uintptr(fds[0]), "agentd-ui")
	ours, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("the engine's channel: %w", err)
	}
	logFile, err := openLog(f.Log)
	if err != nil {
		_ = ours.Close()
		return nil, err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(f.Engine, "run")
	cmd.Dir = home
	// The last of a duplicated name wins: the app's PATH and channel replace whatever its own environment says.
	cmd.Env = append(os.Environ(), "PATH="+path, "AGENTD_UI_FD=3", "AGENTD_MANAGED=1", "AGENTD_HOME="+f.Home)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = ours.Close()
		return nil, fmt.Errorf("start the engine: %w", err)
	}
	e := &Engine{cmd: cmd, conn: ours, started: time.Now(), done: make(chan struct{})}
	go e.read(onState)
	go e.wait()
	return e, nil
}

// openLog opens the engine's log for appending, after moving one that grew past maxLog to daemon.log.1.
func openLog(file string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, fmt.Errorf("the engine's log: %w", err)
	}
	if st, err := os.Stat(file); err == nil && st.Size() > maxLog {
		_ = os.Rename(file, file+".1")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("the engine's log: %w", err)
	}
	return f, nil
}

func (e *Engine) read(onState func(State)) {
	in := bufio.NewScanner(e.conn)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for in.Scan() {
		if s, ok := ParseState(in.Bytes()); ok {
			onState(s)
		}
	}
}

func (e *Engine) wait() {
	err := e.cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		e.code = 0
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		e.code = exit.ExitCode()
	default:
		e.code = -1 // killed by a signal, or never waited on: a crash
	}
	_ = e.conn.Close()
	close(e.done)
}

// Send hands the engine a choice of the user.
func (e *Engine) Send(id string) {
	e.write.Lock()
	defer e.write.Unlock()
	_, _ = e.conn.Write(append(ActionLine(id), '\n'))
}

// Done is closed once the engine has exited; ExitCode and RanFor are then its exit.
func (e *Engine) Done() <-chan struct{} { return e.done }
func (e *Engine) ExitCode() int         { return e.code }
func (e *Engine) RanFor() time.Duration { return time.Since(e.started) }

// Stop ends the engine: it closes the channel, and the engine ends its agents and exits. One that does not within
// `grace` is ended with its process group, SIGTERM and then SIGKILL.
func (e *Engine) Stop(grace time.Duration) {
	e.write.Lock()
	_ = e.conn.Close()
	e.write.Unlock()
	select {
	case <-e.done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
		<-e.done
	}
}
