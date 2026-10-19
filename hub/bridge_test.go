package hub_test

import (
	"slices"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/hub"
)

// The command an app puts in session/new is the one the daemon puts on each agent's PATH, with the argument its mcp
// subcommand reads: both live in the daemon's packages, so a rename there fails here.
func TestTheBridgeCommandIsTheOneTheDaemonInstalls(t *testing.T) {
	command, args := hub.BridgeCommand("grant-7")
	if command != home.Wrapper {
		t.Fatalf("command = %q, the daemon installs %q", command, home.Wrapper)
	}
	if !slices.Equal(args, []string{"--session", "grant-7"}) {
		t.Fatalf("args = %q, want --session grant-7", args)
	}
	parsed, err := cli.Parse(append([]string{"mcp"}, args...)) // the wrapper runs `agentd mcp <args>`
	if err != nil {
		t.Fatalf("the daemon's mcp command does not take %q: %v", args, err)
	}
	if mcp, ok := parsed.(cli.MCP); !ok || mcp.Session != "grant-7" {
		t.Fatalf("the daemon reads %#v, want the session grant-7", parsed)
	}
}
