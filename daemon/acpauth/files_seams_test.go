package acpauth

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// The system failing while the token is read (no descriptors left, an I/O error) says nothing about the file: it is not
// replaced. A replaced token would break every address a client was configured with, silently.
func TestATransientReadErrorNeverRotatesTheToken(t *testing.T) {
	for name, errno := range map[string]error{"no descriptors left": syscall.EMFILE, "an I/O error": syscall.EIO} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			good, err := Ensure(home)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(TokenFile(home))
			openToken = func(path string) (*os.File, error) { return nil, &os.PathError{Op: "open", Path: path, Err: errno} }
			t.Cleanup(func() { openToken = openPrivate })

			tok, err := Ensure(home)
			if err == nil || tok != "" || !errors.Is(err, errno) {
				t.Fatalf("Ensure() = %q, %v; want the error of the system and no token", tok, err)
			}
			if errors.Is(err, ErrInsecure) {
				t.Errorf("a failing system is reported as an insecure file: %v", err)
			}
			if _, err := Load(home); !errors.Is(err, errno) || errors.Is(err, ErrInsecure) {
				t.Errorf("Load() = %v; want the error of the system", err)
			}
			openToken = openPrivate
			after, _ := os.ReadFile(TokenFile(home))
			if string(before) != string(after) {
				t.Errorf("the token file changed from %q to %q", before, after)
			}
			if again, err := Ensure(home); err != nil || again != good {
				t.Errorf("after the failure Ensure() = %q, %v; want the same token %q", again, err, good)
			}
		})
	}
}

// What is judged is the file that was opened, not the name. Swap the name for a link at the worst moment, between the
// open and the judging: the open file is still the daemon's good token, and a rotation that lands there must not be
// taken for an attack, nor for a reason to make yet another token.
func TestWhatIsJudgedIsTheFileThatWasOpened(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	good, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir() + "/elsewhere"
	if err := os.WriteFile(elsewhere, []byte(New()), 0o644); err != nil {
		t.Fatal(err)
	}
	swapped := false
	afterOpen = func() {
		_ = os.Remove(TokenFile(home))
		_ = os.Symlink(elsewhere, TokenFile(home))
		swapped = true
	}
	t.Cleanup(func() { afterOpen = func() {} })

	got, err := Load(home)
	if !swapped {
		t.Fatal("the seam did not run")
	}
	if err != nil || got != good {
		t.Fatalf("Load() = %q, %v; the file that was opened held %q and that is what was read", got, err, good)
	}
}
