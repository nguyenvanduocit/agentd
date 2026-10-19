package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
)

func TestLoop(t *testing.T) {
	t.Run("install starts the launcher in the background, in the user's home, with AGENTD_RESTART=loop", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)
		if len(m.spawns) != 1 {
			t.Fatalf("spawns: %v", m.spawns)
		}
		s := m.spawns[0]
		if s.file != m.layout.Launcher || strings.Join(s.args, " ") != "run" || s.dir != m.d.Host.Home {
			t.Fatalf("spawn: %+v", s)
		}
		contains(t, "env", strings.Join(s.env, "\n"), "AGENTD_RESTART=loop", "AGENTD_HOME="+filepath.Join(m.root, "agentd"), "PATH=/usr/bin:/bin")
		contains(t, "stdout", m.out.String(),
			"no service manager here: the daemon runs in the background and restarts when it crashes, but it does not start at login or after a reboot",
			"agentd service: restart loop (no service manager)",
			"state:   running (pid 5001)",
		)
		notContains(t, "stdout", m.out.String(), "file:")
		if !m.exists(filepath.Dir(m.layout.LogFile)) {
			t.Fatal("the log directory is not there")
		}
	})

	t.Run("install with --dir starts the daemon there", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		dir := filepath.Join(m.root, "work")
		wantCode(t, m.run(cli.Service{Action: cli.ServiceInstall, Dir: &dir}), 0)
		if m.spawns[0].dir != dir {
			t.Fatalf("spawn: %+v", m.spawns[0])
		}
	})

	t.Run("install over a running loop ends it first", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)
		wantCode(t, m.do(cli.ServiceInstall), 0)
		if len(m.spawns) != 2 || len(m.termed) != 1 || m.termed[0] != 5001 {
			t.Fatalf("spawns %v, terminated %v", m.spawns, m.termed)
		}
	})

	t.Run("start leaves a loop that runs alone, and starts one that does not", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceStart), 0)
		wantCode(t, m.do(cli.ServiceStart), 0)
		if len(m.spawns) != 1 {
			t.Fatalf("spawns: %v", m.spawns)
		}
	})

	t.Run("restart ends the loop and starts another", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceStart), 0)
		wantCode(t, m.do(cli.ServiceRestart), 0)
		if len(m.spawns) != 2 || len(m.termed) != 1 {
			t.Fatalf("spawns %v, terminated %v", m.spawns, m.termed)
		}
		contains(t, "stdout", m.out.String(), "state:   running (pid 5002)")
	})

	t.Run("uninstall ends the loop and removes its pid file", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceStart), 0)
		m.out.Reset()
		wantCode(t, m.do(cli.ServiceUninstall), 0)
		if m.exists(m.layout.LoopPidFile) || len(m.termed) != 1 {
			t.Fatalf("pid file %v, terminated %v", m.exists(m.layout.LoopPidFile), m.termed)
		}
		contains(t, "stdout", m.out.String(), "removed the loop service "+testLabel+"; "+m.layout.Home+" stays")
	})

	t.Run("a pid file of a process that is gone is not a running loop", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.writeFile(m.layout.LoopPidFile, "99\n")
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "state:   not installed")
		m.writeFile(m.layout.LoopPidFile, "1\n") // init is no loop
		m.alive[1] = true
		wantCode(t, m.do(cli.ServiceStatus), 1)
	})

	t.Run("status tells how the last run ended", func(t *testing.T) {
		for code, want := range map[string]string{
			"0": "stopped (it exited 0",
			"1": "restarting after a crash (last exit code 1)",
		} {
			m := newMachine(t, Loop)
			m.writeFile(m.layout.LastExitFile, code+"\n")
			wantCode(t, m.do(cli.ServiceStatus), 1)
			contains(t, "stdout", m.out.String(), want)
		}
		m := newMachine(t, Loop)
		m.writeFile(m.layout.LastExitFile, "garbage\n")
		m.do(cli.ServiceStatus)
		contains(t, "stdout", m.out.String(), "not installed")
	})

	t.Run("a launcher that cannot start is an error", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		m.spawnErr = errors.New("fork failed")
		wantCode(t, m.do(cli.ServiceInstall), 1)
		contains(t, "stderr", m.errOut.String(), "fork failed")
	})

	t.Run("the loop polls for its pid file for 3 s at most", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.installLauncher()
		m.d.Procs = silentLauncher{m}
		wantCode(t, m.do(cli.ServiceInstall), 0)
		if len(m.sleeps) != 30 {
			t.Fatalf("sleeps: %d", len(m.sleeps))
		}
	})
}

// silentLauncher starts a launcher that never writes its pid file.
type silentLauncher struct{ *machine }

func (silentLauncher) StartDetached(string, []string, string, []string, *os.File) error { return nil }

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		forced   string
		answers  bool
		want     string
	}{
		{"launchd answers on macOS", "darwin", "", true, "agentd service: launchd, "},
		{"launchd does not answer: the loop", "darwin", "", false, "agentd service: restart loop"},
		{"systemd --user answers on Linux", "linux", "", true, "agentd service: systemd, "},
		{"no systemd --user: the loop", "linux", "", false, "agentd service: restart loop"},
		{"Windows has no manager", "windows", "", true, "agentd service: restart loop"},
		{"AGENTD_SERVICE_MANAGER forces the loop", "darwin", "loop", true, "agentd service: restart loop"},
		{"AGENTD_SERVICE_MANAGER forces systemd", "darwin", "systemd", false, "agentd service: systemd, "},
		{"an unknown AGENTD_SERVICE_MANAGER is ignored", "darwin", "upstart", true, "agentd service: launchd, "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMachine(t, Loop)
			m.d.Host.Platform = c.platform
			m.d.Host.Env["AGENTD_SERVICE_MANAGER"] = c.forced
			m.domainAnswers = c.answers
			m.do(cli.ServiceStatus)
			contains(t, "stdout", m.out.String(), c.want)
		})
	}
}

func TestLogs(t *testing.T) {
	t.Run("no log yet is exit 1", func(t *testing.T) {
		m := newMachine(t, Loop)
		wantCode(t, m.do(cli.ServiceLogs), 1)
		contains(t, "stderr", m.errOut.String(), "no log yet: "+m.layout.LogFile)
	})

	t.Run("the last lines of the log go to stdout", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.writeFile(m.layout.LogFile, "a\nb\n")
		m.tailOut = "b\n"
		wantCode(t, m.run(cli.Service{Action: cli.ServiceLogs, Lines: 1}), 0)
		if m.stdout.String() != "b\n" || !m.called("/usr/bin/tail -n 1 "+m.layout.LogFile) {
			t.Fatalf("stdout %q, calls %v", m.stdout.String(), m.calls)
		}
	})

	t.Run("tail's exit code is the exit code", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.writeFile(m.layout.LogFile, "a\n")
		m.failures["/usr/bin/tail -n"] = failed("tail: bad")
		wantCode(t, m.do(cli.ServiceLogs), 1)
	})

	t.Run("-f follows with tail's own output and code", func(t *testing.T) {
		m := newMachine(t, Loop)
		m.writeFile(m.layout.LogFile, "a\n")
		m.runCode = 3
		wantCode(t, m.run(cli.Service{Action: cli.ServiceLogs, Lines: 20, Follow: true}), 3)
		if len(m.ran) != 1 || strings.Join(m.ran[0], " ") != "/usr/bin/tail -n 20 -f "+m.layout.LogFile {
			t.Fatalf("ran: %v", m.ran)
		}
	})
}
