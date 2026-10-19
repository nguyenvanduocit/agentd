//go:build !windows

package main

import (
	"bufio"
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

// EngineFiles are what the app runs: Node.js, the engine's daemon.js, and where its log goes.
type EngineFiles struct {
	Node, Script, Home, Log string
}

// FindEngine finds the engine the app carries in its resources: daemon.js in engine/, and node in node/bin of the
// platform's resources (merged into the app's by mygo build, in a directory of its own under go run).
// AGENTD_APP_NODE and AGENTD_APP_ENGINE name others (development, tests); AGENTD_HOME moves the engine's home.
func FindEngine(resources, home string, env func(string) string) (EngineFiles, error) {
	f := EngineFiles{Node: env("AGENTD_APP_NODE"), Script: env("AGENTD_APP_ENGINE"), Home: env("AGENTD_HOME")}
	if f.Home == "" {
		f.Home = filepath.Join(home, ".agentd")
	}
	f.Log = filepath.Join(f.Home, "logs", "daemon.log")
	if f.Script == "" {
		f.Script = filepath.Join(resources, "engine", "daemon.js")
	}
	if f.Node == "" {
		for _, dir := range []string{resources, filepath.Join(resources, runtime.GOOS+"-"+runtime.GOARCH)} {
			if node := filepath.Join(dir, "node", "bin", "node"); exists(node) {
				f.Node = node
				break
			}
		}
	}
	if f.Node == "" || !exists(f.Script) {
		return f, fmt.Errorf("this app carries no engine (node: %q, daemon.js: %q)", f.Node, f.Script)
	}
	return f, nil
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
	defer theirs.Close()
	// A net.Conn, on the poller: closing it ends a read in flight, so the engine sees its channel close.
	file := os.NewFile(uintptr(fds[0]), "agentd-ui")
	ours, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, fmt.Errorf("the engine's channel: %w", err)
	}
	logFile, err := openLog(f.Log)
	if err != nil {
		ours.Close()
		return nil, err
	}
	defer logFile.Close()
	cmd := exec.Command(f.Node, f.Script, "run")
	cmd.Dir = home
	// The last of a duplicated name wins: the app's PATH and channel replace whatever its own environment says.
	cmd.Env = append(os.Environ(), "PATH="+path, "AGENTD_UI_FD=3", "AGENTD_MANAGED=1", "AGENTD_HOME="+f.Home)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		ours.Close()
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
	e.conn.Close()
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
