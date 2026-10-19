package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
)

func TestSystemd(t *testing.T) {
	unit := testLabel + ".service"

	t.Run("install writes the unit, reloads, enables and restarts it", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)
		text := m.read(m.unitFile())
		contains(t, "unit", text, "ExecStart=\""+m.layout.Launcher+"\" run", "WorkingDirectory=%h", "Environment=\"AGENTD_MANAGED=1\"", "StandardOutput=append:"+m.layout.LogFile)
		notContains(t, "unit", text, "AGENTD_SERVICE_")
		var steps []string
		for _, c := range m.calls {
			if strings.HasPrefix(c, "systemctl --user ") && !strings.Contains(c, "show") {
				steps = append(steps, strings.TrimPrefix(c, "systemctl --user "))
			}
		}
		if strings.Join(steps, "|") != "daemon-reload|enable "+unit+"|restart "+unit {
			t.Fatalf("steps: %v", steps)
		}
		contains(t, "stdout", m.out.String(), "started the systemd user unit "+unit, "agentd service: systemd, "+testLabel, "state:   running (pid 4242)", "file:    "+m.unitFile())
		notContains(t, "stdout", m.out.String(), "linger is off")
	})

	t.Run("linger off: it says how to keep the daemon after logout", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		m.unit.linger = "no"
		wantCode(t, m.do(cli.ServiceInstall), 0)
		contains(t, "stdout", m.out.String(), "linger is off: the daemon stops when you log out. To keep it running without a login, run: loginctl enable-linger ann")
	})

	t.Run("a step that fails is an error with systemctl's words", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		m.failures["systemctl enable"] = failed("boom")
		wantCode(t, m.do(cli.ServiceInstall), 1)
		contains(t, "stderr", m.errOut.String(), "systemctl --user enable "+unit+" failed: boom")
	})

	t.Run("a unit loaded from another file is another install's: not touched", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		m.unit.loaded, m.unit.Path = true, "/etc/systemd/user/"+unit
		wantCode(t, m.do(cli.ServiceInstall), 1)
		contains(t, "stderr", m.errOut.String(), "systemd already has a unit named "+unit+" from /etc/systemd/user/", "not touching it")
		if m.exists(m.unitFile()) {
			t.Fatal("the unit is written")
		}
	})

	t.Run("a --dir with a line break cannot go into a unit: nothing is written", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		dir := filepath.Join(m.root, "wo\nrk")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Skip("no line break in a file name here")
		}
		wantCode(t, m.run(cli.Service{Action: cli.ServiceInstall, Dir: &dir}), 1)
		contains(t, "stderr", m.errOut.String(), "line break")
		if m.exists(m.unitFile()) {
			t.Fatal("the unit is written")
		}
	})

	t.Run("--dir is the working directory", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		dir := filepath.Join(m.root, "work")
		wantCode(t, m.run(cli.Service{Action: cli.ServiceInstall, Dir: &dir}), 0)
		contains(t, "unit", m.read(m.unitFile()), "WorkingDirectory="+dir+"\n")
	})

	t.Run("status: a unit systemd does not know is not installed", func(t *testing.T) {
		m := newMachine(t, Systemd)
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "state:   not installed")
	})

	t.Run("status: stopped by the user, then started and restarted", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		m.unit.loaded, m.unit.Path, m.unit.LastExit = true, m.unitFile(), intPtr(0)
		wantCode(t, m.do(cli.ServiceStatus), 1)
		contains(t, "stdout", m.out.String(), "stopped (it exited 0")

		wantCode(t, m.do(cli.ServiceStart), 0)
		if !m.called("systemctl --user start " + unit) {
			t.Fatalf("calls: %v", m.calls)
		}
		wantCode(t, m.do(cli.ServiceStatus), 0)
		wantCode(t, m.do(cli.ServiceRestart), 0)
		if !m.called("systemctl --user restart " + unit) {
			t.Fatalf("calls: %v", m.calls)
		}
	})

	t.Run("start that fails is an error", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		m.failures["systemctl start"] = failed("Unit not found.")
		wantCode(t, m.do(cli.ServiceStart), 1)
		contains(t, "stderr", m.errOut.String(), "systemctl --user start "+unit+" failed: Unit not found.")
	})

	t.Run("uninstall disables the unit, removes the file and reloads", func(t *testing.T) {
		m := newMachine(t, Systemd)
		m.installLauncher()
		wantCode(t, m.do(cli.ServiceInstall), 0)
		m.calls = nil
		wantCode(t, m.do(cli.ServiceUninstall), 0)
		if m.exists(m.unitFile()) || m.unit.loaded {
			t.Fatalf("unit file %v, loaded %v", m.exists(m.unitFile()), m.unit.loaded)
		}
		if !m.called("systemctl --user disable --now "+unit) || m.calls[len(m.calls)-1] != "systemctl --user daemon-reload" {
			t.Fatalf("calls: %v", m.calls)
		}
		contains(t, "stdout", m.out.String(), "removed the systemd service "+testLabel+"; ")
	})
}

// The generated files, checked by the tools that read them (as the TS integration test does).
func TestGeneratedServiceFiles(t *testing.T) {
	t.Run("the plist passes plutil -lint, with and without environment, and with characters XML escapes", func(t *testing.T) {
		plutil, err := exec.LookPath("plutil")
		if err != nil {
			t.Skip("no plutil")
		}
		dir := t.TempDir()
		for _, env := range []map[string]string{{}, {"PATH": "/a&b:/usr/bin", "AGENTD_X": `<"'>`}} {
			file := filepath.Join(dir, "x.plist")
			text := Plist(PlistInput{Label: "com.agentd.daemon", Launcher: "/h/.agentd/bin/agentd", WorkDir: "/Users/a & b", LogFile: "/h/.agentd/logs/daemon.log", Env: env})
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(plutil, "-lint", file).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "OK") {
				t.Fatalf("plutil: %v\n%s", err, out)
			}
		}
	})

	t.Run("the unit passes systemd-analyze verify", func(t *testing.T) {
		analyze, err := exec.LookPath("systemd-analyze")
		if err != nil {
			t.Skip("no systemd-analyze")
		}
		file := filepath.Join(t.TempDir(), "agentd.service")
		text, err := SystemdUnit(UnitInput{Launcher: "/bin/true", LogFile: "/tmp/daemon.log", Env: map[string]string{"PATH": "/usr/bin"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		cmd := exec.Command(analyze, "--user", "verify", file)
		cmd.Stderr = &stderr
		_ = cmd.Run()
		if stderr.String() != "" {
			t.Fatalf("systemd-analyze: %s", stderr.String())
		}
	})
}
