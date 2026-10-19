package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	testUID   = 501
	testLabel = "com.agentd.daemon.test.ab12"
	jobPID    = 4242
)

// machine is a computer with fakes of launchctl, systemctl, loginctl and tail, and of the processes of the restart
// loop. Its files are in a temporary directory: the user's real service is never touched.
type machine struct {
	t      *testing.T
	d      Deps
	out    *bytes.Buffer
	errOut *bytes.Buffer
	stdout *bytes.Buffer
	root   string
	layout home.Layout
	// definition is where the manager's file goes (AGENTD_SERVICE_DIR).
	definition string

	calls  []string
	sleeps []time.Duration

	// what launchd and systemd have loaded
	job  Running
	unit struct {
		Running
		loaded bool
		linger string
	}
	jobLoaded bool

	// failures[<program> <verb>] is how a call fails.
	failures map[string]proc.Exited
	// bootstrapFailures is how many bootstraps fail with EIO before one works.
	bootstrapFailures int
	// domainAnswers is whether launchctl print gui/<uid> and systemctl show-environment succeed.
	domainAnswers bool
	tailOut       string

	// the restart loop's processes
	alive    map[int]bool
	nextPID  int
	spawns   []spawn
	ran      [][]string
	runCode  int
	termed   []int
	spawnErr error
}

type spawn struct {
	file string
	args []string
	dir  string
	env  []string
}

func newMachine(t *testing.T, name ManagerName) *machine {
	t.Helper()
	root := t.TempDir()
	m := &machine{t: t, root: root, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, stdout: &bytes.Buffer{}, failures: map[string]proc.Exited{}, domainAnswers: true, alive: map[int]bool{}, nextPID: 5000}
	m.definition = filepath.Join(root, "svc")
	for _, dir := range []string{m.definition, filepath.Join(root, "home"), filepath.Join(root, "work")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	platform := "linux"
	if name == Launchd {
		platform = "darwin"
	}
	label := testLabel
	host := home.Host{Platform: platform, Home: filepath.Join(root, "home"), Env: home.Env{
		"PATH":                   "/usr/bin:/bin",
		"USER":                   "ann",
		"AGENTD_HOME":            filepath.Join(root, "agentd"),
		"AGENTD_SERVICE_DIR":     m.definition,
		"AGENTD_SERVICE_LABEL":   label,
		"AGENTD_SERVICE_MANAGER": string(name),
	}}
	m.layout = home.LayoutOf(home.AgentdHome(host))
	m.d = Deps{Host: host, UID: testUID, PID: 77, Exec: m.exec, Sleep: func(d time.Duration) { m.sleeps = append(m.sleeps, d) }, Procs: m, Log: proc.NewLogger(m.out, m.errOut), Stdout: m.stdout}
	m.unit.linger = "yes"
	return m
}

func (m *machine) installLauncher() {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.layout.Launcher), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(m.layout.Launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) writeFile(file, text string) {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

// run is the command line `agentd service <action>` with a launcher installed; it returns the exit code.
func (m *machine) run(cmd cli.Service) int {
	m.t.Helper()
	if cmd.Lines == 0 {
		cmd.Lines = cli.DefaultLogLines
	}
	return RunService(cmd, m.d)
}

func (m *machine) do(action cli.ServiceAction) int {
	m.t.Helper()
	return m.run(cli.Service{Action: action})
}

func (m *machine) called(call string) bool {
	for _, c := range m.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (m *machine) plistFile() string { return filepath.Join(m.definition, testLabel+".plist") }
func (m *machine) unitFile() string  { return filepath.Join(m.definition, testLabel+".service") }
func (m *machine) target() string    { return fmt.Sprintf("gui/%d/%s", testUID, testLabel) }
func (m *machine) read(file string) string {
	m.t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		m.t.Fatal(err)
	}
	return string(b)
}

func (m *machine) exists(file string) bool {
	_, err := os.Stat(file)
	return err == nil
}

// exec is proc.Exec of this machine.
func (m *machine) exec(file string, args []string, _ []string) proc.Exited {
	call := file + " " + strings.Join(args, " ")
	m.calls = append(m.calls, call)
	verb := ""
	for _, a := range args {
		if a != "--user" {
			verb = a
			break
		}
	}
	if failed, ok := m.failures[file+" "+verb]; ok {
		return failed
	}
	switch file {
	case "launchctl":
		return m.launchctl(verb, args)
	case "systemctl":
		return m.systemctl(verb, args)
	case "loginctl":
		return proc.Exited{Stdout: "Linger=" + m.unit.linger + "\n"}
	case tail:
		return proc.Exited{Stdout: m.tailOut}
	}
	return proc.Exited{Code: 127, Stderr: "no such program"}
}

func (m *machine) launchctl(verb string, args []string) proc.Exited {
	switch verb {
	case "print":
		if args[1] == fmt.Sprintf("gui/%d", testUID) {
			if m.domainAnswers {
				return proc.Exited{}
			}
			return proc.Exited{Code: 113, Stderr: "Could not find service"}
		}
		if !m.jobLoaded {
			return proc.Exited{Code: 113, Stderr: "Could not find service"}
		}
		text := "gui/501/" + testLabel + " = {\n\tactive count = 1\n\tpath = " + m.job.Path + "\n"
		if m.job.Running {
			text += "\tstate = running\n\n\tpid = " + fmt.Sprint(m.job.PID) + "\n\tlast exit code = (never exited)\n"
		} else {
			text += "\tstate = not running\n"
			if m.job.LastExit != nil {
				text += "\tlast exit code = " + fmt.Sprint(*m.job.LastExit) + "\n"
			}
		}
		return proc.Exited{Stdout: text + "}\n"}
	case "bootstrap":
		if m.bootstrapFailures > 0 {
			m.bootstrapFailures--
			return proc.Exited{Code: 5, Stderr: "Bootstrap failed: 5: Input/output error"}
		}
		if !m.exists(args[2]) {
			return proc.Exited{Code: 5, Stderr: "no such plist"}
		}
		m.jobLoaded = true
		m.job = Running{Running: true, PID: jobPID, Path: args[2]}
	case "bootout":
		m.jobLoaded = false
	case "kickstart":
		if !m.jobLoaded {
			return proc.Exited{Code: 113, Stderr: "Could not find service"}
		}
		m.job.Running, m.job.PID = true, jobPID
	}
	return proc.Exited{}
}

func (m *machine) systemctl(verb string, args []string) proc.Exited {
	switch verb {
	case "show-environment":
		if m.domainAnswers {
			return proc.Exited{}
		}
		return proc.Exited{Code: 1, Stderr: "Failed to connect to bus"}
	case "show":
		if !m.unit.loaded {
			return proc.Exited{Stdout: "LoadState=not-found\nActiveState=inactive\nExecMainStatus=0\nMainPID=0\nFragmentPath=\n"}
		}
		state := "inactive"
		if m.unit.Running.Running {
			state = "active"
		}
		return proc.Exited{Stdout: fmt.Sprintf("LoadState=loaded\nActiveState=%s\nExecMainStatus=%d\nMainPID=%d\nFragmentPath=%s\n", state, exitOf(m.unit.LastExit), m.unit.PID, m.unit.Path)}
	case "daemon-reload":
		file := m.unitFile()
		if m.unit.Path != "" {
			file = m.unit.Path
		}
		if m.exists(file) {
			m.unit.loaded, m.unit.Path = true, file
		} else {
			m.unit.loaded = false
		}
	case "restart", "start":
		m.unit.Running.Running, m.unit.PID = true, jobPID
	case "disable":
		m.unit.Running.Running, m.unit.PID = false, 0
	}
	return proc.Exited{}
}

func exitOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Processes of the restart loop.

func (m *machine) Alive(pid int) bool { return m.alive[pid] }

func (m *machine) Terminate(pid int) error {
	m.termed = append(m.termed, pid)
	m.alive[pid] = false
	return nil
}

// StartDetached is the launcher: it writes its process id where the loop manager looks for it.
func (m *machine) StartDetached(file string, args []string, dir string, env []string, _ *os.File) error {
	if m.spawnErr != nil {
		return m.spawnErr
	}
	m.nextPID++
	m.alive[m.nextPID] = true
	m.spawns = append(m.spawns, spawn{file, args, dir, env})
	m.writeFile(m.layout.LoopPidFile, fmt.Sprintf("%d\n", m.nextPID))
	return nil
}

func (m *machine) Run(file string, args []string) int {
	m.ran = append(m.ran, append([]string{file}, args...))
	return m.runCode
}
