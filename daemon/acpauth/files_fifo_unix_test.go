//go:build unix

package acpauth

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// A named pipe in the token's place would make a plain open wait for a writer for ever, and the daemon reads the token
// at every request. It is replaced, and nothing waits.
func TestAFifoInPlaceOfTheTokenFileIsReplacedAndNothingWaits(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(TokenFile(home), 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	var tok string
	var err error
	within(t, "Ensure over a FIFO", 5*time.Second, func() { tok, err = Ensure(home) })
	if err != nil || !Valid(tok) {
		t.Fatalf("Ensure() = %q, %v; want a fresh token", tok, err)
	}
	if fi, lerr := os.Lstat(TokenFile(home)); lerr != nil || !fi.Mode().IsRegular() {
		t.Errorf("the token file is %v, %v; want a regular file", fi, lerr)
	}
}

// Load of a FIFO is a refusal that comes back at once.
func TestLoadOfAFifoRefusesAtOnce(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(TokenFile(home), 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	var err error
	within(t, "Load of a FIFO", 5*time.Second, func() { _, err = Load(home) })
	if err == nil {
		t.Fatal("Load() of a FIFO succeeded")
	}
}
