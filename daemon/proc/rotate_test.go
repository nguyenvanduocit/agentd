package proc

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func ops(steps []RotateStep) []RotateOp {
	var out []RotateOp
	for _, s := range steps {
		out = append(out, s.Op)
	}
	return out
}

func TestRotateSteps(t *testing.T) {
	t.Run("keeping 3 files: .2 goes, .1 becomes .2, the log is copied to .1 and truncated in place", func(t *testing.T) {
		want := []RotateStep{
			{Op: RotateRemove, File: "/l/daemon.log.2"},
			{Op: RotateRename, From: "/l/daemon.log.1", To: "/l/daemon.log.2"},
			{Op: RotateCopy, From: "/l/daemon.log", To: "/l/daemon.log.1"},
			{Op: RotateTruncate, File: "/l/daemon.log"},
		}
		if got := RotateSteps("/l/daemon.log", 3); !slices.Equal(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("keeping 4 files shifts every older one", func(t *testing.T) {
		want := []RotateOp{RotateRemove, RotateRename, RotateRename, RotateCopy, RotateTruncate}
		if got := ops(RotateSteps("f", 4)); !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("keeping 1 file only truncates", func(t *testing.T) {
		want := []RotateStep{{Op: RotateTruncate, File: "f"}}
		if got := RotateSteps("f", 1); !slices.Equal(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

func TestNeedsRotation(t *testing.T) {
	if NeedsRotation(10, 10) {
		t.Fatal("a file at the limit rotates")
	}
	if !NeedsRotation(11, 10) {
		t.Fatal("a file over the limit does not rotate")
	}
}

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if RotateLog(log, 10, 3) {
		t.Fatal("a missing file rotated")
	}
	write := func(name, text string) {
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(log, "0123456789")
	if RotateLog(log, 10, 3) {
		t.Fatal("a file at the limit rotated")
	}
	write(log, "first-log-line")
	if !RotateLog(log, 10, 3) {
		t.Fatal("a file over the limit did not rotate")
	}
	if read(log) != "" || read(log+".1") != "first-log-line" {
		t.Fatalf("after one rotation: log=%q .1=%q", read(log), read(log+".1"))
	}
	write(log, "second-log-line")
	RotateLog(log, 10, 3)
	if read(log+".1") != "second-log-line" || read(log+".2") != "first-log-line" {
		t.Fatalf("after two rotations: .1=%q .2=%q", read(log+".1"), read(log+".2"))
	}
	write(log, "third-log-line")
	RotateLog(log, 10, 3)
	if _, err := os.Stat(log + ".3"); err == nil {
		t.Fatal("keeping 3 files left a .3")
	}
	if read(log+".2") != "second-log-line" {
		t.Fatalf(".2 is %q", read(log+".2"))
	}
}
