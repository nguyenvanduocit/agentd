package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// SettingsPatch is a change to Settings: a nil field is left as it is.
type SettingsPatch struct {
	Updates *bool
}

// SettingsStore is the settings of this daemon: read once at open (a missing or broken file is the defaults, which a
// broken one says through warn) and saved at every change, one change at a time, so the file ends with the last one made.
type SettingsStore struct {
	mu      sync.Mutex
	file    string
	current Settings
}

// OpenSettings reads the settings file. warn receives one line when the file cannot be read or is not JSON; it may be nil.
func OpenSettings(file string, warn func(string)) *SettingsStore {
	return &SettingsStore{file: file, current: loadSettings(file, warn)}
}

func loadSettings(file string, warn func(string)) Settings {
	say := func(format string, a ...any) {
		if warn != nil {
			warn(fmt.Sprintf(format, a...))
		}
	}
	text, err := os.ReadFile(file)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			say("cannot read %s: %v; using the default settings", file, err)
		}
		return DefaultSettings
	}
	s, err := ParseSettings(string(text))
	if err != nil {
		say("%s is not JSON (%v); using the default settings", file, err)
		return DefaultSettings
	}
	return s
}

// Get is the settings now.
func (s *SettingsStore) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Change saves the settings with `patch` applied, after every earlier change; they hold in memory once saved.
// A failed save returns the error and changes nothing.
func (s *SettingsStore) Change(patch SettingsPatch) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.current
	if patch.Updates != nil {
		next.Updates = *patch.Updates
	}
	if err := saveSettings(s.file, next); err != nil {
		return Settings{}, err
	}
	s.current = next
	return next, nil
}

// saveSettings writes through a temporary file and a rename, as the registry is: a reader never sees half of it.
func saveSettings(file string, settings Settings) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	text, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(file)+".*.tmp") // mode 0600
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(text, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), file)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}
