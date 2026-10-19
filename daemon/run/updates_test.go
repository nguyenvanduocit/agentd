package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// installAgentd lays out an installed daemon in root: `<root>/mc/daemon/<version>/agentd`, a copy of the built
// cmd/agentd, with launcher.sh beside it as a release unpacks it. It returns the home and the binary's real path.
func installAgentd(t *testing.T, root, version string) (agentdHome, exe string) {
	t.Helper()
	agentdHome = filepath.Join(root, "mc")
	dir := filepath.Join(agentdHome, "daemon", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(binaries.agentd)
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(dir, "agentd")
	if err := os.WriteFile(exe, built, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "launcher.sh"), []byte("#!/bin/sh\n# the launcher of "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	return agentdHome, exe
}

func TestUpdatesAnInstalledGoBinaryCountsAsInstalledAndRefreshesTheLauncher(t *testing.T) {
	box := newSandbox(t)
	agentdHome, exe := installAgentd(t, box.root, home.Version)
	host := home.Host{Platform: "linux", Env: home.Env{"AGENTD_HOME": agentdHome}, Home: box.root}
	out := &syncBuffer{}
	l, ok := installedLayout(exe, host, proc.NewLogger(out, out))
	if !ok {
		t.Fatalf("%s does not count as installed; output: %s", exe, out)
	}
	if l.DaemonDir != filepath.ToSlash(filepath.Join(agentdHome, "daemon")) {
		t.Errorf("DaemonDir is %s", l.DaemonDir)
	}
	launcher, err := os.ReadFile(filepath.Join(agentdHome, "bin", "agentd"))
	if err != nil || !bytes.Contains(launcher, []byte("the launcher of "+home.Version)) {
		t.Errorf("bin/agentd is %q, %v", launcher, err)
	}
}

func TestUpdatesAnInstalledGoBinaryOfAnotherVersionKeepsUpdatesOff(t *testing.T) {
	box := newSandbox(t)
	agentdHome, exe := installAgentd(t, box.root, "9.9.9")
	host := home.Host{Platform: "linux", Env: home.Env{"AGENTD_HOME": agentdHome}, Home: box.root}
	out := &syncBuffer{}
	if _, ok := installedLayout(exe, host, proc.NewLogger(out, out)); ok {
		t.Fatal("a binary that is not the version of its directory counts as installed")
	}
	if !strings.Contains(out.String(), "updates are off") {
		t.Errorf("output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(agentdHome, "bin", "agentd")); err == nil {
		t.Error("a launcher was written for it")
	}
}

func TestUpdatesAnInstalledDaemonProcessDoesNotSayItsUpdatesAreOff(t *testing.T) {
	r := &rig{app: startApp(t), box: newSandbox(t)}
	r.box.writeServers(t, r.app.origin())
	agentdHome, exe := installAgentd(t, r.box.root, testVersion)
	real := binaries.agentd
	binaries.agentd = exe // the daemon is started from its installed place
	t.Cleanup(func() { binaries.agentd = real })
	r.start(t, sleepAgent, nil)
	r.app.waitHellos(t, 1)
	if r.d.says("updates are off: this daemon is not installed") {
		t.Fatalf("output:\n%s", r.d.out)
	}
	launcher, err := os.ReadFile(filepath.Join(agentdHome, "bin", "agentd"))
	if err != nil || !bytes.Contains(launcher, []byte("the launcher of "+testVersion)) {
		t.Errorf("bin/agentd is %q, %v", launcher, err)
	}
}
