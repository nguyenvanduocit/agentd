package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshLauncher(t *testing.T) {
	scriptDir := t.TempDir()
	l := LayoutOf(filepath.Join(t.TempDir(), ".agentd"))

	if wrote, err := RefreshLauncher(scriptDir, l); err != nil || wrote {
		t.Fatalf("no shipped launcher.sh: wrote = %v, err = %v; want false, nil", wrote, err)
	}

	if err := os.WriteFile(filepath.Join(scriptDir, "launcher.sh"), []byte("#!/bin/sh\necho one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := RefreshLauncher(scriptDir, l); err != nil || !wrote {
		t.Fatalf("first install: wrote = %v, err = %v", wrote, err)
	}
	fi, err := os.Stat(l.Launcher)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("launcher: %v, %v; want mode 0755", fi, err)
	}
	if wrote, err := RefreshLauncher(scriptDir, l); err != nil || wrote {
		t.Errorf("same content: wrote = %v, err = %v; want false", wrote, err)
	}

	if err := os.WriteFile(filepath.Join(scriptDir, "launcher.sh"), []byte("#!/bin/sh\necho two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := RefreshLauncher(scriptDir, l); err != nil || !wrote {
		t.Fatalf("changed content: wrote = %v, err = %v", wrote, err)
	}
	if got, _ := os.ReadFile(l.Launcher); string(got) != "#!/bin/sh\necho two\n" {
		t.Errorf("launcher = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(l.Launcher))
	if len(entries) != 1 {
		t.Errorf("bin dir holds %d entries, want only the launcher", len(entries))
	}
}
