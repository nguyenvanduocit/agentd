// Package home is the daemon's view of its machine: where its files live, how to find a program on PATH, the exit-code
// protocol with agentd.app and the server address rules. Everything except CurrentHost/FindProgram is pure: the
// environment, the platform and the file probe are passed in.
package home

import "path"

// Env is an environment: variable name to value. A variable that is not set is absent from the map.
type Env map[string]string

// Host is the machine a path is computed for. Platform is a runtime.GOOS value ("linux", "darwin", "windows").
type Host struct {
	Platform string
	Env      Env
	Home     string
}

const windows = "windows"

// join joins with the path rules of the host's platform.
func join(h Host, parts ...string) string {
	if h.Platform == windows {
		return winJoin(parts...)
	}
	return path.Join(parts...)
}

// nonEmpty returns an environment value that is set and not empty.
func (e Env) nonEmpty(name string) (string, bool) {
	v := e[name]
	return v, v != ""
}

// configRoot is $XDG_CONFIG_HOME or ~/.config on Linux and macOS, %APPDATA% on Windows.
func configRoot(h Host) string {
	if h.Platform == windows {
		if v, ok := h.Env["APPDATA"]; ok {
			return v
		}
		return join(h, h.Home, "AppData", "Roaming")
	}
	if v, ok := h.Env.nonEmpty("XDG_CONFIG_HOME"); ok {
		return v
	}
	return join(h, h.Home, ".config")
}

// stateRoot is where daemon state that is not configuration lives: $XDG_STATE_HOME or ~/.local/state, %LOCALAPPDATA% on Windows.
func stateRoot(h Host) string {
	if h.Platform == windows {
		if v, ok := h.Env["LOCALAPPDATA"]; ok {
			return v
		}
		return join(h, h.Home, "AppData", "Local")
	}
	if v, ok := h.Env.nonEmpty("XDG_STATE_HOME"); ok {
		return v
	}
	return join(h, h.Home, ".local", "state")
}

// ConfigFile is the path of the registry file (agentd/daemon.json under the config root).
func ConfigFile(h Host) string {
	return join(h, configRoot(h), "agentd", "daemon.json")
}

// SessionsFile is the record of the process groups of running agents, read by the next start to end what a killed daemon left.
func SessionsFile(h Host) string {
	return join(h, stateRoot(h), "agentd", "sessions.json")
}

// AgentdHome is the daemon's own home: $AGENTD_HOME (a test seam, and for people who keep tools elsewhere) or ~/.agentd.
func AgentdHome(h Host) string {
	if v, ok := h.Env.nonEmpty("AGENTD_HOME"); ok {
		return v
	}
	return join(h, h.Home, ".agentd")
}
