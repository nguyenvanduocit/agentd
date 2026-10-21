// Package entry is agentd's command line: `agentd pair`, `agentd run` (the default), `agentd apps`, `agentd acp`,
// `agentd service`, `agentd mcp` and `agentd version`. cmd/agentd runs it as a program of its own, and agentd.app runs it
// when it is started with a command: the agents' MCP wrappers and the user's terminal call the app's executable. The exit codes of `run`
// are its protocol with whatever restarts it: 0 stays stopped, 75 starts again (daemon/home/exit.go).
package entry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/mcpbridge"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/daemon/run"
)

// process is what a command touches of its process: its standard streams and the log made of them.
type process struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	log     *proc.Logger
	tracker *proc.Tracker
}

// Main runs the command the arguments (without the program name) ask for, on this process's standard streams, and
// returns the exit code. A panic stops the process groups the command tracks before it ends the process.
func Main(argv []string) int {
	p := process{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, log: proc.NewLogger(os.Stdout, os.Stderr), tracker: proc.NewTracker()}
	defer p.log.CrashOnPanic(p.tracker)
	return dispatch(argv, p)
}

// commands are the words agentd.app takes for a command, rather than for the system's arguments to an app (a launch
// from the Finder may pass some): the first argument of a command line that is not the app's.
var commands = []string{"run", "pair", "apps", "acp", "service", "mcp", "version", "help", "--version", "-v", "--help", "-h"}

// IsCommand is whether argv is a command line of agentd and not the arguments the system gives an app.
func IsCommand(argv []string) bool { return len(argv) > 0 && slices.Contains(commands, argv[0]) }

// dispatch runs the command the arguments ask for and returns the exit code.
func dispatch(argv []string, p process) int {
	command, err := cli.Parse(argv)
	if err != nil {
		p.log.Warn(err.Error() + "\n\n" + cli.Usage)
		return 2
	}
	switch command.(type) {
	case cli.Help:
		p.log.Say(cli.Usage)
		return home.ExitStopped
	case cli.Version:
		p.log.Say(home.Version)
		return home.ExitStopped
	}
	host, err := home.CurrentHost()
	if err != nil {
		p.log.Warn(err.Error())
		return 1
	}
	code, err := dispatchWith(command, host, p)
	if err != nil {
		p.log.Warn(err.Error())
		return 1
	}
	return code
}

func dispatchWith(command cli.Command, host home.Host, p process) (int, error) {
	switch cmd := command.(type) {
	case cli.Apps:
		return registry.RunApps(cmd, host, p.log)
	case cli.ACP:
		return acpauth.RunACP(cmd, acpauth.Deps{Home: home.AgentdHome(host), Stdout: p.stdout, Stderr: p.stderr}), nil
	case cli.Pair:
		return registry.RunPair(context.Background(), cmd, registry.PairDeps{
			Client: http.DefaultClient, Machine: registry.CurrentMachine(), Host: host, Log: p.log, Stdin: p.stdin, Prompt: p.stderr,
		})
	case cli.MCP:
		return mcpbridge.RunBridge(cmd, p.bridgeDeps(host)), nil
	case cli.PageMCP:
		return mcpbridge.RunPageBridge(p.bridgeDeps(host)), nil
	case cli.Run:
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		// The first signal stops the daemon; a second one, while it shuts down, ends the process as the default does.
		go func() {
			<-ctx.Done()
			stop()
		}()
		return run.Daemon(ctx, cmd, run.Deps{Log: p.log, Host: host, Tracker: p.tracker}), nil
	}
	return 1, fmt.Errorf("unknown command %T", command)
}

func (p process) bridgeDeps(host home.Host) mcpbridge.Deps {
	return mcpbridge.Deps{Stdin: p.stdin, Stdout: p.stdout, Stderr: p.stderr, Host: host, Connect: mcpbridge.Dial}
}
