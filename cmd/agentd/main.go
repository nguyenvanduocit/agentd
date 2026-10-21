// Command agentd runs your agents for the apps you allow: `agentd pair`, `agentd run` (the default), `agentd apps`,
// `agentd acp`, `agentd service`, `agentd mcp` and `agentd version` (daemon/entry). It is the daemon without a window:
// the tests and the checks on Linux run it, and agentd.app carries the same command line.
package main

import (
	"os"

	"github.com/nguyenvanduocit/agentd/daemon/entry"
)

func main() { os.Exit(entry.Main(os.Args[1:])) }
