package home

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
)

// CurrentHost is the machine this process runs on.
func CurrentHost() (Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		u, uerr := user.Current()
		if uerr != nil || u.HomeDir == "" {
			return Host{}, fmt.Errorf("cannot find the home directory: %w", err)
		}
		home = u.HomeDir
	}
	return Host{Platform: runtime.GOOS, Env: currentEnv(), Home: home}, nil
}

// currentEnv is the process environment as an Env. Windows keeps entries such as "=C:=C:\" whose name starts with "="; they split after it.
func currentEnv() Env {
	env := Env{}
	for _, kv := range os.Environ() {
		if i := strings.Index(kv[1:], "="); i >= 0 {
			env[kv[:i+1]] = kv[i+2:]
		}
	}
	return env
}

// FindProgram finds a program on the PATH of `env`, as a shell would.
func FindProgram(name string, env Env) (string, bool) {
	return ResolveExecutable(name, env, runtime.GOOS, isExecutableFile)
}

func isExecutableFile(file string) bool {
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return canExecute(file)
}
