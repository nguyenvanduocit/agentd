# goclient

What a Go program uses to find the agentd on the user's computer, ask the user to connect it, and run an agent through it: the Go counterpart of `@agentd/client` (`sdk/web/`), for Go programs that talk to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624) the way a web page does. Standard library and [`gorilla/websocket`](https://github.com/gorilla/websocket) only; the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

A page has an origin the browser puts in every request; a Go program has none, so **every call takes the app's origin**, written the way a browser writes it (see [Origins](#origins)).

**Try it first:** `examples/askagent/` is a 98-line program that goes through every state below. Run it against a [test daemon](#test-without-clicking).

## Install

```sh
go get github.com/nguyenvanduocit/agentd@latest
```

The package is part of agentd's Go module and exists from **v0.7.0** (the next release): the tags v0.6.0 and earlier do not contain it, and `go get` answers `found (v0.6.0), but does not contain package`. Pin the version you tested (`@v0.8.0`): `go.sum` records its hash, and a published tag never changes.

New in 0.7.0 is marked *since 0.7.0* below.

## Use

Two ways to use the daemon: **server mode**, where the daemon connects to your app's server and the server runs the agents, and **browser mode**, where your program runs an agent on the user's computer itself.

### Server mode

<!-- fragment -->
```go
// myServer is your server: StartDaemonPairing mints a one-time code, CancelDaemonPairing drops it.
```

```go
const origin = "https://app.example.com" // your server's origin: the daemon pairs with the app there

// 1. The daemon on this computer: is it there, and can the user connect it to this app?
local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
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
// Or, on Linux or from a terminal, the one command that installs it, pairs it with this app and starts it.
if command, ok := goclient.InstallCommand(goclient.InstallOptions{App: origin, Code: "K7QX-29PD"}); ok {
	fmt.Println(command)
}
```

### Browser mode

The program asks for its own grant, opens an agent, and speaks ACP to it with [`acp-go-sdk`](https://github.com/coder/acp-go-sdk): `session.Stdout()` is the agent's stdout and `session.Stdin()` its stdin, as `acp.NewClientSideConnection` takes them.

```go
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
```

```go
const origin = "http://localhost:5173" // https, or http on a loopback host: what the gateway accepts

local, err := goclient.NewLocalDaemon(goclient.LocalDeps{Origin: origin, Mode: goclient.ModeBrowser})
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
```

`local.OpenSession(ctx, agent)` *(since 0.7.0)* is the call after `Connect`: no port to carry, no origin to repeat. The lower-level `goclient.OpenSession(ctx, daemon, origin, agent)` is for a program that holds a `Daemon` itself (`Detected.Daemon()`).

Without an ACP library, the session is lines: `session.Stdout()` gives the agent's stdout lines (each ending in `\n`), `session.WriteLine(line)` and `session.Stdin()` send lines to its stdin.

### Telling the user what happened

`LocalState.Phase` *(since 0.7.0)* is the one thing a screen needs to choose what to show; the phases mean what `@agentd/client`'s do.

```go
render := func(s goclient.LocalState) {
	switch s.Phase {
	case goclient.PhaseDetecting:
		fmt.Println("Looking for agentd on this computer...")
	case goclient.PhaseAbsent:
		// Not installed, not running, or something between blocked the call: the program cannot tell which.
		fmt.Println("agentd is not here. To start it from a terminal install:", goclient.AgentdCommands().Start)
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
```

`Watch` *(since 0.7.0)* keeps looking while the state is absent, available or connected, so a person who installs agentd and comes back, or whose daemon ended, is found without the app doing anything. It does not look while the dialog is open, nor while the refusal of the user's last attempt is on the screen (a look clears it; once a cooldown's `RetryAt` has passed, it looks again). Listeners hear of a change only. Start it when the user shows interest (a settings screen), not at start-up.

`LocalState` also has `Reason` (the code of a refusal), `Message` (the daemon's own words), `RetryAt` and `Incompatible` (a sentence when the daemon speaks a protocol this client does not: update the side it names). `CanConnect` is false while `RetryAt` is in the future; `Connect` does nothing then.

### Origins

The gateway takes the origin exactly as a browser writes it in the `Origin` header. A program has no browser to get that right, and the mistake looks like "agentd is not running". `ValidateOrigin` *(since 0.7.0)* finds it first; `NewLocalDaemon` calls it, and the lower-level calls answer `ReasonOrigin` without a request.

| Origin | |
|---|---|
| `https://app.example.com`, `https://app.example.com:8443` | works |
| `http://localhost:5173`, `http://127.0.0.1:5199`, `http://[::1]:8080` | works: plain http is for localhost only |
| `http://127.0.0.1:5199/` | `goclient: origin "http://127.0.0.1:5199/" has a path; use "http://127.0.0.1:5199"` |
| `https://app.example.com/app`, `...?x=1`, `...#top` | a path, a query, a fragment: use `https://app.example.com` |
| `http://app.example.com` | plain http on a name that is not localhost: use https |
| `HTTPS://App.Example.com`, `https://app.example.com:443` | not in the form a browser sends: lowercase, no port the scheme already means |

When a daemon answers and turns the call down anyway (the gateway refuses the origin, the caller is another user), `Detect` says so in `Detected.Refused` and the state is `PhaseRefused` with that `Reason`, not "absent".

### Errors

```go
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
```

`OpenSession` looks once more when the last look found no allowed daemon, because the grant may have come since. `*SessionError`, `ErrNotAllowed` and `ErrNoDaemon` are *since 0.7.0*; before, every failure was a plain error.

### Knowing when the agent ended

`Done()` closes when the **socket** ends. The agent can exit and leave the socket open, so a program that waits on `Done()` waits for nothing. `Wait()` *(since 0.7.0)* is `exec.Cmd.Wait` for the agent:

```go
session.OnFrame(func(f goclient.Frame) { // out: a stdout line, err: stderr text, exit: the agent ended
	fmt.Println(f.Kind, f.Data)
})
if err := session.WriteLine(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`); err != nil {
	log.Fatal(err)
}
code, err := session.Wait() // blocks until the agent exits; an error if the socket ended first
fmt.Println("the agent exited with", code, err)
```

`Wait` returns the exit code (`-1` and an error wrapping `ErrClosed` when the socket ended first). The `OnFrame` callbacks have seen the exit frame by the time it returns. It blocks, so call it from a goroutine when the program also does something else.

### Cancelling Connect

`Connect` waits for the user's answer in the daemon's dialog, as long as `ctx` lives. If `ctx` ends first, the dialog **is still open** on the user's computer, and answering Allow there still connects. The reason is `ReasonCancelled` *(since 0.7.0)*, not "the daemon did not answer":

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if !local.Connect(ctx) && local.State().Reason == goclient.ReasonCancelled {
	// The dialog may still be open on the user's computer. If they allow it there, the next Detect finds the grant.
	fmt.Println(local.State().Error)
}
```

### Test without clicking

A test needs a daemon, and the dialog needs a click. Run a throwaway daemon on its own port, in its own directories, with the dialog answered by a script. **These variables are for tests only**: they replace what protects the user (the dialog, the real agents, the update check), so never set them for a daemon a person uses.

```sh
go build -o /tmp/agentd-test/agentd github.com/nguyenvanduocit/agentd/cmd/agentd
go build -o /tmp/agentd-test/fake-agent github.com/nguyenvanduocit/agentd/cmd/fake-agent
cd /tmp/agentd-test
# The dialog: the program gets the title and the text as arguments. Exit 0 and one absolute folder per line is Allow
# (the folders the agent may start in); any other exit is Deny.
printf '#!/bin/sh\necho /tmp/agentd-test\n' > allow.sh && chmod +x allow.sh
mkdir -p home
HOME=$PWD/home AGENTD_HOME=$PWD/home/.agentd XDG_CONFIG_HOME=$PWD/home/.config XDG_STATE_HOME=$PWD/home/.state \
  AGENTD_LOCAL_PORT=47661 AGENTD_CONSENT=$PWD/allow.sh AGENTD_AGENT_CLAUDE=$PWD/fake-agent \
  AGENTD_ADAPTERS=off AGENTD_TRAY=off AGENTD_RELEASE_URL= ./agentd run
```

The first four variables keep it away from your own `~/.agentd` and `~/.config/agentd`. `AGENTD_AGENT_CLAUDE` replaces the agent with a fake one that answers `initialize`. `AGENTD_CONSENT` answers the dialog. `AGENTD_LOCAL_PORT` moves the gateway off the ports of a real daemon, and the client then looks there:

```go
// A test daemon on port 47661 (AGENTD_LOCAL_PORT=47661, see "Test without clicking" in the README).
local, err := goclient.NewLocalDaemon(goclient.LocalDeps{
	Origin:  "http://localhost:5173",
	Mode:    goclient.ModeBrowser,
	Options: []goclient.Option{goclient.WithPorts(47661)},
})
if err != nil {
	log.Fatal(err)
}
local.Detect(context.Background())
```

To test a refusal, make the script `exit 1` (Deny), `sleep` past 60 s (timeout), or print no folder (Allow with no folder chosen). A Deny or a timeout is followed by a 30-second cooldown for that origin (`ReasonCooldown`, and `RetryAfter` when the daemon says how long); three refusals within ten minutes lock every dialog for a while (`ReasonLocked`).

### Tools from the app

An app can give its agent tools of its own: the app runs an MCP server, and the agent reaches it through the daemon. `session.Mcp()` is the app's end, a `io.ReadWriteCloser` of JSON-RPC lines; `session.McpServer(name)` is the stdio server to list in `session/new`, which the agent starts and which relays its MCP lines to `session.Mcp()`. Call `Mcp()` before `session/new`: lines that come before it is called are not kept. A page's agent may list only this server; see `daemon/README.md`.

```go
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
```

Any MCP server whose transport reads and writes JSON-RPC lines works in place of the `go-sdk` one. The agent may start the server again (a new `initialize` arrives on the same `Mcp()`). The bridge ends with the session and with its socket.

### Why a connection was refused

The words are the same in `@agentd/client` and in the daemon's tests: all three read `contract/testdata/page-reasons.json`. A daemon sends a legacy `reason` every client since 0.4 knows and, where a newer client can say it better, a specific `code` (`locked`, `no-folder`, `not-saved`); the `Reason` of a result is the code when this package knows it. A reason a newer daemon adds gets a sentence that names it.

| Reason | When | What to tell the user |
|---|---|---|
| `ReasonDenied` | the user pressed Deny, or closed the dialog | You declined. The app stays disconnected from this computer. |
| `ReasonTimeout` | the dialog was not answered in time (60 s) | The dialog on this computer was not answered in time. Try again and answer it. |
| `ReasonCooldown` | the user declined a moment ago; `RetryAfter` says how long | You declined a moment ago. Wait half a minute before asking again. |
| `ReasonLocked` | several requests were declined in a row; `RetryAfter` says how long | Several connection requests were declined in a row, so agentd is not asking for a while. Try again in a few minutes. |
| `ReasonBusy` | another request waits for an answer | Another connection request is waiting for an answer on this computer. Answer it first. |
| `ReasonNoDialog` | the computer has no way to ask (no graphical session) | This computer cannot show the confirmation dialog (no graphical session), so nothing was connected. |
| `ReasonNoFolder` | Allow was pressed with no folder chosen | No folder was chosen, so nothing was connected. Choose at least one folder to allow. |
| `ReasonPairing` | the app refused the pairing code (server mode) | The app refused the pairing code. Start the connection again to get a fresh one. |
| `ReasonNotSaved` | the daemon could not save the connection | agentd could not save the connection on this computer. Look at its log, then try again. |
| `ReasonOrigin` | the gateway does not accept the origin (see "Origins") | agentd does not accept requests from this address. Use the address the app is served from, without a path. |
| `ReasonHost` | the address used to reach the gateway was not accepted | agentd did not accept the address used to reach it. Open the page from its usual address. |
| `ReasonMalformed` | the daemon did not understand the request | agentd did not understand the request. Update agentd and try again. |
| `ReasonNotFound` | the daemon does not know the request (an older agentd) | This agentd does not know the request. Update agentd and try again. |
| `ReasonError` | the daemon could not carry the request out | agentd could not carry out the request. Look at its log, then try again. |
| `ReasonPeer` | the daemon runs as another user than the program | agentd runs as another user than this program, so it refuses the request. |
| `ReasonUnreachable` | (client side) the daemon did not answer | agentd on this computer did not answer. Check that it is running. |
| `ReasonCancelled` | (client side) the context ended while the dialog was open | You stopped waiting. The dialog may still be open: answering Allow there still connects. |

### Reference

- `NewLocalDaemon(deps)` makes a `*LocalDaemon`, safe for concurrent use: `State()` (a value: `Found`, `Connecting`, `Error`, `CanConnect`, `Incompatible`, and *since 0.7.0* `Phase`, `Reason`, `Message`, `RetryAt`), `Subscribe(fn)` (every change, in order; returns what stops it), `Detect(ctx)`, `Watch(ctx, every)`, `Connect(ctx) bool`, `Disconnect(ctx)`, `OpenSession(ctx, agent)`. `deps` is `LocalDeps{Origin, Mode, StartPairing, CancelPairing, Client, Options, Now}`: `Mode` is `ModeServer` (the zero value) or `ModeBrowser`, browser mode takes no pairing, `Client` replaces the calls to the daemon, and `Now` is the clock that `RetryAt` is read against. `Detect` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it). After an Allow, the state goes from connecting straight to connected. `Disconnect` takes back what this app was allowed and looks again; when the daemon refuses, `State().Error` says why. A daemon whose protocol (`Detected.Protocol`, read from `/v1/status`) is not one of `SupportedProtocols()` is `Found` with `State().Incompatible` set, `CanConnect` false, and `Connect` returns false without asking it.
- `OpenSession(ctx, daemon, origin, agent)` gives a `*Session`: `Stdout()` (each line ending in `\n`; EOF when the agent exits or the socket closes), `Stdin()` (an `io.WriteCloser`: each complete line is sent, a partial one waits for its newline; closing it sends the partial line, not the session), the line-level `Write(line)` / `WriteLine(line)`, `OnFrame(fn)`, `Wait()`, `Done()`, `Close()`, `Pid`, `Cwd`. `Stdout()` starts the first time it is called and keeps no line from before: call it right after the session opens. `ctx` covers opening only. `Mcp()` and `McpServer(name)` (`{Name, Command, Args, Env}`, ACP's stdio MCP server) are for tools from the app.
- `AgentInfo` is `{Name, Title, Ready, Reason}`; `Title` is the name to show, the agent's `Name` when the daemon is older than the title.
- `DesktopDownloadsFrom(agentd)` is `{AppleSilicon, Intel}`, the links of agentd.app's disk images in the latest release, or false while the releases page is not a plain http(s) URL (`""` is `AgentdReleases`).
- `InstallCommand(InstallOptions{Agentd, App, Code})` is the curl line, or false while an origin or the code is not safe to paste into a shell.
- `AgentdCommands()` are what the user of an install from the terminal runs on their machine: `Status`, `Logs`, `Start`, each with the full path `~/.agentd/bin/agentd` (the installer puts nothing on `PATH`). The desktop app has no command line.
- Lower level: `Detect(ctx, origin, opts...)`, `ConnectServer(ctx, daemon, origin, code, opts...)`, `ConnectBrowser(ctx, daemon, origin, opts...)` (a `ConnectResult{Granted, Reason, Message, RetryAfter}`), `Disconnect(ctx, daemon, origin, opts...)` (a refusal is a `*DisconnectError{Status, Reason, Message}`), `ValidateOrigin(origin)`, `ReasonText(reason)`, `DefaultPorts()`, `SupportedProtocols()`, `Detected.Incompatible()`, `Detected.Daemon()`. Options: `WithHTTPClient`, `WithDialer`, `WithPorts`, `WithTimeout`.

The app connected is the one at **the origin you name**: the daemon refuses a request that names another server.

### Differences from `@agentd/client`

- Every call takes the app's `origin`; a page's is the browser's, which is why `ValidateOrigin` exists here.
- `Stdout()`, `Stdin()` and `Mcp()` are an `io.Reader`, an `io.WriteCloser` and an `io.ReadWriteCloser` in place of web streams; `Wait()` and `Done()` replace `onClose`.
- `McpServer(name)` returns an error in place of throwing; `Disconnect` returns an error when the daemon did not confirm.
- `ReasonText` has the same sentences for the same reasons, from the same file.
- An empty releases page (`Agentd`, `DesktopDownloadsFrom("")`) is agentd's own; `@agentd/client` takes an empty string as unsafe.
- `Watch` and the phases exist in both.

## Develop

```sh
go vet ./sdk/goclient/... && go test ./sdk/goclient/...   # unit tests, and a real daemon/gateway run in process with fake agents
```

Every Go block of this README is a quotation of `example_test.go` or `examples/askagent/main.go`, which are compiled with the package; `readme_test.go` fails when a line is not in them, or when the README names a `goclient.` identifier that is gone.
