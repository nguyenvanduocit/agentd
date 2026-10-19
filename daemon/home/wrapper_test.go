package home

import (
	"strings"
	"testing"
)

func TestWrapperScript(t *testing.T) {
	t.Run("POSIX: an sh script that execs agentd with the baked arguments", func(t *testing.T) {
		got := WrapperScript("linux", WrapperTarget{Exe: "/usr/bin/agentd", Args: []string{"--config", "/c/daemon.json"}})
		want := "#!/bin/sh\nexec '/usr/bin/agentd' 'mcp' '--config' '/c/daemon.json' \"$@\"\n"
		if got != want {
			t.Errorf("script = %q, want %q", got, want)
		}
	})
	t.Run("POSIX: a quote in a path cannot end the quoting", func(t *testing.T) {
		got := WrapperScript("linux", WrapperTarget{Exe: "/it's/agentd"})
		if !strings.Contains(got, `'/it'\''s/agentd'`) {
			t.Errorf("script = %q", got)
		}
	})
	t.Run("POSIX: the script keeps shell metacharacters of a path inert", func(t *testing.T) {
		got := WrapperScript("linux", WrapperTarget{Exe: "/a b/$(touch x)/agentd"})
		if !strings.Contains(got, `'/a b/$(touch x)/agentd'`) {
			t.Errorf("script = %q", got)
		}
	})
	t.Run("Windows: a .cmd that forwards its arguments", func(t *testing.T) {
		got := WrapperScript("windows", WrapperTarget{Exe: `C:\n\agentd.exe`, Args: []string{"--config", `C:\c\100%.json`}})
		want := "@echo off\r\n\"C:\\n\\agentd.exe\" \"mcp\" \"--config\" \"C:\\c\\100%%.json\" %*\r\n"
		if got != want {
			t.Errorf("script = %q, want %q", got, want)
		}
	})
}

func TestWrapperFileName(t *testing.T) {
	for platform, want := range map[string]string{"linux": "agentd-mcp", "darwin": "agentd-mcp", "windows": "agentd-mcp.cmd"} {
		if got := WrapperFileName(platform); got != want {
			t.Errorf("WrapperFileName(%s) = %q, want %q", platform, got, want)
		}
	}
}

func TestAgentEnv(t *testing.T) {
	t.Run("puts the wrapper directory first on PATH and keeps the rest", func(t *testing.T) {
		env := AgentEnv(Env{"PATH": "/usr/bin:/bin", "HOME": "/h"}, "/tmp/bin", "linux")
		if env["PATH"] != "/tmp/bin:/usr/bin:/bin" || env["HOME"] != "/h" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("an empty PATH is just the wrapper directory", func(t *testing.T) {
		if got := AgentEnv(Env{}, "/tmp/bin", "linux")["PATH"]; got != "/tmp/bin" {
			t.Errorf("PATH = %q", got)
		}
	})
	t.Run("does not change the environment it was given", func(t *testing.T) {
		base := Env{"PATH": "/usr/bin"}
		AgentEnv(base, "/tmp/bin", "linux")
		if base["PATH"] != "/usr/bin" || len(base) != 1 {
			t.Errorf("base = %v", base)
		}
	})
	t.Run("Windows: keeps the variable's own spelling and the ; delimiter", func(t *testing.T) {
		env := AgentEnv(Env{"Path": `C:\Windows`}, `C:\bin`, "windows")
		if _, has := env["PATH"]; has || env["Path"] != `C:\bin;C:\Windows` {
			t.Errorf("env = %v", env)
		}
	})
}
