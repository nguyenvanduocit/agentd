package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// The engine in agentd.app is the version of the app: what `agentd version` and the engine's hello say is mygo.json's.
func TestTheEnginesVersionIsTheAppsVersion(t *testing.T) {
	b, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Version string }
	if err := json.Unmarshal(b, &c); err != nil || c.Version == "" {
		t.Fatalf("mygo.json: %v, version %q", err, c.Version)
	}
	if home.Version != c.Version {
		t.Fatalf("home.Version = %q, want %q", home.Version, c.Version)
	}
}
