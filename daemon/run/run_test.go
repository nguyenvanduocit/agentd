package run

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

func TestMakeTempDirsIsAgentdAndSixCharactersWithABinInsideAndMode0700(t *testing.T) {
	root, bin, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if !regexp.MustCompile(`^agentd-[0-9A-Za-z]{6}$`).MatchString(filepath.Base(root)) {
		t.Fatalf("root %q is not agentd- and six characters", root)
	}
	if filepath.Dir(root) != filepath.Clean(os.TempDir()) || bin != filepath.Join(root, "bin") {
		t.Fatalf("root %q, bin %q", root, bin)
	}
	for _, dir := range []string{root, bin} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, %v", dir, info, err)
		}
	}
	other, _, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	if other == root {
		t.Fatal("two runs got one directory")
	}
}

func TestTheNextStartRemovesTheDirectoryOfADeadDaemon(t *testing.T) {
	root, bin, err := makeTempDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	file := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(file, []byte(`{"daemon":{"pid":2147483000,"lstart":"x"},"bin":"`+bin+`","groups":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proc.NewSessionState(file, filepath.Join(t.TempDir(), "bin"), proc.NewLogger(&syncBuffer{}, &syncBuffer{})).Start()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v): the shape is not the one proc.SessionState cleans", root, err)
	}
}

func TestWorkDir(t *testing.T) {
	cwd, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty := ""
	for _, c := range []struct {
		name string
		dir  *string
		want string
		fail string
	}{
		{"where the daemon was started", nil, cwd, ""},
		{"an empty --dir is where it was started", &empty, cwd, ""},
		{"--dir", &dir, dir, ""},
		{"a relative --dir is made absolute", ptr("."), cwd, ""},
		{"a missing directory", ptr(filepath.Join(dir, "none")), "", "is not a directory"},
		{"a file", &file, "", "is not a directory"},
	} {
		got, err := workDir(c.dir)
		if c.fail != "" {
			if err == nil || !strings.Contains(err.Error(), c.fail) {
				t.Fatalf("%s: err = %v, want %q", c.name, err, c.fail)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%s: %q, %v, want %q", c.name, got, err, c.want)
		}
	}
}

func ptr(s string) *string { return &s }

func TestADaemonWithABrokenRegistryStopsWithAMessageNamingTheFile(t *testing.T) {
	box := newSandbox(t)
	if err := os.MkdirAll(filepath.Dir(box.configFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(box.configFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, box, nil, nil)
	if code := d.exitCode(t, waitLimit); code != 1 || !d.says("daemon.json") {
		t.Fatalf("exit code %d; output:\n%s", code, d.out)
	}
}

func TestRunningForAnAppThatIsNotPairedSaysHowToPairAndExits1(t *testing.T) {
	box := newSandbox(t)
	d := startDaemon(t, box, []string{"--app", "https://nowhere.example"}, nil)
	if code := d.exitCode(t, waitLimit); code != 1 {
		t.Fatalf("exit code %d; output:\n%s", code, d.out)
	}
	if !d.says("not paired with https://nowhere.example") || !d.says("agentd pair --app") {
		t.Fatalf("output:\n%s", d.out)
	}
}

func TestTheDaemonRemovesItsTemporaryDirectoryOnTheWayOut(t *testing.T) {
	tmp := t.TempDir()
	r := startRig(t, sleepAgent, map[string]string{"TMPDIR": tmp})
	entries, err := filepath.Glob(filepath.Join(tmp, "agentd-??????"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary directories: %v (%v)", entries, err)
	}
	r.d.signal(syscall.SIGTERM)
	r.d.exitCode(t, waitLimit)
	if _, err := os.Stat(entries[0]); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v)", entries[0], err)
	}
}
