package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// pathMark brackets PATH in what the login shell prints: its startup files may print anything around it.
const pathMark = "_AGENTD_PATH_"

// PathFromShell reads the PATH a login shell printed between two marks; false when it printed none.
func PathFromShell(out string) (string, bool) {
	i := strings.Index(out, pathMark)
	j := strings.LastIndex(out, pathMark)
	if i < 0 || j <= i {
		return "", false
	}
	p := out[i+len(pathMark) : j]
	return p, p != ""
}

// EnginePath is the PATH of the engine and its agents: the Node.js the app carries first (the adapters run on it and
// install with its npm), then the user's.
func EnginePath(nodeBin, user string) string {
	return nodeBin + string(os.PathListSeparator) + user
}

// LoginPath is the PATH of the user's login shell. An app opened from the Finder gets a bare one (/usr/bin:/bin),
// without where the user's agents and tools are; the shell's startup files say where. Without a shell that answers in
// time, it is the app's own PATH.
func LoginPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-ilc", `printf '`+pathMark+`%s`+pathMark+`' "$PATH"`).Output()
	if p, ok := PathFromShell(string(out)); err == nil && ok {
		return p
	}
	return os.Getenv("PATH")
}
