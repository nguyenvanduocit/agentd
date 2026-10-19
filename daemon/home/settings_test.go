package home

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestParseSettings(t *testing.T) {
	got, err := ParseSettings(`{"updates":false}`)
	if err != nil || got != (Settings{Updates: false}) {
		t.Errorf("reads what was saved: %+v, %v", got, err)
	}
	for _, text := range []string{"{}", `{"updates":"no"}`, `{"updates":null}`, "[]", "null", "3"} {
		got, err := ParseSettings(text)
		if err != nil || got != DefaultSettings {
			t.Errorf("ParseSettings(%s) = %+v, %v; want the defaults", text, got, err)
		}
	}
	if _, err := ParseSettings("{"); err == nil {
		t.Error("text that is not JSON must be an error")
	}
	if !DefaultSettings.Updates {
		t.Error("updates are on by default")
	}
}

func TestSettingsFileSitsBesideTheRegistryFile(t *testing.T) {
	got := SettingsFile(filepath.Join("/c", "agentd", "daemon.json"))
	if want := filepath.Join("/c", "agentd", "settings.json"); got != want {
		t.Errorf("SettingsFile = %q, want %q", got, want)
	}
}

func readSaved(t *testing.T, file string) Settings {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(text, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func boolPatch(b bool) SettingsPatch { return SettingsPatch{Updates: &b} }

func TestSettingsStore(t *testing.T) {
	t.Run("a missing file is the defaults, and nothing is written until a change", func(t *testing.T) {
		dir := t.TempDir()
		s := OpenSettings(filepath.Join(dir, "agentd", "settings.json"), nil)
		if s.Get() != DefaultSettings {
			t.Errorf("Get = %+v", s.Get())
		}
		if got := listDir(t, dir); len(got) != 0 {
			t.Errorf("dir = %v, want empty", got)
		}
	})

	t.Run("a file that is not JSON is the defaults, and the log says so", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "agentd", "settings.json")
		if err := os.Mkdir(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		var warned []string
		s := OpenSettings(file, func(m string) { warned = append(warned, m) })
		if s.Get() != DefaultSettings {
			t.Errorf("Get = %+v", s.Get())
		}
		if len(warned) != 1 {
			t.Errorf("warnings = %v, want one", warned)
		}
	})

	t.Run("a change is saved, whole, and read back by the next daemon", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "agentd", "settings.json")
		s := OpenSettings(file, nil)
		got, err := s.Change(boolPatch(false))
		if err != nil || got != (Settings{Updates: false}) {
			t.Fatalf("Change = %+v, %v", got, err)
		}
		if s.Get() != (Settings{Updates: false}) {
			t.Errorf("Get = %+v", s.Get())
		}
		if saved := readSaved(t, file); saved != (Settings{Updates: false}) {
			t.Errorf("saved = %+v", saved)
		}
		if got := listDir(t, filepath.Dir(file)); !reflect.DeepEqual(got, []string{"settings.json"}) {
			t.Errorf("dir = %v, want only settings.json", got)
		}
		if OpenSettings(file, nil).Get() != (Settings{Updates: false}) {
			t.Error("the next daemon must read the change")
		}
	})

	t.Run("the file is the bytes JSON.stringify(settings, null, 2) writes, 0600 in a 0700 directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "agentd", "settings.json")
		if _, err := OpenSettings(file, nil).Change(boolPatch(false)); err != nil {
			t.Fatal(err)
		}
		text, _ := os.ReadFile(file)
		if string(text) != "{\n  \"updates\": false\n}\n" {
			t.Errorf("file = %q", text)
		}
		if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(file)); fi.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", fi.Mode().Perm())
		}
	})

	t.Run("changes are saved in the order they were made", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "agentd", "settings.json")
		s := OpenSettings(file, nil)
		for _, b := range []bool{false, true, false} {
			if _, err := s.Change(boolPatch(b)); err != nil {
				t.Fatal(err)
			}
		}
		if s.Get() != (Settings{Updates: false}) || readSaved(t, file) != (Settings{Updates: false}) {
			t.Errorf("memory %+v, file %+v: both must hold the last change", s.Get(), readSaved(t, file))
		}
	})

	t.Run("concurrent changes leave the file equal to the settings in memory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "agentd", "settings.json")
		s := OpenSettings(file, nil)
		var wg sync.WaitGroup
		for _, b := range []bool{false, true, false, true, false, true} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.Change(boolPatch(b)); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if readSaved(t, file) != s.Get() {
			t.Errorf("file %+v differs from memory %+v", readSaved(t, file), s.Get())
		}
		if got := listDir(t, filepath.Dir(file)); !reflect.DeepEqual(got, []string{"settings.json"}) {
			t.Errorf("dir = %v, want only settings.json", got)
		}
	})

	t.Run("an empty patch keeps the settings", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "agentd", "settings.json")
		s := OpenSettings(file, nil)
		if got, err := s.Change(SettingsPatch{}); err != nil || got != DefaultSettings {
			t.Errorf("Change = %+v, %v", got, err)
		}
	})

	t.Run("a change that cannot be saved is refused and leaves the settings as they were", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "agentd"), []byte("a file where the directory should be"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := OpenSettings(filepath.Join(dir, "agentd", "settings.json"), nil)
		if _, err := s.Change(boolPatch(false)); err == nil {
			t.Fatal("the save must fail")
		}
		_, _ = s.Change(boolPatch(true))
		if s.Get() != DefaultSettings {
			t.Errorf("Get = %+v, want the defaults", s.Get())
		}
	})
}
