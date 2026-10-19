package hub_test

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/nguyenvanduocit/agentd/hub"
)

// The command an app puts in session/new is the one the daemon puts on each agent's PATH, with the argument its mcp
// subcommand reads: both live in the daemon's sources, so a rename there fails here.
func TestTheBridgeCommandIsTheOneTheDaemonInstalls(t *testing.T) {
	command, args := hub.BridgeCommand("grant-7")
	if want := constIn(t, "../daemon/src/core/wrapper.ts", `export const WRAPPER = "([^"]+)"`); command != want {
		t.Fatalf("command = %q, the daemon installs %q", command, want)
	}
	if !slices.Equal(args, []string{"--session", "grant-7"}) {
		t.Fatalf("args = %q, want --session grant-7", args)
	}
	constIn(t, "../daemon/src/core/cli.ts", `(session): \{ type: "string" \}`)
}

func constIn(t *testing.T, file, pattern string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(pattern).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s has no match for %s", file, pattern)
	}
	return string(m[1])
}
