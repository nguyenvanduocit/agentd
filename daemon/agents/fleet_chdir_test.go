package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// samePlace is whether two paths name one directory (/var is /private/var on macOS).
func samePlace(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return ra == rb
}

func TestFleetChdir(t *testing.T) {
	t.Run("starts the same agent again in the new directory on the same stream, and says nothing of the old one's end", func(t *testing.T) {
		f, sink, groups, ended := seamFleet(t, `sh -c 'pwd; cat'`)
		first, err := f.Open("s1", "claude")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the first pwd", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 1 })
		target := t.TempDir()

		moved, err := f.Chdir(context.Background(), "s1", target)
		if err != nil || moved.Cwd != target || moved.PID == first.PID || moved.PID <= 0 {
			t.Fatalf("got %+v, %v", moved, err)
		}
		waitFor(t, "the second pwd", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 2 })
		if got := sink.data(FrameOut)[1]; !samePlace(got, target) {
			t.Fatalf("the new agent runs in %q, want %q", got, target)
		}
		if alive(first.PID) {
			t.Fatal("the old agent is still running")
		}
		if _, gone := sink.exit(); gone || len(*ended) != 0 || f.Size() != 1 {
			t.Fatalf("the move was announced as an end: exit %v, ended %v, size %d", gone, *ended, f.Size())
		}
		if !f.Write("s1", "ping") {
			t.Fatal("the new agent does not take lines")
		}
		waitFor(t, "the echo", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 3 })
		if added, removed := groups.snapshot(); len(added) != 2 || len(removed) != 1 || removed[0] != first.PID {
			t.Fatalf("groups added %v removed %v", added, removed)
		}
		f.Close("s1")
		waitFor(t, "the end of the new agent", 5*time.Second, func() bool { _, ok := sink.exit(); return ok })
		waitFor(t, "the id to be free", 5*time.Second, func() bool { return f.Size() == 0 })
		if len(*ended) != 1 {
			t.Fatalf("ended %v", *ended)
		}
	})

	t.Run("moves again from where it moved to", func(t *testing.T) {
		f, sink, _, _ := seamFleet(t, `sh -c 'pwd; cat'`)
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		a, b := t.TempDir(), t.TempDir()
		for i, dir := range []string{a, b} {
			waitFor(t, "the agent's pwd", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == i+1 })
			if _, err := f.Chdir(context.Background(), "s1", dir); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "three pwd lines", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 3 })
		if got := sink.data(FrameOut)[2]; !samePlace(got, b) {
			t.Fatalf("got %q, want %q", got, b)
		}
	})

	t.Run("keeps the session where it is when the directory is refused", func(t *testing.T) {
		f, sink, _, _ := seamFleet(t, "cat")
		opened, err := f.Open("s1", "claude")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ id, dir, want string }{
			{"s1", "relative/dir", "is not an absolute path"},
			{"s1", filepath.Join(t.TempDir(), "missing"), "is not a directory"},
			{"s1", file, "is not a directory"},
			{"nobody", t.TempDir(), "session nobody is not running"},
		} {
			if _, err := f.Chdir(context.Background(), c.id, c.dir); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("chdir %q %q: got %v, want %q", c.id, c.dir, err, c.want)
			}
		}
		if !alive(opened.PID) || !f.Write("s1", "still") {
			t.Fatal("the agent was disturbed")
		}
		waitFor(t, "the echo", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 1 })
	})

	t.Run("refuses a directory outside the folders the user allowed, and one it cannot check", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "cat")
		inside, outside := t.TempDir(), t.TempDir()
		f.o.Folders = func() ([]string, error) { return []string{inside}, nil }
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Chdir(context.Background(), "s1", outside); err == nil || !strings.Contains(err.Error(), "is outside the folders you allowed this app") {
			t.Fatalf("outside: %v", err)
		}
		if _, err := f.Chdir(context.Background(), "s1", filepath.Join(inside, "..", filepath.Base(outside))); err == nil {
			t.Fatal("a path with .. was taken")
		}
		sub := filepath.Join(inside, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if moved, err := f.Chdir(context.Background(), "s1", sub); err != nil || moved.Cwd != sub {
			t.Fatalf("inside: %+v, %v", moved, err)
		}
		f.o.Folders = func() ([]string, error) { return nil, os.ErrPermission }
		if _, err := f.Chdir(context.Background(), "s1", sub); err == nil || !strings.Contains(err.Error(), "cannot check") {
			t.Fatalf("unreadable folders: %v", err)
		}
	})

	t.Run("an agent that cannot start in the new directory is gone, and the client is told", func(t *testing.T) {
		f, sink, _, ended := seamFleet(t, "cat")
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		f.o.Sink = func() FrameSink { // the directory vanishes between the check and the start
			_ = os.Remove(target)
			return sink
		}
		if _, err := f.Chdir(context.Background(), "s1", target); err == nil {
			t.Fatal("started in a directory that is gone")
		}
		if exit, ok := sink.exit(); !ok || exit.Code != 1 || len(*ended) != 1 || f.Size() != 0 {
			t.Fatalf("exit %+v %v, ended %v, size %d", exit, ok, *ended, f.Size())
		}
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatalf("the id opens again: %v", err)
		}
	})

	t.Run("a stopping daemon moves nothing", func(t *testing.T) {
		f, _, _, _ := seamFleet(t, "cat")
		if _, err := f.Open("s1", "claude"); err != nil {
			t.Fatal(err)
		}
		f.Drain()
		if _, err := f.Chdir(context.Background(), "s1", t.TempDir()); err == nil || err.Error() != "this daemon is stopping" {
			t.Fatalf("got %v", err)
		}
	})
}
