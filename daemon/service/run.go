package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const tail = "/usr/bin/tail"

// Processes is what the restart loop needs from the operating system besides running a program to the end.
type Processes interface {
	// Alive reports whether a process with this id exists.
	Alive(pid int) bool
	// Terminate sends SIGTERM to one process.
	Terminate(pid int) error
	// StartDetached starts a program that outlives this one: its own session, no stdin, stdout and stderr to out.
	StartDetached(file string, args []string, dir string, env []string, out *os.File) error
	// Run runs a program to the end with this process's stdin, stdout and stderr, and returns its exit code.
	Run(file string, args []string) int
}

// Deps is the machine `agentd service` acts on. Every file it reads or writes lies under Host.Home, or under
// $AGENTD_HOME and $AGENTD_SERVICE_DIR of Host.Env; a test points them at a temporary directory.
type Deps struct {
	Host home.Host
	// UID names the launchd domain gui/<uid>.
	UID int
	// PID names the temporary file of an atomic write.
	PID int
	// Exec runs launchctl, systemctl, loginctl and tail to the end (proc.Exec).
	Exec func(file string, args []string, env []string) proc.Exited
	// Sleep waits between the polls of a manager.
	Sleep func(time.Duration)
	Procs Processes
	Log   *proc.Logger
	// Stdout receives the lines of `service logs`.
	Stdout io.Writer
}

// NewDeps is the real machine: this process's host, user and processes.
func NewDeps(host home.Host, log *proc.Logger, stdout io.Writer) Deps {
	uid := max(os.Getuid(), 0)
	return Deps{Host: host, UID: uid, PID: os.Getpid(), Exec: proc.Exec, Sleep: time.Sleep, Procs: osProcesses{}, Log: log, Stdout: stdout}
}

// ctx is what one service manager needs to know about the service it controls.
type ctx struct {
	layout home.Layout
	// env is the environment of the shell that runs `service install`: PATH and the test seams are copied from it.
	env home.Env
	// home is the user's home: the daemon's working directory unless `service install --dir` names another.
	home string
	// workDir is empty when no --dir was given.
	workDir string
	// name is the launchd label or systemd unit name.
	name string
	// file is the definition file: the plist or the unit. Empty for the restart loop.
	file string
}

// manager is one way to keep the daemon running.
type manager interface {
	// install writes the definition and starts the service; when it runs already, restarts it so it runs the current version.
	install(c ctx) ([]string, error)
	// uninstall stops the service and removes its definition; it leaves ~/.agentd.
	uninstall(c ctx) error
	// status is nil when the service is not installed.
	status(c ctx) *Running
	// start starts a stopped service (after a Stop from the web UI); one that runs is left alone.
	start(c ctx) error
	// restart ends the daemon and starts it again, whatever state it was in.
	restart(c ctx) error
}

// detect is launchd on macOS and systemd --user on Linux when they answer; otherwise the launcher's own restart loop.
func (d Deps) detect() ManagerName {
	switch forced := ManagerName(d.Host.Env["AGENTD_SERVICE_MANAGER"]); forced {
	case Loop, Launchd, Systemd:
		return forced
	}
	switch d.Host.Platform {
	case "darwin":
		if d.Exec("launchctl", []string{"print", "gui/" + strconv.Itoa(d.UID)}, nil).Code == 0 {
			return Launchd
		}
	case "linux":
		if d.Exec("systemctl", []string{"--user", "show-environment"}, nil).Code == 0 {
			return Systemd
		}
	}
	return Loop
}

func (d Deps) manager(name ManagerName) manager {
	switch name {
	case Launchd:
		return launchd{d}
	case Systemd:
		return systemd{d}
	}
	return loop{d}
}

func (d Deps) context(name ManagerName, workDir string) ctx {
	svc := ServiceName(name, d.Host.Env)
	file := ""
	if name != Loop {
		file = ServiceFile(name, svc, d.Host.Home, d.Host.Env)
	}
	return ctx{layout: home.LayoutOf(home.AgentdHome(d.Host)), env: d.Host.Env, home: d.Host.Home, workDir: workDir, name: svc, file: file}
}

func (d Deps) installedVersion(c ctx) string {
	target, err := os.Readlink(c.layout.CurrentLink)
	if err != nil {
		return "none"
	}
	return filepath.Base(target)
}

func (d Deps) describe(name ManagerName, c ctx, now *Running) []string {
	where := "restart loop (no service manager)"
	if name != Loop {
		where = string(name) + ", " + c.name
	}
	lines := []string{
		"agentd service: " + where,
		"  state:   " + StateText(now),
		"  version: " + d.installedVersion(c),
		"  log:     " + c.layout.LogFile,
	}
	if c.file != "" {
		lines = append(lines, "  file:    "+c.file)
	}
	return lines
}

func (d Deps) showStatus(name ManagerName, c ctx, m manager) *Running {
	now := m.status(c)
	for _, line := range d.describe(name, c, now) {
		d.Log.Say(line)
	}
	return now
}

func launcherReady(c ctx) error {
	if _, err := os.Stat(c.layout.Launcher); err != nil {
		return fmt.Errorf("%s is missing: install the daemon first with the command from the app's web UI", c.layout.Launcher)
	}
	return nil
}

func (d Deps) logs(c ctx, cmd cli.Service) int {
	if _, err := os.Stat(c.layout.LogFile); err != nil {
		d.Log.Warn("no log yet: " + c.layout.LogFile)
		return 1
	}
	args := []string{"-n", strconv.Itoa(cmd.Lines)}
	if cmd.Follow {
		return d.Procs.Run(tail, append(args, "-f", c.layout.LogFile))
	}
	r := d.Exec(tail, append(args, c.layout.LogFile), nil)
	_, _ = io.WriteString(d.Stdout, r.Stdout)
	return r.Code
}

// serviceDir is `service install --dir <dir>`: an existing directory, as an absolute path; nil is the user's home.
func serviceDir(dir *string) (string, error) {
	if dir == nil {
		return "", nil
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("--dir %s is not a directory", abs)
	}
	return abs, nil
}

// RunService is `agentd service install|uninstall|start|restart|status|logs`; it returns the exit code. A failure is
// told on stderr and is exit code 1.
func RunService(cmd cli.Service, d Deps) int {
	code, err := d.run(cmd)
	if err != nil {
		d.Log.Warn(err.Error())
		return 1
	}
	return code
}

func (d Deps) run(cmd cli.Service) (int, error) {
	name := d.detect()
	workDir := ""
	if cmd.Action == cli.ServiceInstall {
		dir, err := serviceDir(cmd.Dir)
		if err != nil {
			return 0, err
		}
		workDir = dir
	}
	c := d.context(name, workDir)
	m := d.manager(name)
	switch cmd.Action {
	case cli.ServiceInstall:
		if err := launcherReady(c); err != nil {
			return 0, err
		}
		lines, err := m.install(c)
		if err != nil {
			return 0, err
		}
		for _, line := range lines {
			d.Log.Say(line)
		}
		d.showStatus(name, c, m)
		return 0, nil
	case cli.ServiceStart, cli.ServiceRestart:
		if err := launcherReady(c); err != nil {
			return 0, err
		}
		do := m.start
		if cmd.Action == cli.ServiceRestart {
			do = m.restart
		}
		if err := do(c); err != nil {
			return 0, err
		}
		d.showStatus(name, c, m)
		return 0, nil
	case cli.ServiceUninstall:
		if err := m.uninstall(c); err != nil {
			return 0, err
		}
		d.Log.Say(fmt.Sprintf("removed the %s service %s; %s stays (rm -rf it to remove the rest)", name, c.name, c.layout.Home))
		return 0, nil
	case cli.ServiceStatus:
		if now := d.showStatus(name, c, m); now != nil && now.Running {
			return 0, nil
		}
		return 1, nil
	case cli.ServiceLogs:
		return d.logs(c, cmd), nil
	}
	return 0, fmt.Errorf("unknown service action %q", cmd.Action)
}

// failure is the text of a failed program: its stderr, else its stdout.
func failure(r proc.Exited) string {
	if r.Stderr != "" {
		return strings.TrimSpace(r.Stderr)
	}
	return strings.TrimSpace(r.Stdout)
}

// realPath resolves symlinks; a file that is not there is its own path.
func realPath(file string) string {
	if r, err := filepath.EvalSymlinks(file); err == nil {
		return r
	}
	return file
}

// sameFile reports whether the definition file a manager loaded is the one of this install.
func sameFile(loaded, file string) bool {
	return realPath(loaded) == realPath(file)
}

// writeAtomic writes a definition by rename, so a manager never reads half of it.
func (d Deps) writeAtomic(file, text string) error {
	if err := os.MkdirAll(path.Dir(file), 0o777); err != nil {
		return err
	}
	tmp := file + "." + strconv.Itoa(d.PID) + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// removeFile removes a definition; one that is not there is removed already.
func removeFile(file string) error {
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
