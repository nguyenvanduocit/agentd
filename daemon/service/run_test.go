package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

func contains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s lacks %q:\n%s", what, want, text)
		}
	}
}

func notContains(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(text, bad) {
			t.Errorf("%s has %q:\n%s", what, bad, text)
		}
	}
}

func wantCode(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("exit code %d, want %d", got, want)
	}
}

func intPtr(n int) *int { return &n }

func TestLaunchdInstall(t *testing.T) {
	t.Run("writes the plist, loads it, and tells where the daemon runs", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)

		plist := m.read(m.plistFile())
		contains(t, "plist", plist,
			"<string>"+testLabel+"</string>",
			"<string>"+m.layout.Launcher+"</string>",
			"<key>WorkingDirectory</key>\n  <string>"+m.d.Host.Home+"</string>", // the user's home, not `/`
			"<key>AGENTD_MANAGED</key>\n    <string>1</string>",
			"<key>AGENTD_HOME</key>",
		)
		notContains(t, "plist", plist, "AGENTD_SERVICE_LABEL", "AGENTD_SERVICE_DIR", "AGENTD_SERVICE_MANAGER")
		contains(t, "stdout", m.out.String(),
			"started the launchd agent "+testLabel,
			"agentd service: launchd, "+testLabel,
			"state:   running (pid 4242)",
			"file:    "+m.plistFile(),
			"log:     "+m.layout.LogFile,
		)
		if !m.called("launchctl enable "+m.target()) || !m.called("launchctl bootstrap gui/501 "+m.plistFile()) {
			t.Fatalf("calls: %v", m.calls)
		}
		if m.called("launchctl bootout " + m.target()) {
			t.Fatalf("nothing was loaded, nothing to boot out: %v", m.calls)
		}
		if info, err := os.Stat(m.plistFile()); err != nil || info.Mode().Perm()&0o022 != 0 {
			t.Fatalf("plist mode: %v %v", info, err)
		}
		if _, err := os.Stat(m.plistFile() + ".77.tmp"); err == nil {
			t.Fatal("the temporary file stays")
		}
		if !m.exists(filepath.Dir(m.layout.LogFile)) {
			t.Fatal("the log directory is not there")
		}
	})

	t.Run("a job that runs is booted out first, so it runs the current version", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{Running: true, PID: 1234, Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceInstall), 0)
		bootout, bootstrap := -1, -1
		for i, c := range m.calls {
			switch c {
			case "launchctl bootout " + m.target():
				bootout = i
			case "launchctl bootstrap gui/501 " + m.plistFile():
				bootstrap = i
			}
		}
		if bootout < 0 || bootstrap < bootout {
			t.Fatalf("calls: %v", m.calls)
		}
	})

	t.Run("a label loaded from another file is another install's: not touched", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{Running: true, PID: 1234, Path: "/elsewhere/" + testLabel + ".plist"}
		wantCode(t, m.do(cli.ServiceInstall), 1)
		contains(t, "stderr", m.errOut.String(), "launchd already runs a service named "+testLabel+" from /elsewhere/", "not touching it")
		if m.exists(m.plistFile()) || m.called("launchctl bootout "+m.target()) {
			t.Fatalf("it touched the other install: %v", m.calls)
		}
	})

	t.Run("bootstrap right after bootout fails with EIO for a while: it tries again", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.bootstrapFailures = 3
		wantCode(t, m.do(cli.ServiceInstall), 0)
		if len(m.sleeps) != 3 || m.sleeps[0] != 500*time.Millisecond {
			t.Fatalf("sleeps: %v", m.sleeps)
		}
	})

	t.Run("a bootstrap that never works is an error with launchctl's words", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.bootstrapFailures = 100
		wantCode(t, m.do(cli.ServiceInstall), 1)
		contains(t, "stderr", m.errOut.String(), "launchctl bootstrap "+m.plistFile()+" failed: Bootstrap failed: 5: Input/output error")
	})

	t.Run("--dir is the daemon's working directory", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		dir := filepath.Join(m.root, "work")
		wantCode(t, m.run(cli.Service{Action: cli.ServiceInstall, Dir: &dir}), 0)
		contains(t, "plist", m.read(m.plistFile()), "<key>WorkingDirectory</key>\n  <string>"+dir+"</string>")
	})

	t.Run("--dir that is not a directory stops before anything is written", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		dir := filepath.Join(m.root, "nope")
		wantCode(t, m.run(cli.Service{Action: cli.ServiceInstall, Dir: &dir}), 1)
		contains(t, "stderr", m.errOut.String(), "--dir "+dir+" is not a directory")
		if m.exists(m.plistFile()) {
			t.Fatal("the plist is written")
		}
	})

	t.Run("a missing launcher is told, and nothing is written", func(t *testing.T) {
		for _, action := range []cli.ServiceAction{cli.ServiceInstall, cli.ServiceStart, cli.ServiceRestart} {
			m := newMachine(t, Launchd)
			wantCode(t, m.do(action), 1)
			contains(t, string(action), m.errOut.String(), m.layout.Launcher+" is missing: install the daemon first")
			if len(m.calls) != 0 || m.exists(m.plistFile()) {
				t.Fatalf("%s ran %v or wrote a plist", action, m.calls)
			}
		}
	})
}

func TestLaunchdLifecycle(t *testing.T) {
	t.Run("status: not installed is exit 1; running is exit 0", func(t *testing.T) {
		m := newMachine(t, Launchd)
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "state:   not installed", "version: none")

		m = newMachine(t, Launchd)
		m.jobLoaded, m.job = true, Running{Running: true, PID: 4242, Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceStatus), 0)
		contains(t, "stdout", m.out.String(), "state:   running (pid 4242)")
	})

	t.Run("status shows the installed version", func(t *testing.T) {
		m := newMachine(t, Launchd)
		if err := os.MkdirAll(m.layout.DaemonDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(m.layout.DaemonDir, "0.4.0"), m.layout.CurrentLink); err != nil {
			t.Fatal(err)
		}
		m.do(cli.ServiceStatus)
		contains(t, "stdout", m.out.String(), "version: 0.4.0")
	})

	t.Run("after a Stop (the daemon exited 0) launchd leaves the job: status says stopped, exit 1", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.jobLoaded, m.job = true, Running{LastExit: intPtr(0), Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "stopped (it exited 0")
	})

	t.Run("after a crash the job waits for its restart", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.jobLoaded, m.job = true, Running{LastExit: intPtr(1), Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "restarting after a crash (last exit code 1)")
	})

	t.Run("start brings a stopped job back with kickstart, without killing", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{LastExit: intPtr(0), Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceStart), 0)
		if !m.called("launchctl kickstart "+m.target()) || !m.job.Running {
			t.Fatalf("calls: %v", m.calls)
		}
		contains(t, "stdout", m.out.String(), "state:   running")
	})

	t.Run("start leaves a job that runs alone", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{Running: true, PID: 9, Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceStart), 0)
		for _, c := range m.calls {
			if strings.Contains(c, "kickstart") || strings.Contains(c, "bootstrap") {
				t.Fatalf("calls: %v", m.calls)
			}
		}
	})

	t.Run("start of a job that is not loaded loads its plist; with no plist it says to install", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.writeFile(m.plistFile(), "<plist/>")
		wantCode(t, m.do(cli.ServiceStart), 0)
		if !m.called("launchctl bootstrap gui/501 " + m.plistFile()) {
			t.Fatalf("calls: %v", m.calls)
		}

		m = newMachine(t, Launchd)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceStart), 1)
		contains(t, "stderr", m.errOut.String(), testLabel+" is not installed: run agentd service install")
	})

	t.Run("start does not touch a label that another install loaded", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{LastExit: intPtr(0), Path: "/elsewhere.plist"}
		wantCode(t, m.do(cli.ServiceStart), 1)
		contains(t, "stderr", m.errOut.String(), "not touching it")
	})

	t.Run("restart kills and starts the loaded job", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{Running: true, PID: 9, Path: m.plistFile()}
		wantCode(t, m.do(cli.ServiceRestart), 0)
		if !m.called("launchctl kickstart -k " + m.target()) {
			t.Fatalf("calls: %v", m.calls)
		}
		contains(t, "stdout", m.out.String(), "state:   running (pid 4242)")
	})

	t.Run("restart of a job that is not loaded starts it", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.writeFile(m.plistFile(), "<plist/>")
		wantCode(t, m.do(cli.ServiceRestart), 0)
		if !m.called("launchctl bootstrap gui/501 " + m.plistFile()) {
			t.Fatalf("calls: %v", m.calls)
		}
	})

	t.Run("a kickstart that fails is an error with launchctl's words", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		m.jobLoaded, m.job = true, Running{Running: true, Path: m.plistFile()}
		m.failures["launchctl kickstart"] = failed("Operation not permitted")
		wantCode(t, m.do(cli.ServiceRestart), 1)
		contains(t, "stderr", m.errOut.String(), "launchctl kickstart failed: Operation not permitted")
	})

	t.Run("uninstall boots the job out and removes the file; nothing is left in launchd", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)
		m.out.Reset()
		wantCode(t, m.do(cli.ServiceUninstall), 0)
		if m.exists(m.plistFile()) || m.jobLoaded {
			t.Fatalf("plist exists: %v, job loaded: %v", m.exists(m.plistFile()), m.jobLoaded)
		}
		contains(t, "stdout", m.out.String(), "removed the launchd service "+testLabel+"; "+m.layout.Home+" stays (rm -rf it to remove the rest)")
		if got := m.exec("launchctl", []string{"print", m.target()}, nil); got.Code == 0 {
			t.Fatal("launchd still has the job")
		}
	})

	t.Run("uninstall of a service that is not there is fine", func(t *testing.T) {
		m := newMachine(t, Launchd)
		wantCode(t, m.do(cli.ServiceUninstall), 0)
	})

	t.Run("uninstall removes its own file, and leaves a job another install loaded", func(t *testing.T) {
		m := newMachine(t, Launchd)
		m.writeFile(m.plistFile(), "<plist/>")
		m.jobLoaded, m.job = true, Running{Running: true, Path: "/elsewhere.plist"}
		wantCode(t, m.do(cli.ServiceUninstall), 0)
		if m.called("launchctl bootout "+m.target()) || m.exists(m.plistFile()) {
			t.Fatalf("calls: %v", m.calls)
		}
	})
}

func failed(stderr string) proc.Exited { return proc.Exited{Code: 1, Stderr: stderr} }
