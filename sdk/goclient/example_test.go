package goclient_test

// The examples of README.md, compiled with the package: they are not run (they need a daemon on this computer).

import (
	"context"
	"fmt"
	"log"

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
		Origin:        origin,
		StartPairing:  myServer.StartDaemonPairing, // the app's own call: a one-time code
		CancelPairing: myServer.CancelDaemonPairing,
	})
	if err != nil {
		log.Fatal(err)
	}
	stop := local.Subscribe(func(s goclient.LocalState) { fmt.Println(s.Found.Found, s.Connecting, s.Error, s.CanConnect) })
	defer stop()
	local.Detect(ctx) // when the settings screen opens, and when the app gets focus
	if local.State().CanConnect {
		local.Connect(ctx) // the dialog opens on the user's computer
	}

	// 2. No daemon here: agentd.app for the user's Mac. Once it is open, Detect finds it and Connect pairs it.
	if downloads, ok := goclient.DesktopDownloadsFrom(""); ok { // "": agentd's own releases page
		fmt.Println(downloads.AppleSilicon, downloads.Intel)
	}
	// Or, on Linux or from a terminal, the one command that installs it, pairs it with this app and starts it.
	if command, ok := goclient.InstallCommand(goclient.InstallOptions{App: origin, Code: "K7QX-29PD"}); ok {
		fmt.Println(command)
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

	local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: origin, Mode: goclient.ModeBrowser})
	if err != nil {
		log.Fatal(err)
	}
	local.Detect(ctx)
	if local.State().CanConnect { // a daemon is here and this origin has no grant yet
		local.Connect(ctx) // the dialog opens on the user's computer
	}
	found := local.State().Found
	if !found.Found || !found.Granted.Browser {
		log.Fatal(local.State().Error)
	}

	session, err := goclient.OpenSession(ctx, goclient.Daemon{Port: found.Port}, origin, "claude") // found.Agents lists them
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
