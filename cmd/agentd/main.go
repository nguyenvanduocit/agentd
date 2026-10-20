// Command agentd runs your agents for the apps you allow: `agentd pair`, `agentd run` (the default), `agentd apps`,
// `agentd service`, `agentd mcp` and `agentd version`. The exit codes of `run` are its protocol with whatever restarts
// it: 0 stays stopped, 75 starts again (daemon/home/exit.go).
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/mcpbridge"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
	"github.com/nguyenvanduocit/agentd/daemon/run"
	"github.com/nguyenvanduocit/agentd/daemon/service"
)

// process is what a command touches of its process: its standard streams and the log made of them.
type process struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	log     *proc.Logger
	tracker *proc.Tracker
}

func main() {
	p := process{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, log: proc.NewLogger(os.Stdout, os.Stderr), tracker: proc.NewTracker()}
	defer p.log.CrashOnPanic(p.tracker)
	os.Exit(dispatch(os.Args[1:], p))
}

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
	case cli.Service:
		return service.RunService(cmd, service.NewDeps(host, p.log, p.stdout)), nil
	}
	return 1, fmt.Errorf("unknown command %T", command)
}

func (p process) bridgeDeps(host home.Host) mcpbridge.Deps {
	return mcpbridge.Deps{Stdin: p.stdin, Stdout: p.stdout, Stderr: p.stderr, Host: host, Connect: mcpbridge.Dial}
}
