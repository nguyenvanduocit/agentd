package home

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindProgramFindsAnExecutableFileOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bits are a Unix notion")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Env{"PATH": dir}
	if got, ok := FindProgram("tool", env); !ok || got != filepath.Join(dir, "tool") {
		t.Errorf("FindProgram(tool) = %q, %v", got, ok)
	}
	for _, name := range []string{"plain", "adir", "missing"} {
		if got, ok := FindProgram(name, env); ok {
			t.Errorf("FindProgram(%s) = %q, want not found", name, got)
		}
	}
}

func TestCurrentHostReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("AGENTD_HOME_TEST_VAR", "a=b")
	h, err := CurrentHost()
	if err != nil {
		t.Fatal(err)
	}
	if h.Platform != runtime.GOOS || h.Home == "" || h.Env["AGENTD_HOME_TEST_VAR"] != "a=b" {
		t.Errorf("CurrentHost = platform %q home %q var %q", h.Platform, h.Home, h.Env["AGENTD_HOME_TEST_VAR"])
	}
}
