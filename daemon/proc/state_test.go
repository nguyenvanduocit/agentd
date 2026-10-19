package proc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// binIn makes <parent>/<prefix>XXXXXX/bin; a name with six random characters, as installWrapper's parent has.
func binIn(t *testing.T, parent, prefix string) string {
	t.Helper()
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range len(alphabet) {
		dir := filepath.Join(parent, prefix+strings.Repeat(string(alphabet[i]), 6))
		if err := os.Mkdir(dir, 0o700); err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(dir, "bin")
	}
	t.Fatal("no free directory name")
	return ""
}

// startAfter: a state file left by a dead daemon (no process has that pid) that names bin; then a new daemon starts.
func startAfter(t *testing.T, bin string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sessions.json")
	record := `{"daemon":{"pid":1073741824,"lstart":"x"},"bin":"` + bin + `","groups":[]}`
	if err := os.WriteFile(file, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	NewSessionState(file, "", NewLogger(&out, &out)).Start()
	return file
}

func TestSessionStateRemovesTheWrapperDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no session state on Windows")
	}
	tmp := os.TempDir()
	own := binIn(t, tmp, "agentd-")
	startAfter(t, own)
	if _, err := os.Stat(filepath.Dir(own)); !os.IsNotExist(err) {
		t.Fatalf("the wrapper directory of a dead daemon is still there (err %v)", err)
	}

	notOurs := binIn(t, tmp, "mc-state-test-docs-")
	home, err := os.MkdirTemp(tmp, "mc-state-test-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	notInTmp := binIn(t, home, "agentd-")
	for _, bin := range []string{notOurs, notInTmp} {
		startAfter(t, bin)
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("%s was removed: %v", bin, err)
		}
	}
}

func TestSessionStateRecordsGroups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no session state on Windows")
	}
	file := filepath.Join(t.TempDir(), "run", "sessions.json")
	var out bytes.Buffer
	s := NewSessionState(file, "/t/bin", NewLogger(&out, &out))
	s.Start()
	s.Add(4242)
	s.Add(4243)
	s.Remove(4243)

	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ParseState(string(text))
	if !ok || got.Daemon.PID != os.Getpid() || got.Bin != "/t/bin" || len(got.Groups) != 1 || got.Groups[0].Pgid != 4242 {
		t.Fatalf("recorded %s", text)
	}
	if !strings.HasPrefix(string(text), `{"daemon":{"pid":`) || !strings.Contains(string(text), `"groups":[{"pgid":4242,`) {
		t.Fatalf("the file bytes differ from the TypeScript record: %s", text)
	}

	s.Clear()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a clean exit left the file (err %v)", err)
	}
}

func TestSessionStateWithNoGroupsWritesAnEmptyArray(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	var out bytes.Buffer
	NewSessionState(file, "", NewLogger(&out, &out)).Start()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(text), `"groups":[]}`) {
		t.Fatalf("got %s", text)
	}
}

func TestSessionStateReapsAnOrphanGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no session state on Windows")
	}
	cmd := exec.Command("sh", "-c", "sleep 60 & wait")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { SignalGroup(cmd.Process.Pid, true) })

	var lstart string
	for _, r := range psTable() {
		if r.PID == cmd.Process.Pid {
			lstart = r.Lstart
		}
	}
	if lstart == "" {
		t.Fatal("ps does not list the child")
	}
	file := filepath.Join(t.TempDir(), "sessions.json")
	record := fmt.Sprintf(`{"daemon":{"pid":1073741824,"lstart":"x"},"bin":"","groups":[{"pgid":%d,"lstart":%q}]}`, cmd.Process.Pid, lstart)
	if err := os.WriteFile(file, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	NewSessionState(file, "", NewLogger(&out, &out)).Start()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the orphan group is still running")
	}
	if !strings.HasPrefix(out.String(), "reaped 1 orphan group(s), 2 process(es) from a previous daemon") {
		t.Fatalf("said %q", out.String())
	}
}
