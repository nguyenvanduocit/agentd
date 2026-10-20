package acpauth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// `agentd acp --rotate` prints the token Rotate returned; the daemon calls Ensure at every request. If an Ensure that
// meets the file mid-rename rotates again, the printed token is already dead. Rotate and the next read must agree
// while others Ensure.
func TestAFreshTokenIsTheOneTheNextReadSeesWhileOthersEnsure(t *testing.T) {
	home := t.TempDir()
	if _, err := Ensure(home); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if tok, err := Ensure(home); err != nil || !Valid(tok) {
					t.Errorf("Ensure() = %q, %v while the token was being rotated", tok, err)
					return
				}
			}
		}()
	}
	rounds := 1500
	if testing.Short() {
		rounds = 300
	}
	for i := 0; i < rounds; i++ {
		fresh, err := Rotate(home)
		if err != nil {
			t.Errorf("round %d: Rotate: %v", i, err)
			break
		}
		got, err := Load(home)
		if err != nil || got != fresh {
			t.Errorf("round %d: Rotate returned %q but the file holds %q (%v): an Ensure rotated it again", i, fresh, got, err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// A directory in the token's place is replaced when it is empty (an attacker or a mistake put it there), and left
// alone, with an error, when it holds something: the daemon does not delete what it did not write.
func TestADirectoryInPlaceOfTheTokenFileIsReplacedWhenEmptyAndLeftWhenItHoldsSomething(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	if err := os.MkdirAll(TokenFile(home), 0o700); err != nil {
		t.Fatal(err)
	}
	tok, err := Ensure(home)
	if err != nil || !Valid(tok) {
		t.Fatalf("Ensure() over an empty directory = %q, %v; want a fresh token", tok, err)
	}
	if fi, err := os.Lstat(TokenFile(home)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the token file is %v, %v; want a regular file", fi, err)
	}

	full := t.TempDir()
	keep := filepath.Join(TokenFile(full), "keep")
	if err := os.MkdirAll(TokenFile(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := Ensure(full); err == nil {
		t.Fatalf("Ensure() = %q over a directory that holds a file; want an error", tok)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "mine" {
		t.Errorf("the file inside the directory is %q, %v; it must be left alone", data, err)
	}
}

// A directory others could enter is tightened and the token in it is kept: the file was never exposed by the mode of
// the directory it sits in changing, so the addresses clients were configured with must keep working.
func TestADirectoryOthersCouldEnterIsTightenedAndTheGoodTokenInItIsKept(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	good, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Ensure(home)
	if err != nil || got != good {
		t.Fatalf("Ensure() = %q, %v after chmod 755 of the directory; want the token it had, %q", got, err, good)
	}
	if mode := modeOf(t, Dir(home)); mode != 0o700 {
		t.Errorf("the directory has mode %o, want 700", mode)
	}
}

// Modes the owner cannot or must not use: group-writable, unreadable even by the owner. Both are replaced.
func TestATokenFileWithAnotherModeIsReplaced(t *testing.T) {
	unixOnly(t)
	for name, mode := range map[string]os.FileMode{"group readable and writable": 0o660, "readable by nobody, not even the owner": 0o000, "world writable": 0o666} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			old := New()
			if err := os.MkdirAll(Dir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(TokenFile(home), []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(TokenFile(home), mode); err != nil {
				t.Fatal(err)
			}
			tok, err := Ensure(home)
			if err != nil || !Valid(tok) || tok == old {
				t.Fatalf("Ensure() = %q, %v; want a new token, not %q", tok, err, old)
			}
			if got := modeOf(t, TokenFile(home)); got != 0o600 {
				t.Errorf("the new file has mode %o, want 600", got)
			}
		})
	}
}

// How a token someone saved with an editor may end.
func TestATokenFileMayEndInCRLFOrInNothing(t *testing.T) {
	for name, ending := range map[string]string{"CRLF": "\r\n", "nothing": "", "two newlines": "\n\n"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tok := New()
			if err := os.MkdirAll(Dir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(TokenFile(home), []byte(tok+ending), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := Ensure(home); err != nil || got != tok {
				t.Fatalf("Ensure() = %q, %v; the token must stay %q", got, err, tok)
			}
		})
	}
}

// Ensure must come back, not wait, whatever sits in the token's place.
func within(t *testing.T, what string, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}
