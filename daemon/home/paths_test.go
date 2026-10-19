package home

import "testing"

func linux(env Env) Host       { return Host{Platform: "linux", Env: env, Home: "/home/ann"} }
func windowsHost(env Env) Host { return Host{Platform: "windows", Env: env, Home: `C:\Users\ann`} }

func TestConfigFile(t *testing.T) {
	darwin := linux(nil)
	darwin.Platform = "darwin"
	for _, tc := range []struct {
		name string
		host Host
		want string
	}{
		{"Linux: ~/.config by default", linux(nil), "/home/ann/.config/agentd/daemon.json"},
		{"macOS: ~/.config by default", darwin, "/home/ann/.config/agentd/daemon.json"},
		{"$XDG_CONFIG_HOME wins", linux(Env{"XDG_CONFIG_HOME": "/x/cfg"}), "/x/cfg/agentd/daemon.json"},
		{"an empty $XDG_CONFIG_HOME does not count", linux(Env{"XDG_CONFIG_HOME": ""}), "/home/ann/.config/agentd/daemon.json"},
		{"Windows: %APPDATA%", windowsHost(Env{"APPDATA": `C:\Users\ann\AppData\Roaming`}), `C:\Users\ann\AppData\Roaming\agentd\daemon.json`},
		{"Windows: default under the home", windowsHost(nil), `C:\Users\ann\AppData\Roaming\agentd\daemon.json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfigFile(tc.host); got != tc.want {
				t.Errorf("ConfigFile = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionsFileLivesUnderTheStateDirectory(t *testing.T) {
	for _, tc := range []struct {
		host Host
		want string
	}{
		{linux(nil), "/home/ann/.local/state/agentd/sessions.json"},
		{linux(Env{"XDG_STATE_HOME": "/x/state"}), "/x/state/agentd/sessions.json"},
		{linux(Env{"XDG_STATE_HOME": ""}), "/home/ann/.local/state/agentd/sessions.json"},
		{windowsHost(Env{"LOCALAPPDATA": `C:\L`}), `C:\L\agentd\sessions.json`},
		{windowsHost(nil), `C:\Users\ann\AppData\Local\agentd\sessions.json`},
	} {
		if got := SessionsFile(tc.host); got != tc.want {
			t.Errorf("SessionsFile(%+v) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestWinJoin(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		want  string
	}{
		{[]string{`C:\a`, "b", "c.txt"}, `C:\a\b\c.txt`},
		{[]string{`C:/a//b`, `..\c`}, `C:\a\c`},
		{[]string{`C:\a`, ".", "b"}, `C:\a\b`},
		{[]string{`C:\`, "a"}, `C:\a`},
		{[]string{`C:\a\..\..`, "x"}, `C:\x`},
		{[]string{`\\srv\share`, "d", "f"}, `\\srv\share\d\f`},
		{[]string{"a", "", "b"}, `a\b`},
		{[]string{"..", "a"}, `..\a`},
		{[]string{"", ""}, "."},
		{[]string{`C:\a\`, "b\\"}, `C:\a\b\`},
	} {
		if got := winJoin(tc.parts...); got != tc.want {
			t.Errorf("winJoin(%q) = %q, want %q", tc.parts, got, tc.want)
		}
	}
}
