package proc

import (
	"strconv"
	"strings"
	"unicode"
)

// SpawnPlan is how to start a program on a platform.
type SpawnPlan struct {
	File string
	Args []string
	// Shell: the program runs through the shell, which needs File and Args quoted.
	Shell bool
	// Detached (POSIX): the agent leads its own process group, so one signal ends it and everything it started.
	Detached bool
}

func winQuote(s string) string {
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`"&|<>^()`, r) }) >= 0 {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// PlanSpawn is how to start file on goos (a runtime.GOOS value). Windows runs .cmd and .bat (npx.cmd, the wrapper)
// through the shell, which then needs its arguments quoted; the agent's tree is ended with taskkill, so it is not
// detached there.
func PlanSpawn(file string, args []string, goos string) SpawnPlan {
	plain := make([]string, len(args))
	copy(plain, args)
	if goos != "windows" {
		return SpawnPlan{File: file, Args: plain, Detached: true}
	}
	lower := strings.ToLower(file)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = winQuote(a)
		}
		return SpawnPlan{File: winQuote(file), Args: quoted, Shell: true}
	}
	return SpawnPlan{File: file, Args: plain}
}

// TaskkillArgs is the command that ends the process tree rooted at pid on Windows (POSIX uses signals on the group).
func TaskkillArgs(pid int) []string {
	return []string{"/pid", strconv.Itoa(pid), "/T", "/F"}
}
