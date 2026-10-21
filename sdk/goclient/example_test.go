package goclient_test

// The examples of README.md, compiled with the package: they are not run (they need a daemon on this computer).
// readme_test.go checks that every line of a Go block of the README is a line of this file or of
// examples/askagent/main.go, so a README that names something that is gone does not pass.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// pairingServer is the app's own server: it mints the one-time code the daemon pairs with.
type pairingServer struct{}

func (pairingServer) StartDaemonPairing(context.Context) (string, error) { return "K7QX-29PD", nil }
func (pairingServer) CancelDaemonPairing()                               {}

func Example_serverMode() {
	ctx := context.Background()
	const origin = "https://app.example.com" // your server's origin: the daemon pairs with the app there
	var myServer pairingServer

	// 1. The daemon on this computer: is it there, and can the user connect it to this app?
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
		Name:          "My app",
		Origin:        origin,
		StartPairing:  myServer.StartDaemonPairing, // the app's own call: a one-time code
		CancelPairing: myServer.CancelDaemonPairing,
	})
	if err != nil {
		log.Fatal(err)
	}
	stop := local.Subscribe(func(s goclient.LocalState) { fmt.Println(s.Phase, s.Error) })
	defer stop()
	local.Detect(ctx) // when the settings screen opens, and when the app gets focus
	if local.State().CanConnect {
		local.Connect(ctx) // the dialog opens on the user's computer
	}

	// 2. No daemon here: agentd.app for the user's Mac. Once it is open, Detect finds it and Connect pairs it.
	if downloads, ok := goclient.DesktopDownloadsFrom(""); ok { // "": agentd's own releases page
		fmt.Println(downloads.AppleSilicon, downloads.Intel)
	}
}

// acpApp is the ACP client of the agent: it shows what the agent says and answers its permission requests. The agent asks
// for no file or terminal of this client, so the embedded acp.Client is never called.
type acpApp struct{ acp.Client }

func (acpApp) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

func (acpApp) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if m := n.Update.AgentMessageChunk; m != nil && m.Content.Text != nil {
		fmt.Print(m.Content.Text.Text)
	}
	return nil
}

func Example_browserMode() {
	ctx := context.Background()
	const origin = "http://localhost:5173" // https, or http on a loopback host: what the gateway accepts

	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "My app", Origin: origin, Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err) // an origin the gateway would refuse is an *OriginError that says what to write instead
	}
	local.Detect(ctx)
	if local.State().CanConnect { // a daemon is here and this origin has no grant yet
		local.Connect(ctx) // the dialog opens on the user's computer
	}

	session, err := local.OpenSession(ctx, "claude") // local.State().Found.Agents lists them: Name, Title, Ready, Reason
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	conn := acp.NewClientSideConnection(acpApp{}, session.Stdin(), session.Stdout())
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		log.Fatal(err)
	}
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: session.Cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Hello")}}); err != nil {
		log.Fatal(err)
	}

	local.Disconnect(ctx) // takes this origin's grant back; open sessions end
}

func Example_phases() {
	ctx := context.Background()
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "My app", Origin: "http://localhost:5173", Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err)
	}
	render := func(s goclient.LocalState) {
		switch s.Phase {
		case goclient.PhaseDetecting:
			fmt.Println("Looking for agentd on this computer...")
		case goclient.PhaseAbsent:
			// Not installed, not running, or something between blocked the call: the program cannot tell which.
			fmt.Println("agentd is not here. Open agentd.app.")
		case goclient.PhaseAvailable:
			fmt.Println("agentd is here. Connect asks the user to allow this app.") // s.CanConnect
		case goclient.PhaseConnecting:
			fmt.Println("Answer the dialog on this computer.")
		case goclient.PhaseConnected:
			fmt.Println("Connected:", len(s.Found.Agents), "agents")
		case goclient.PhaseRefused:
			// s.Error is a sentence for a person; s.Reason is the code; s.RetryAt is when to ask again, if the daemon says.
			fmt.Println(s.Error)
		}
	}
	stopListening := local.Subscribe(render)
	defer stopListening()
	stopWatching := local.Watch(ctx, 3*time.Second) // looks now and every 3 s: an install or a restart shows up by itself
	defer stopWatching()
}

func Example_errors() {
	ctx := context.Background()
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "My app", Origin: "http://localhost:5173", Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err)
	}
	session, err := local.OpenSession(ctx, "claude")
	var refused *goclient.SessionError
	switch {
	case errors.Is(err, goclient.ErrNoDaemon):
		fmt.Println("agentd is not running on this computer")
	case errors.Is(err, goclient.ErrNotAllowed):
		fmt.Println("the user has not allowed this app: ask with local.Connect(ctx)")
	case errors.As(err, &refused):
		fmt.Println(refused.Message) // the daemon's own sentence: the agent is unknown, still setting up, or not installed
	case err != nil:
		log.Fatal(err)
	default:
		defer func() { _ = session.Close() }()
	}
}

func Example_agentEnded() {
	ctx := context.Background()
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "My app", Origin: "http://localhost:5173", Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err)
	}
	session, err := local.OpenSession(ctx, "claude")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	session.OnFrame(func(f goclient.Frame) { // out: a stdout line, err: stderr text, exit: the agent ended
		fmt.Println(f.Kind, f.Data)
	})
	if err := session.WriteLine(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`); err != nil {
		log.Fatal(err)
	}
	code, err := session.Wait() // blocks until the agent exits; an error if the socket ended first
	fmt.Println("the agent exited with", code, err)
}

func Example_cancelConnect() {
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Name: "My app", Origin: "http://localhost:5173", Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err)
	}
	local.Detect(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !local.Connect(ctx) && local.State().Reason == goclient.ReasonCancelled {
		// The dialog may still be open on the user's computer. If they allow it there, the next Detect finds the grant.
		fmt.Println(local.State().Error)
	}
}

func Example_testWithoutClicking() {
	// A test daemon on port 47661 (AGENTD_LOCAL_PORT=47661, see "Test without clicking" in the README).
	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
		Name:    "My app",
		Origin:  "http://localhost:5173",
		Mode:    goclient.ModeBrowser,
		Options: []goclient.Option{goclient.WithPorts(47661)},
	})
	if err != nil {
		log.Fatal(err)
	}
	local.Detect(context.Background())
}

func Example_toolsFromTheApp() {
	ctx := context.Background()
	const origin = "http://localhost:5173"
	session, err := goclient.OpenSession(ctx, goclient.Daemon{Port: 47615}, origin, "claude")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	// The app's own MCP server, served over the session before session/new lists it.
	tools := mcp.NewServer(&mcp.Implementation{Name: "page", Version: "1"}, nil)
	mcp.AddTool(tools, &mcp.Tool{Name: "current_page", Description: "The page the user is on"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "/projects/42"}}}, nil, nil
		})
	stream := session.Mcp()
	go func() { _ = tools.Run(ctx, &mcp.IOTransport{Reader: stream, Writer: stream}) }()

	// The stdio server the agent starts to reach it, as session/new takes it.
	server, err := session.McpServer("page") // fails on a daemon older than 0.4
	if err != nil {
		log.Fatal(err)
	}
	env := make([]acp.EnvVariable, len(server.Env))
	for i, e := range server.Env {
		env[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
	}
	mcpServers := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: server.Name, Command: server.Command, Args: server.Args, Env: env}}}

	conn := acp.NewClientSideConnection(acpApp{}, session.Stdin(), session.Stdout())
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		log.Fatal(err)
	}
	if _, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: session.Cwd, McpServers: mcpServers}); err != nil {
		log.Fatal(err)
	}
}
