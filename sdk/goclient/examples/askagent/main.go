// Command askagent connects to the agentd on this computer as the app at -origin, opens an agent, sends it one line and
// prints what comes back until the agent exits. It is the smallest program that goes through every state of goclient
// (see README.md); copy it and replace the line with ACP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

func main() { os.Exit(run()) }

func run() int {
	origin := flag.String("origin", "http://localhost:5173", "the app's origin: https, or http on localhost")
	port := flag.Int("port", 0, "the port of the daemon's gateway (default: look on 47615-47624)")
	agent := flag.String("agent", "claude", "the agent to open")
	line := flag.String("line", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`, "the line to send it")
	wait := flag.Duration("wait", 5*time.Second, "how long to wait for the agent to exit")
	forget := flag.Bool("forget", false, "take this app's grant back at the end")
	flag.Parse()

	var options []goclient.Option
	if *port != 0 {
		options = append(options, goclient.WithPorts(*port))
	}
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: *origin, Mode: goclient.ModeBrowser, Options: options})
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // an origin the gateway would refuse says what to write instead
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	local.Detect(ctx)
	switch state := local.State(); state.Phase {
	case goclient.PhaseAbsent:
		fmt.Fprintln(os.Stderr, "agentd is not here. Start it with:", goclient.AgentdCommands().Start)
		return 1
	case goclient.PhaseRefused:
		fmt.Fprintln(os.Stderr, state.Error)
		return 1
	case goclient.PhaseAvailable:
		fmt.Println("asking to connect: answer the dialog on this computer")
		if !local.Connect(ctx) {
			refused := local.State()
			fmt.Fprintln(os.Stderr, refused.Error) // the sentence for the reason; State().Reason is the code
			if !refused.RetryAt.IsZero() {
				fmt.Fprintln(os.Stderr, "ask again after", refused.RetryAt.Format(time.TimeOnly))
			}
			return 1
		}
	}

	session, err := local.OpenSession(ctx, *agent)
	switch {
	case errors.Is(err, goclient.ErrNotAllowed):
		fmt.Fprintln(os.Stderr, "the user has not allowed this app")
		return 1
	case err != nil:
		fmt.Fprintln(os.Stderr, err) // for an agent the daemon refuses, its own sentence
		return 1
	}
	defer func() { _ = session.Close() }()

	session.OnFrame(func(f goclient.Frame) {
		if f.Kind == goclient.FrameExit {
			fmt.Println("exit: code", f.Code) // the exit code is in Code; Data is empty
			return
		}
		fmt.Printf("%s: %s\n", f.Kind, f.Data)
	})
	if err := session.WriteLine(*line); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ended := make(chan int, 1)
	go func() {
		if code, err := session.Wait(); err == nil {
			ended <- code
		}
	}()
	select {
	case code := <-ended:
		fmt.Println("the agent exited with code", code)
	case <-time.After(*wait):
		fmt.Println("the agent is still running; closing the session")
	}
	if *forget {
		local.Disconnect(ctx)
	}
	return 0
}
