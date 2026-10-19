package home

import (
	"encoding/json"
	"path/filepath"
)

// Settings is what the user chose in the tray's settings window.
type Settings struct {
	// Updates installs the versions the release server offers by itself.
	Updates bool `json:"updates"`
}

// DefaultSettings are the settings of a daemon whose settings file is missing or broken.
var DefaultSettings = Settings{Updates: true}

// SettingsFile is settings.json beside the registry file: `--config` moves both, so a test daemon never reads the user's.
func SettingsFile(registryFile string) string {
	return filepath.Join(filepath.Dir(registryFile), "settings.json")
}

// ParseSettings is the settings saved in `text`; one that is missing or not of its type keeps its default.
// Text that is not JSON is an error.
func ParseSettings(text string) (Settings, error) {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return Settings{}, err
	}
	s := DefaultSettings
	if obj, ok := v.(map[string]any); ok {
		if b, ok := obj["updates"].(bool); ok {
			s.Updates = b
		}
	}
	return s, nil
}
