package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheCLIServiceIsLaunchdsAgentdJobUnlessATestNamesAnother(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	label, plist := CLIService("/Users/ann", env(nil))
	if label != "com.agentd.daemon" || plist != "/Users/ann/Library/LaunchAgents/com.agentd.daemon.plist" {
		t.Fatalf("got %q %q", label, plist)
	}
	label, plist = CLIService("/Users/ann", env(map[string]string{"AGENTD_SERVICE_LABEL": "com.agentd.daemon.test.1", "AGENTD_SERVICE_DIR": "/tmp/p"}))
	if label != "com.agentd.daemon.test.1" || plist != "/tmp/p/com.agentd.daemon.test.1.plist" {
		t.Fatalf("got %q %q", label, plist)
	}
}

func TestTakingOverRemovesTheCLIServicesPlistAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTD_SERVICE_LABEL", "com.agentd.daemon.test.takeover")
	t.Setenv("AGENTD_SERVICE_DIR", dir)
	if took, err := TakeOverCLIService("/nowhere"); took || err != nil {
		t.Fatalf("without a plist: %v, %v", took, err)
	}
	plist := filepath.Join(dir, "com.agentd.daemon.test.takeover.plist")
	other := filepath.Join(dir, "com.example.other.plist")
	for _, f := range []string{plist, other} {
		if err := os.WriteFile(f, []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if took, err := TakeOverCLIService("/nowhere"); !took || err != nil {
		t.Fatalf("with a plist: %v, %v", took, err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatal("the service's plist is still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another plist went too")
	}
}
