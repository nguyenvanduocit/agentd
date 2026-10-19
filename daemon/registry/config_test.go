package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func appRegistry(token string) Registry {
	return Empty().AddApp(App{Server: "https://s", Token: token})
}

func TestSaveWritesMode0600InADirectoryOf0700AlsoOverALooserFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX modes")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a", "b", "daemon.json")
	if err := Save(file, appRegistry("t")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, file); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	if got := mode(t, filepath.Join(dir, "a", "b")); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(file, appRegistry("t2")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, file); got != 0o600 {
		t.Errorf("file mode after a second save = %o, want 600", got)
	}
	wantEqual(t, names(t, filepath.Join(dir, "a", "b")), []string{"daemon.json"})
}

func TestSaveLeavesAFileTheNodeDaemonCanRead(t *testing.T) {
	file := filepath.Join(t.TempDir(), "daemon.json")
	if err := Save(file, appRegistry("t")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"apps\": [\n    {\n      \"server\": \"https://s\",\n      \"token\": \"t\"\n    }\n  ],\n  \"browsers\": []\n}\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestSaveFailureLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "daemon.json")
	if err := os.Mkdir(file, 0o700); err != nil { // a directory in the way: the rename fails
		t.Fatal(err)
	}
	if err := Save(file, appRegistry("t")); err == nil {
		t.Fatal("Save over a directory succeeded")
	}
	wantEqual(t, names(t, dir), []string{"daemon.json"})
}

func TestFileIsConfigOrTheDefaultLocation(t *testing.T) {
	dir := t.TempDir()
	h := home.Host{Platform: "linux", Env: home.Env{"XDG_CONFIG_HOME": dir}, Home: "/nowhere"}
	if got := File(ptr("/c.json"), h); got != "/c.json" {
		t.Errorf("File(--config) = %q", got)
	}
	if got, want := File(nil, h), dir+"/agentd/daemon.json"; got != want {
		t.Errorf("File(nil) = %q, want %q", got, want)
	}
}

func TestLoadOfAMissingFileIsAnEmptyRegistry(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, got, Empty())
}

func TestLoadOfABrokenOrIncompleteFileNamesItselfAndSaysHowToPair(t *testing.T) {
	f := filepath.Join(t.TempDir(), "c.json")
	for _, tc := range []struct{ text, want string }{
		{"{oops", f + " is not valid JSON"},
		{`{"server":"https://s"}`, f + " has no apps list"},
	} {
		if err := os.WriteFile(f, []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(f); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "agentd pair --app") {
			t.Errorf("Load(%q) error = %v, want it to contain %q", tc.text, err, tc.want)
		}
	}
}

func TestLoadOfAnUnreadableFileFailsInsteadOfReadingAsEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil { // a directory cannot be read as a file
		t.Error("Load of a directory = nil error, want one: an empty registry would be saved over it")
	}
}

func TestUpdateChangesMadeAtTheSameTimeAllLand(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "q", "daemon.json")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Update(file, func(r Registry) Registry {
				return r.AddApp(App{Server: fmt.Sprintf("https://app%d.example", i), Token: fmt.Sprintf("t%d", i)})
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Apps) != 20 {
		t.Fatalf("saved %d apps, want 20", len(saved.Apps))
	}
	for i := range 20 {
		if !saved.HasApp(fmt.Sprintf("https://app%d.example", i)) {
			t.Errorf("app%d is lost", i)
		}
	}
	wantEqual(t, names(t, filepath.Join(dir, "q")), []string{"daemon.json"})
}

func TestUpdateSeesThePreviousChangeAndSavesItsAnswer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "r.json")
	if err := Save(file, appRegistry("t1")); err != nil {
		t.Fatal(err)
	}
	seen, err := Update(file, func(r Registry) Registry {
		return Registry{Apps: r.Apps, Browsers: []BrowserGrant{{Origin: "https://p.example", GrantedAt: "t"}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Apps) != 1 {
		t.Errorf("Update returned %d apps, want 1", len(seen.Apps))
	}
	saved, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Browsers, []BrowserGrant{{Origin: "https://p.example", GrantedAt: "t"}})
}

func TestUpdateOfABrokenFileFailsAndLeavesItAlone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(file, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(file, func(r Registry) Registry { return r }); err == nil {
		t.Fatal("Update of a broken file succeeded")
	}
	if got, _ := os.ReadFile(file); string(got) != "{oops" {
		t.Errorf("file = %q, want it untouched", got)
	}
}
