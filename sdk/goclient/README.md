# goclient

What a Go program uses to find the agentd daemon on the user's computer, ask it to connect, and run an agent through it: the Go counterpart of `@agentd/client` (`sdk/web/`), for Go apps that talk to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624) the way a web page does. Standard library and [`gorilla/websocket`](https://github.com/gorilla/websocket) only; the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

A page has an origin the browser puts in every request; a Go program has none, so **every call takes the app's origin**. The gateway accepts an `https` origin, or `http` on a loopback host (`http://localhost:5173`), and the daemon connects a server mode app at that origin.

## Install

The package is part of agentd's Go module; the tag of a release is the module version:

```
go get github.com/nguyenvanduocit/agentd/sdk/goclient@v<version>
```

`v<version>` is the release it was published with (`v0.5.0`, for example). `go.sum` records the module's hash, and a published tag never changes: a fix is a new version in a new release.

## Use

Two ways to use the daemon: **server mode**, where the daemon connects to your app's server and the server runs the agents, and **browser mode**, where your program runs an agent on the user's computer itself.

### Server mode

```go
import "github.com/nguyenvanduocit/agentd/sdk/goclient"

const origin = "https://app.example.com" // your server's origin: the daemon pairs with the app there

// 1. The daemon on this computer: is it there, and can the user connect it to this app?
local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
	Origin:        origin,
	StartPairing:  myServer.StartDaemonPairing, // func(ctx) (code string, err error): the app's own call, a one-time code
	CancelPairing: myServer.CancelDaemonPairing, // func()
})
stop := local.Subscribe(func(s goclient.LocalState) { render(s) }) // Found, Connecting, Error, CanConnect
defer stop()
local.Detect(ctx) // when the settings screen opens, and when the app gets focus
if local.State().CanConnect {
	local.Connect(ctx) // the dialog opens on the user's computer
}

// 2. No daemon here: agentd.app for the user's Mac. Once it is open, Detect finds it and Connect pairs it.
downloads, ok := goclient.DesktopDownloadsFrom("") // "": agentd's own releases page. downloads.AppleSilicon, downloads.Intel
// Or, on Linux or from a terminal, the one command that installs it, pairs it with this app and starts it.
command, ok := goclient.InstallCommand(goclient.InstallOptions{App: origin, Code: code})
```

### Browser mode

The program asks for its own grant, opens an agent, and speaks ACP to it with [`acp-go-sdk`](https://github.com/coder/acp-go-sdk): `session.Stdout()` is the agent's stdout and `session.Stdin()` its stdin, as `acp.NewClientSideConnection` takes them.

```go
import (
	"github.com/coder/acp-go-sdk"
	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

// acpApp is the ACP client of the agent: it shows what the agent says and answers its permission requests. The agent
// asks for no file or terminal of this client, so the embedded acp.Client is never called.
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

const origin = "http://localhost:5173" // https, or http on a loopback host

local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: origin, Mode: goclient.ModeBrowser})
local.Detect(ctx)
if local.State().CanConnect { // a daemon is here and this origin has no grant yet
	local.Connect(ctx) // the dialog opens on the user's computer
}

found := local.State().Found
session, err := goclient.OpenSession(ctx, goclient.Daemon{Port: found.Port}, origin, "claude") // found.Agents lists them: Name, Title, Ready, Reason
defer func() { _ = session.Close() }()

conn := acp.NewClientSideConnection(acpApp{}, session.Stdin(), session.Stdout())
_, err = conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: session.Cwd, McpServers: []acp.McpServer{}})
_, err = conn.Prompt(ctx, acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Hello")}})

local.Disconnect(ctx) // takes this origin's grant back; open sessions end
```

Without an ACP library, the session is lines: `session.Stdout()` gives the agent's stdout lines (each ending in `\n`), `session.Write(line)` and `session.Stdin()` send lines to its stdin.

### Tools from the app

An app can give its agent tools of its own: the app runs an MCP server, and the agent reaches it through the daemon. `session.Mcp()` is the app's end, a `io.ReadWriteCloser` of JSON-RPC lines; `session.McpServer(name)` is the stdio server to list in `session/new`, which the agent starts and which relays its MCP lines to `session.Mcp()`. Call `Mcp()` before `session/new`: lines that come before it is called are not kept.

```go
import "github.com/modelcontextprotocol/go-sdk/mcp"

tools := mcp.NewServer(&mcp.Implementation{Name: "page", Version: "1"}, nil)
mcp.AddTool(tools, &mcp.Tool{Name: "current_page", Description: "The page the user is on"},
	func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "/projects/42"}}}, nil, nil
	})
stream := session.Mcp()
go tools.Run(ctx, &mcp.IOTransport{Reader: stream, Writer: stream})

server, err := session.McpServer("page") // fails on a daemon older than 0.4
env := make([]acp.EnvVariable, len(server.Env))
for i, e := range server.Env {
	env[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
}
_, err = conn.NewSession(ctx, acp.NewSessionRequest{
	Cwd:        session.Cwd,
	McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: server.Name, Command: server.Command, Args: server.Args, Env: env}}},
})
```

Any MCP server whose transport reads and writes JSON-RPC lines works in place of the `go-sdk` one. The agent may start the server again (a new `initialize` arrives on the same `Mcp()`). The bridge ends with the session and with its socket.

These snippets are compiled with the package (`example_test.go`).

### Reference

- `NewLocalDaemon(deps)` makes a `*LocalDaemon`, safe for concurrent use: `State()` (a value: `Found`, `Connecting`, `Error`, `CanConnect`), `Subscribe(fn)` (every change, in order; returns what stops it), `Detect(ctx)`, `Connect(ctx) bool`, `Disconnect(ctx)`. `deps` is `LocalDeps{Origin, Mode, StartPairing, CancelPairing, Client, Options}`: `Mode` is `ModeServer` (the zero value) or `ModeBrowser`, browser mode takes no pairing, and `Client` replaces the calls to the daemon. `Detect` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it). `Connect` puts the reason of a refusal in `State().Error` in plain words. `Disconnect` takes back what this app was allowed and looks again; when the daemon refuses, `State().Error` says why. A daemon whose protocol (`Detected.Protocol`, read from `/v1/status`) is not one of `SupportedProtocols()` is `Found` with `State().Incompatible` set to a sentence naming the side to update, `CanConnect` false, and `Connect` returns false without asking it.
- `OpenSession(ctx, daemon, origin, agent)` gives a `*Session`: `Stdout()` (each line ending in `\n`; EOF when the agent exits or the socket closes), `Stdin()` (an `io.WriteCloser`: each complete line is sent, a partial one waits for its newline; closing it sends the partial line, not the session), and the line-level `Write(line)`, `OnFrame(fn)`, `Done()`, `Close()`, `Pid`, `Cwd`. `Stdout()` starts the first time it is called and keeps no line from before: call it right after the session opens. `ctx` covers opening only. `Mcp()` and `McpServer(name)` (`{Name, Command, Args, Env}`, ACP's stdio MCP server) are for tools from the app.
- `AgentInfo` is `{Name, Title, Ready, Reason}`; `Title` is the name to show, the agent's `Name` when the daemon is older than the title.
- `DesktopDownloadsFrom(agentd)` is `{AppleSilicon, Intel}`, the links of agentd.app's disk images in the latest release, or false while the releases page is not a plain http(s) URL (`""` is `AgentdReleases`).
- `InstallCommand(InstallOptions{Agentd, App, Code})` is the curl line, or false while an origin or the code is not safe to paste into a shell.
- `AgentdCommands()` are what the user of an install from the terminal runs on their machine: `Status`, `Logs`, `Start`.
- Lower level: `Detect(ctx, origin, opts...)`, `ConnectServer(ctx, daemon, origin, code, opts...)`, `ConnectBrowser(ctx, daemon, origin, opts...)`, `Disconnect(ctx, daemon, origin, opts...)` (a refusal is a `*DisconnectError{Status, Reason, Message}`), `ReasonText(reason)`, `DefaultPorts()`, `SupportedProtocols()`, `Detected.Incompatible()`. Options: `WithHTTPClient`, `WithDialer`, `WithPorts`, `WithTimeout`.

The app connected is the one at **the origin you name**: the daemon refuses a request that names another server, and a gateway refuses an origin that is not `https` or `http` on a loopback host.

Call `Detect` when the user shows interest (a settings screen, a "connect" step), not at start-up.

### Differences from `@agentd/client`

- Every call takes the app's `origin`; a page's is the browser's.
- `Stdout()`, `Stdin()` and `Mcp()` are an `io.Reader`, an `io.WriteCloser` and an `io.ReadWriteCloser` in place of web streams; `Done()` is a channel in place of `onClose`.
- `McpServer(name)` returns an error in place of throwing; `Disconnect` returns an error when the daemon did not confirm.
- `ReasonText` answers a sentence for the refusals the gateway sends that are not a `Reason` (`peer`, `host`, `error`).
- An empty releases page (`Agentd`, `DesktopDownloadsFrom("")`) is agentd's own; `@agentd/client` takes an empty string as unsafe.

## Develop

```
go vet ./sdk/goclient/ && go test ./sdk/goclient/   # unit tests, and a real daemon/gateway run in process with fake agents
```
