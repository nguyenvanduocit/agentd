# goserver

`github.com/nguyenvanduocit/agentd/sdk/goserver` is the library for **your app's server**: it lets your users' computers run coding agents (Claude Code, Codex, Grok) for your app.

A user installs agentd on their computer. Your app shows a pairing code; the user gives it to agentd; from then on the agentd on that computer dials out to your server, and your server can start an agent there and speak [ACP](https://agentclientprotocol.com) with it, as it would over a local pipe. A user pairs as many computers as they like. Nothing on the user's side listens for your server: there is no port to open and no address to know.

```
your page ──▶ your server ◀── sdk/goserver ───┐
                                              │  agentd dials out (Socket.IO, namespace /agentd)
                  agent process ◀── agentd on the user's computer
```

For the page that lets a user connect their computer, see [`sdk/web`](../web/README.md) (JavaScript) or [`sdk/goclient`](../goclient/README.md) (Go programs). `examples/minimal-app` is a complete app on both.

## Install

```sh
go get github.com/nguyenvanduocit/agentd@v0.9.0
```

The package is `sdk/goserver` from v0.7.0. Releases up to v0.6.0 carry the same library as `github.com/nguyenvanduocit/agentd/hub`, without the names marked "Since 0.7.0" below.

## Wire it up

```go
func serve(tokens goserver.Tokens) error {
	h := goserver.New(goserver.Options{Tokens: tokens})

	mux := http.NewServeMux()
	srv := &http.Server{Addr: "127.0.0.1:8090", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	srv.RegisterOnShutdown(h.Attach(mux)) // /socket.io/, the /agentd namespace, POST /agentd/pair

	return srv.ListenAndServe()
}
```

`Attach` (since 0.7.0) does everything a daemon needs from your web server in one call. `Options.Tokens` is the only required option: `New` panics without it.

- **`Tokens`** is where paired daemons are kept. `goserver.NewMemoryTokens()` keeps them in memory, which is right for tests and examples and loses every pairing when the process ends. A real app implements `goserver.Tokens` over its database: seven methods (`ByHash`, `ByID`, `List`, `Add`, `Rename`, `Delete`, `Seen`), and the godoc of each says what it owes. The one rule that matters: a method that takes a `userID` answers only that user's daemons, and a daemon of another user is `goserver.ErrNotFound`.
- **An app that already runs a Socket.IO server** does not call `Attach`, which registers `/socket.io/` itself. It makes its server with `goserver.SocketOptions()`, calls `h.Mount(io, mux)`, and routes `/socket.io/` to `io` itself. `Mount` alone does not serve `/socket.io/`: a daemon that cannot reach it retries on the user's machine ("bad handshake") while your app shows it as offline.
- **Behind a proxy**, set `Options.ClientIPHeader` to the header in which it puts the client's address (`Fly-Client-IP`): wrong pairing codes are counted per address.
- **Several instances of the app** share pairing codes through `Options.Pairing` (`goserver.PairingStore`). Nil keeps them in this process, which is right for one instance. `pairingtest.Run` checks a store. After another instance changes a code, call `Refresh` so this instance's browser tabs read the list again. Since 0.8.0.

## Pair a computer

```go
func pairingCode(h *goserver.Server, userID string) (string, error) {
	list, err := h.For(userID).StartPairing()
	if err != nil {
		return "", err
	}
	return list.Pairing.Code, nil // XXXXX-XXXXX, good for ten minutes, one use
}
```

Show the code with the command the user runs on their computer:

```sh
agentd pair --app https://app.example.com --code XXXXX-XXXXX
```

For a first run on one machine, `--app http://127.0.0.1:8090` works too: agentd accepts `http` for a loopback address and requires `https` for any other, and drops a path from the address.

`h.For(userID)` is the Server as one user sees it: every method has the user filled in, so a handler cannot name another user by mistake.

## List the computers, and watch them

```go
func follow(h *goserver.Server, userID string) {
	lists, cancel := h.Watch(userID) // the current list first, then every change
	defer cancel()                   // closes the channel
	for list := range lists {
		for _, d := range list.Daemons {
			fmt.Println(d.Name, d.State, d.Incompatible)
		}
	}
}
```

A `goserver.List` is plain JSON (`json.Marshal(list)`) for your page. Each `Info` has `State`, which is `goserver.StateOnline` or `goserver.StateOffline`, and `Agents` with `Name`, `Title`, `Ready` and a `Reason` when one is not. A computer whose agentd and your app speak protocols that do not meet is `StateOffline` with `Incompatible` set to a sentence naming the side to update: show it. `Watch` suits a page that updates live (Server-Sent Events, a WebSocket); `List(userID)` suits one that does not.

## Open an agent and speak ACP

```go
func ask(ctx context.Context, h *goserver.Server, userID string) (string, error) {
	conn, err := h.OpenLatest(ctx, userID, "claude") // the user's newest online computer
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// conn is the agent's stdin and stdout: one JSON-RPC message per line.
	if _, err := fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`); err != nil {
		return "", err
	}
	return bufio.NewReader(conn).ReadString('\n') // {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,...}}
}
```

`OpenLatest` picks the computer; `Open(ctx, userID, daemonID, "claude")` opens on the one the user chose (`h.Online(userID)` lists the ones that are up). The agent starts in a directory the daemon chooses; `conn.Cwd()` says which. `conn.Chdir(ctx, dir)` moves it to another directory the user allowed this app (absolute path): the daemon ends the agent and starts the same program in `dir`, and `conn` carries on with the new process. That is a new agent that has not been initialized, so speak ACP to it from the start (`initialize`, then `session/new` or `session/load`). The daemon's refusal is an error that starts "your daemon:" and leaves the agent where it is.

`Conn` is a raw byte stream: agentd relays the agent's lines and does not interpret them. For a real conversation run an ACP client over it. [`github.com/coder/acp-go-sdk`](https://github.com/coder/acp-go-sdk) does:

```go
func talk(ctx context.Context, conn goserver.Conn, client acp.Client) error {
	// client is yours: SessionUpdate receives the agent's output, RequestPermission asks the user.
	agent := acp.NewClientSideConnection(client, conn, conn)
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		return err
	}
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: conn.Cwd()})
	if err != nil {
		return err
	}
	_, err = agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Hello")}})
	return err
}
```

`conn.Done()` closes when the agent process is gone, and `conn.Stderr()` holds the last of what it wrote on standard error: show it when a turn fails.

## Errors

Every error is a value to test with `errors.Is` or `errors.As`, and the godoc of each method lists the ones it can return. Most texts are sentences for the user.

| Error | When | What to tell the user |
|---|---|---|
| `ErrUnknownDaemon` | the id is not one of the user's computers (never existed, disconnected, someone else's). `ErrNoSuchDaemon` is the same value | the text: "The computer chosen is no longer paired: pick another" |
| `OfflineError` (`Name`) | paired but not connected. `Restart` and `Stop` answer it too (text "&lt;name&gt; is offline") | start agentd on that computer, or pick another |
| `IncompatibleError` (`Name`, `Reason`) | connected, but agentd and the app speak protocols that do not meet | `Reason` names the side to update |
| `*AgentNotReadyError` (`Daemon`, `Agent`, `Title`, `Reason`, `Offered`) | the computer does not have the agent, or says it is not ready (since 0.7.0) | `Reason`: "Node is not installed"... |
| `ErrNoDaemon` | `OpenLatest`: the user has paired no computer | point them at pairing |
| `ErrNoOnlineDaemon` | `OpenLatest`: they have some, none is online (since 0.7.0) | start agentd on one |
| `ErrBadName` | `Rename`: empty, over 64 characters, or a control character (since 0.7.0) | the text says the rule |
| `ErrNameTaken` | `Rename`: another of their computers has the name | pick another name |
| another error | agentd refused or did not answer; the text starts "your daemon:" and carries its message | show it, and retry |

```go
func explain(err error) (status int, message string) {
	var (
		offline  goserver.OfflineError
		incompat goserver.IncompatibleError
		notReady *goserver.AgentNotReadyError
	)
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, goserver.ErrUnknownDaemon), errors.Is(err, goserver.ErrNoDaemon):
		return http.StatusNotFound, err.Error()
	case errors.As(err, &offline), errors.As(err, &incompat), errors.Is(err, goserver.ErrNoOnlineDaemon):
		return http.StatusConflict, err.Error()
	case errors.As(err, &notReady):
		return http.StatusUnprocessableEntity, err.Error()
	}
	return http.StatusBadGateway, err.Error()
}
```

## Run it locally

You need an `agentd` binary and, to try it without a real agent, the fake one. From a checkout of this repository:

```sh
go build -o /tmp/agentd ./cmd/agentd && go build -o /tmp/fake-agent ./cmd/fake-agent
```

Run your server, then pair and run a daemon that touches nothing of yours. Everything below is for **tests and development only**:

```sh
W=$(mktemp -d)
export HOME=$W AGENTD_HOME=$W/agentd XDG_CONFIG_HOME=$W/config XDG_STATE_HOME=$W/state   # a home of its own
export AGENTD_LOCAL_PORT=47681       # the gateway pages use; 0 for any free port
export AGENTD_ADAPTERS=off           # no npm install of the Claude and Codex adapters
export AGENTD_AGENT_CLAUDE=/tmp/fake-agent   # "claude" is this program: it answers initialize and echoes

/tmp/agentd pair --app http://127.0.0.1:8090 --code XXXXX-XXXXX   # the code your app showed
/tmp/agentd run
```

`AGENTD_AGENT_<NAME>` replaces the command of an agent and counts it as ready. When you also test a page connecting to your computer, `AGENTD_CONSENT=<program>` stands in for the dialog: the daemon runs it, and exit 0 with one absolute folder per line on its standard output is Allow. Server mode, where an app pairs with a code, asks no dialog.

`sdk/goserver/goservertest` wraps this recipe for Go tests; see its README.

## Tools for the agent

An app can give its agents MCP tools that run in your server. Implement `goserver.Tools` (`Owns` says which sessions are yours, `Serve` runs the MCP server of one), pass it as `Options.Tools`, and list the bridge in the `session/new` you send:

```go
func bridge(grantID string) map[string]any {
	command, args := goserver.BridgeCommand(grantID)
	return map[string]any{"name": "my-tools", "command": command, "args": args}
}
```

agentd starts that command on the user's computer and carries its lines back to `Tools.Serve` over a socket of its own. Revoking the daemon cuts the bridge.

## Limits

- **One process serves a `Server`.** Pairing codes, the count of wrong codes and the daemons' sockets live in its memory. An app with several instances sends `/agentd`, `/agentd/pair` and the calls that start a pairing to one of them.
- **A paired daemon is trusted with a token** (SHA-256 kept in `Tokens`); revoking it (`Revoke`) deletes the record and closes its connection and bridges at once.
- **`OnAgentsChanged`** holds one function; registering another replaces it.
