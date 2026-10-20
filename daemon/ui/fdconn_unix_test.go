//go:build !windows

package ui

import (
	"bufio"
	"os"
	"syscall"
	"testing"
	"time"
)

// testFDConn: the engine's side of the app's socket pair (app/engine.go StartEngine) is an inherited descriptor.
func testFDConn(t *testing.T) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	theirs := os.NewFile(uintptr(fds[0]), "app")
	defer func() { _ = theirs.Close() }()
	conn, err := FDConn(fds[1])
	if err != nil {
		_ = syscall.Close(fds[1])
		t.Fatal(err)
	}
	log, _ := testLog()
	actions := make(chan Action, 1)
	gone := make(chan struct{})
	l := NewAppLink(conn, func(a Action) { actions <- a }, func() { close(gone) }, log)
	l.Start()
	l.Update(NewState(baseFacts))
	_ = theirs.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(theirs).ReadString('\n')
	if err != nil || line != NewState(baseFacts).line()+"\n" {
		t.Fatalf("the app read %q, %v", line, err)
	}
	if _, err := theirs.WriteString(`{"t":"action","id":"restart"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-actions:
		if a.Kind != ActionRestart {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no action")
	}
	_ = theirs.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("onGone was not called when the app closed its end")
	}
}

func TestFDConnRefusesADescriptorThatIsNotOpen(t *testing.T) {
	if _, err := FDConn(987); err == nil {
		t.Fatal("no error for a descriptor that is not open")
	}
}
