package acpauth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func unixOnly(t *testing.T) {
	t.Helper()
	if !posix() {
		t.Skip("file modes and symbolic links are not the same on Windows")
	}
}

func TestEnsureMakesTheTokenInAPrivateFileAndKeepsIt(t *testing.T) {
	home := t.TempDir()
	first, err := Ensure(home)
	if err != nil || !Valid(first) {
		t.Fatalf("Ensure() = %q, %v", first, err)
	}
	again, err := Ensure(home)
	if err != nil || again != first {
		t.Fatalf("a second Ensure() = %q, %v; want the same token %q (a client's configured address must keep working)", again, err, first)
	}
	loaded, err := Load(home)
	if err != nil || loaded != first {
		t.Fatalf("Load() = %q, %v; want %q", loaded, err, first)
	}
	if posix() {
		if mode := modeOf(t, Dir(home)); mode != 0o700 {
			t.Errorf("the directory has mode %o, want 700", mode)
		}
		if mode := modeOf(t, TokenFile(home)); mode != 0o600 {
			t.Errorf("the token file has mode %o, want 600", mode)
		}
	}
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestLoadDoesNotCreateAnything(t *testing.T) {
	home := t.TempDir()
	if tok, err := Load(home); !errors.Is(err, fs.ErrNotExist) || tok != "" {
		t.Fatalf("Load() on a home with no token = %q, %v; want fs.ErrNotExist", tok, err)
	}
	if _, err := os.Stat(Dir(home)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load created %s: %v", Dir(home), err)
	}
}

func TestEnsureMakesANewTokenOfWhatIsNotOne(t *testing.T) {
	for name, content := range map[string]string{
		"empty":             "",
		"garbage":           "this is not a token",
		"a short token":     strings.Repeat("a", TokenLen-1),
		"a long token":      strings.Repeat("a", TokenLen+1),
		"binary":            "\x00\x01\x02\x03",
		"two tokens":        strings.Repeat("a", TokenLen) + "\n" + strings.Repeat("b", TokenLen),
		"a huge file":       strings.Repeat("a", 1<<20),
		"standard base64":   strings.Repeat("a", TokenLen-2) + "+/",
		"spaces around it":  "  " + strings.Repeat("a", TokenLen) + "  garbage",
		"the token and not": strings.Repeat("a", TokenLen) + "x",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(Dir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(TokenFile(home), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			tok, err := Ensure(home)
			if err != nil || !Valid(tok) || strings.Contains(content, tok) {
				t.Fatalf("Ensure() = %q, %v; want a fresh valid token", tok, err)
			}
			if got, err := Load(home); err != nil || got != tok {
				t.Errorf("Load() = %q, %v; want %q", got, err, tok)
			}
		})
	}
}

func TestATokenFileMayEndInANewline(t *testing.T) {
	home := t.TempDir()
	tok := New()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenFile(home), []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Ensure(home); err != nil || got != tok {
		t.Fatalf("Ensure() = %q, %v; a token someone saved with an editor must stay %q", got, err, tok)
	}
}

func TestATokenOthersCouldReadIsReplaced(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	old := New()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenFile(home), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(TokenFile(home), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, err := Ensure(home)
	if err != nil || tok == old || !Valid(tok) {
		t.Fatalf("Ensure() = %q, %v; a token readable by group or others may have been read, so it must change from %q", tok, err, old)
	}
	if mode := modeOf(t, TokenFile(home)); mode != 0o600 {
		t.Errorf("the new file has mode %o, want 600", mode)
	}
}

func TestADirectoryOthersCouldEnterIsTightened(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(home); err != nil {
		t.Fatal(err)
	}
	if mode := modeOf(t, Dir(home)); mode != 0o700 {
		t.Errorf("the directory has mode %o, want 700", mode)
	}
}

func TestASymbolicLinkInPlaceOfTheTokenFileIsReplacedNotFollowed(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	planted := New()
	if err := os.WriteFile(elsewhere, []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, TokenFile(home)); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(home); err == nil {
		t.Fatalf("Load() followed a symbolic link and returned %q", got)
	}
	tok, err := Ensure(home)
	if err != nil || tok == planted || !Valid(tok) {
		t.Fatalf("Ensure() = %q, %v; the planted token %q must not be used", tok, err, planted)
	}
	if fi, err := os.Lstat(TokenFile(home)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the token file is %v, %v; want a regular file", fi, err)
	}
	if data, _ := os.ReadFile(elsewhere); string(data) != planted {
		t.Errorf("the link's target was written to: %q", data)
	}
}

func TestASymbolicLinkInPlaceOfTheDirectoryIsRefusedAndNothingIsWrittenThroughIt(t *testing.T) {
	unixOnly(t)
	home := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, Dir(home)); err != nil {
		t.Fatal(err)
	}
	if tok, err := Ensure(home); err == nil {
		t.Fatalf("Ensure() = %q with the directory a symbolic link; want an error", tok)
	}
	if _, err := Rotate(home); err == nil {
		t.Fatal("Rotate() through a symbolic link directory succeeded")
	}
	if err := WriteInfo(home, Info{Address: "ws://127.0.0.1:1/acp"}); err == nil {
		t.Fatal("WriteInfo() through a symbolic link directory succeeded")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("files were written through the link: %v", entries)
	}
}

func TestEnsureAndLoadNeverShowAPartialToken(t *testing.T) {
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
				if tok, err := Load(home); err == nil && !Valid(tok) {
					t.Errorf("Load() returned %q, which is not a token", tok)
					return
				}
			}
		}()
	}
	for i := 0; i < 300; i++ {
		if _, err := Rotate(home); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
	entries, _ := os.ReadDir(Dir(home))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left: %s", e.Name())
		}
	}
}

func TestRotateMakesTheOldTokenStopWorking(t *testing.T) {
	home := t.TempDir()
	old, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Rotate(home)
	if err != nil || fresh == old || !Valid(fresh) {
		t.Fatalf("Rotate() = %q, %v; want a token other than %q", fresh, err, old)
	}
	current, err := Load(home)
	if err != nil || !Equal(current, fresh) || Equal(current, old) {
		t.Fatalf("after Rotate the file holds %q (%v); the new token must match and the old one %q must not", current, err, old)
	}
}

func TestTheInfoFileNamesTheAddressAndTheAgentsAndNeverTheToken(t *testing.T) {
	home := t.TempDir()
	tok, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	want := Info{Address: "ws://127.0.0.1:47617/acp", Agents: []string{"claude", "grok"}, Updated: "2026-10-20T03:00:00Z"}
	if err := WriteInfo(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInfo(home)
	if err != nil || got.Address != want.Address || strings.Join(got.Agents, ",") != "claude,grok" || got.Updated != want.Updated {
		t.Fatalf("ReadInfo() = %+v, %v; want %+v", got, err, want)
	}
	raw, err := os.ReadFile(InfoFile(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) || strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Errorf("the info file mentions the token: %s", raw)
	}
	if posix() {
		if mode := modeOf(t, InfoFile(home)); mode != 0o600 {
			t.Errorf("the info file has mode %o, want 600", mode)
		}
	}
}

func TestTheInfoFileIsAlwaysValidJSONForAReader(t *testing.T) {
	home := t.TempDir()
	if err := WriteInfo(home, Info{Address: "ws://127.0.0.1:1/acp"}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := ReadInfo(home); err != nil {
				t.Errorf("ReadInfo() saw a partial file: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if err := WriteInfo(home, Info{Address: "ws://127.0.0.1:2/acp", Agents: []string{"claude"}, Updated: "x"}); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

func TestReadInfoOfNothingOrOfGarbage(t *testing.T) {
	home := t.TempDir()
	if _, err := ReadInfo(home); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadInfo() with no file: %v, want fs.ErrNotExist", err)
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InfoFile(home), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(home); err == nil {
		t.Fatal("ReadInfo() of garbage succeeded")
	}
	if err := RemoveInfo(home); err != nil {
		t.Fatal(err)
	}
	if err := RemoveInfo(home); err != nil {
		t.Fatalf("removing what is not there: %v", err)
	}
	if _, err := os.Stat(InfoFile(home)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the info file is still there: %v", err)
	}
}

func TestAddressMakesTheReadyMadeAddress(t *testing.T) {
	tok := New()
	for _, base := range []string{"ws://127.0.0.1:47615/acp", "ws://127.0.0.1:47615/acp/"} {
		if got, want := Address(base, "claude", tok), "ws://127.0.0.1:47615/acp/claude?token="+tok; got != want {
			t.Errorf("Address(%q) = %q, want %q", base, got, want)
		}
	}
	if strings.ContainsAny(tok, "%+/=&?#") {
		t.Errorf("a token needs escaping in a URL: %q", tok)
	}
}
