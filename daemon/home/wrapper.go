package home

import (
	"os"
	"path/filepath"
	"strings"
)

// Wrapper is the name the agent finds the MCP wrapper by on its PATH.
const Wrapper = "agentd-mcp"

// WrapperTarget is what the wrapper runs.
type WrapperTarget struct {
	// Exe is the agentd binary the wrapper runs.
	Exe string
	// Args are options placed before the caller's own arguments, e.g. ["--config", file].
	Args []string
}

// WrapperFileName is the wrapper's file name: agentd-mcp, with .cmd on Windows.
func WrapperFileName(platform string) string {
	if platform == windows {
		return Wrapper + ".cmd"
	}
	return Wrapper
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func cmdQuote(s string) string {
	return `"` + strings.ReplaceAll(s, "%", "%%") + `"`
}

func quoteAll(words []string, quote func(string) string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quote(w)
	}
	return strings.Join(quoted, " ")
}

// WrapperScript is the wrapper the agent finds as `agentd-mcp` on its PATH: it runs `agentd mcp <args> "$@"`.
// The server names the command without a path, and a wrapper (not a symlink) keeps the daemon's own binary and the
// config location out of the server's hands.
func WrapperScript(platform string, t WrapperTarget) string {
	words := append([]string{t.Exe, "mcp"}, t.Args...)
	if platform == windows {
		return "@echo off\r\n" + quoteAll(words, cmdQuote) + " %*\r\n"
	}
	return "#!/bin/sh\nexec " + quoteAll(words, shQuote) + ` "$@"` + "\n"
}

// AgentEnv is the environment of an agent: a copy of `base` with `binDir` first on PATH.
func AgentEnv(base Env, binDir, platform string) Env {
	key := PathKey(base, platform)
	env := make(Env, len(base)+1)
	for k, v := range base {
		env[k] = v
	}
	if old := base[key]; old != "" {
		env[key] = binDir + pathDelimiter(platform) + old
	} else {
		env[key] = binDir
	}
	return env
}

// InstallWrapper writes the wrapper for t as dir/agentd-mcp, executable, and returns dir.
func InstallWrapper(dir, platform string, t WrapperTarget) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the directory holds a script agents run
		return "", err
	}
	file := filepath.Join(dir, WrapperFileName(platform))
	if err := os.WriteFile(file, []byte(WrapperScript(platform, t)), 0o755); err != nil { //nolint:gosec // the wrapper is meant to be run
		return "", err
	}
	if err := os.Chmod(file, 0o755); err != nil { //nolint:gosec // WriteFile leaves an existing file's mode alone
		return "", err
	}
	return dir, nil
}
