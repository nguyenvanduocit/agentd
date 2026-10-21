//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/run"
	agentui "github.com/nguyenvanduocit/agentd/daemon/ui"
)

// maxLog is how large the engine's log may grow before the app starts a new one at the next start of the engine.
const maxLog = 5 << 20

// EngineFiles are what the engine uses of the app: the Node.js it runs the ACP adapters on, the folder it keeps its
// files in, and where its log goes. Node is empty when the app carries none; the engine then reports its adapters not
// ready.
type EngineFiles struct {
	Node, Home, Log string
}

// NodeBin is the directory of the Node.js the app carries, or "" without one.
func (f EngineFiles) NodeBin() string {
	if f.Node == "" {
		return ""
	}
	return filepath.Dir(f.Node)
}

// Why the engine does not start. The error that reaches the log says it in full; what the person reads is PlainReason.
var (
	errLog = errors.New("the engine's log")
)

// couldNotStart is what the person reads for every reason below that has no sentence of its own: the log has the rest.
const couldNotStart = "agentd could not start. Open the log for details."

// plainReasons are the sentences for the reasons a person can act on. The words are the person's: agentd, this copy of
// it, the folder it keeps its files in; never the engine, a path or the error of the system.
var plainReasons = []struct {
	because error
	say     string
}{
	{errLog, "agentd cannot write its log in ~/.agentd. Check that folder, then choose Start."},
}

// PlainReason is the one sentence the menu and the window show for an engine that did not start.
func PlainReason(err error) string {
	for _, r := range plainReasons {
		if errors.Is(err, r.because) {
			return r.say
		}
	}
	return couldNotStart
}

// NoteInLog appends a line to the engine's log, the file Open log shows, so the error behind a plain sentence can be
// found there. It does nothing without a path or when the log cannot be written: the standard log has the error too.
func NoteInLog(file, text string) {
	if file == "" {
		return
	}
	f, err := openLog(file)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), text)
}

// FindFiles finds what the app carries in its resources: node/bin/node for the adapters. It is merged into the app's
// resources by mygo build from the platform's directory, and stays in a directory of its own under go run, so it is
// looked for in the resources and then in the platform's. AGENTD_APP_NODE names another (development, tests);
// AGENTD_HOME moves the engine's home. An app without Node.js runs, and its adapters are not ready.
func FindFiles(resources, home string, env func(string) string) EngineFiles {
	f := EngineFiles{Node: env("AGENTD_APP_NODE"), Home: env("AGENTD_HOME")}
	if f.Home == "" {
		f.Home = filepath.Join(home, ".agentd")
	}
	f.Log = filepath.Join(f.Home, "logs", "daemon.log")
	if f.Node == "" {
		f.Node = carried(resources, "node", "bin", "node")
	}
	return f
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

// Engine is the agentd daemon (daemon/run) running inside the app, in a goroutine. It shows its state and asks the
// user through the link the app gives it, and does what the user chooses through Send. Stopping it ends its agents.
type Engine struct {
	cancel  context.CancelFunc
	tracker *proc.Tracker
	started time.Time
	done    chan struct{}
	code    int

	mu       sync.Mutex
	onAction func(agentui.Action)
}

// OpenLink is how the engine reaches the app: it opens the link that shows the engine's state, and calls onAction with
// each choice of the user.
type OpenLink = func(onAction func(agentui.Action)) agentui.Link

// StartEngine runs the engine in this process, in the user's home with `path` as its PATH, its output in its log. open
// is the link to the app.
func StartEngine(f EngineFiles, home string, path string, open OpenLink) (*Engine, error) {
	logFile, err := openLog(f.Log)
	if err != nil {
		return nil, err
	}
	host, err := engineHost(home, path, f.Home)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cancel: cancel, tracker: proc.NewTracker(), started: time.Now(), done: make(chan struct{})}
	log := proc.NewLogger(logFile, logFile)
	deps := run.Deps{Log: log, Host: host, Tracker: e.tracker, UI: func(onAction func(agentui.Action)) agentui.Link {
		e.mu.Lock()
		e.onAction = onAction
		e.mu.Unlock()
		return open(onAction)
	}}
	go func() {
		defer close(e.done)
		defer func() { _ = logFile.Close() }()
		defer log.CrashOnPanic(e.tracker)
		e.code = run.Daemon(ctx, cli.Run{Dir: &home}, deps)
	}()
	return e, nil
}

// engineHost is the machine the engine sees: this process's environment, with the app's PATH and home on top of it, and
// agentd.app named as what manages it (a Restart from the window exits 75, and the app starts the engine again).
func engineHost(userHome, path, agentdHome string) (home.Host, error) {
	host, err := home.CurrentHost()
	if err != nil {
		return home.Host{}, err
	}
	host.Env["PATH"] = path
	host.Env["AGENTD_MANAGED"] = "1"
	host.Env["AGENTD_HOME"] = agentdHome
	host.Home = userHome
	return host, nil
}

// openLog opens the engine's log for appending, after moving one that grew past maxLog to daemon.log.1.
func openLog(file string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, fmt.Errorf("%w: %w", errLog, err)
	}
	if st, err := os.Stat(file); err == nil && st.Size() > maxLog {
		_ = os.Rename(file, file+".1")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLog, err)
	}
	return f, nil
}

// Send hands the engine a choice of the user, in the id of the menu entry; an id that is no choice of the engine's
// does nothing.
func (e *Engine) Send(id string) {
	action, ok := agentui.ActionOf(id)
	if !ok {
		return
	}
	e.mu.Lock()
	act := e.onAction
	e.mu.Unlock()
	if act != nil {
		act(action)
	}
}

// Done is closed once the engine has exited; ExitCode and RanFor are then its exit.
func (e *Engine) Done() <-chan struct{} { return e.done }
func (e *Engine) ExitCode() int         { return e.code }
func (e *Engine) RanFor() time.Duration { return time.Since(e.started) }

// Stop ends the engine: it ends its agents and exits. One that does not within `grace` leaves its tracked process groups
// to be killed.
func (e *Engine) Stop(grace time.Duration) {
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(grace):
		e.tracker.KillAll()
	}
}
