# agentd

Runs your coding agents (Claude, Codex, Grok) on your own machine for the apps you allow. An app's server is the ACP
client; the daemon starts the agent process, relays its stdio lines over a socket, and runs the agent with your MCP
servers, skills and folders. It serves any number of apps (each one dials in from the daemon's side) and shows an icon in
the menu bar. Its only listening port is a gateway on `127.0.0.1`, which web pages use to find it and to ask for a
connection. A Go program; Node.js >= 20 only for the agents' ACP adapters.

## Packages

One directory per concern, each named for what it provides. `cmd/agentd` is the entry point; `run` wires the rest.

| Package | What it is |
|---------|------------|
| `run` | `agentd run`: starts everything below and ends it in order. |
| `cli` | Parses the command line into a command. |
| `agents` | Which agents (claude, codex, grok) the daemon can run, whether each is ready, and the process of each session. |
| `proc` | Process plumbing: running a program to the end, process groups, line splitting, the log. |
| `home` | The daemon's view of its machine: where its files live, finding a program on `PATH`, exit codes, settings. |
| `registry` | What the daemon remembers between runs (`daemon.json`): the apps it is paired with, the web origins it allowed and their folders. `agentd pair` and `agentd apps`. |
| `link` | The daemon's connection to an app's server: one Socket.IO socket per paired app. |
| `gateway` | The "local gateway": the HTTP and WebSocket server on `127.0.0.1` that web pages use to find the daemon, ask for access and run agents. |
| `consent` | What the daemon asks the user before an origin gets access: the text, the native dialogs, the check of who is connecting. |
| `ui` | The daemon's end of the channel to the app (or tray program): states out, choices and the consent window's answers in. |
| `pagetools` | The tools a web page serves to its own agents: the messages between a page and the daemon. |
| `mcpbridge` | `agentd mcp`: the stdio MCP server an agent starts, which relays to the app's server or to the page. |
| `service` | `agentd service ...`: the launchd or systemd service of a headless install. |
| `update` | Keeps the daemon on the version the release server serves: signature check, install, rollback. |
| `launcher` | Only tests: `launcher.sh` under a real shell. |

## Install

An app's web page shows one command; it installs, pairs and starts the daemon as a service of your user:

```
curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash -s -- --app <app origin> --code <code>
```

`install.sh` is a file of agentd's latest GitHub release; `<app origin>` is the app that gave you the command and `<code>` its
one-time pairing code.

macOS (arm64, x64) and Linux (x64, arm64, glibc). It needs `curl`, `tar`, `bash` and `shasum` or `sha256sum`, no
sudo, and writes only under your home directory. Windows: use WSL. The script checks the daemon tarball against the
sha256 baked into it when the release was built and installs the engine of the machine's platform (the tarball carries
`agentd-<os>-<arch>` for darwin and linux on arm64 and amd64) as `~/.agentd/daemon/<version>/agentd`. The agents' adapters run on
Node.js: it uses the machine's Node.js >= 20 (with `npm`), or else downloads the pinned LTS
from nodejs.org into `~/.agentd/node` (checked against that release's `SHASUMS256.txt`). It does not check the
release signature: the first install trusts GitHub's TLS and the script it serves, so a compromised release can
serve another script. Signing protects the updates of a daemon already installed.

| Flag | Meaning |
|------|---------|
| `--app <origin> --code <code>` | Pair this machine with the app. Without them the saved pairings are used. |
| `--no-service` | Install and pair only; start it yourself: `~/.agentd/bin/agentd run`. |
| `--foreground` | No service: run the daemon in this terminal (debugging, e2e). |

Running the command again installs the latest release and restarts the service on it.

## Commands

`~/.agentd/bin/agentd` is the launcher; it runs `~/.agentd/daemon/current/agentd` (the Go engine), with Node.js on its PATH for the adapters.

```
agentd pair --app <origin> --code <code>      pair this machine with an app
agentd [run] [--app <origin>] [--dir <dir>]    connect and serve agents
agentd apps [list]                             the apps and web pages this machine is connected to
agentd apps remove <origin>                    disconnect one of them (a running daemon ends its sessions within 2 s)
agentd acp [<agent>] [--rotate] [--token]       the address ACP apps connect to, with its token (see The standard ACP endpoint)
agentd service install [--dir <dir>]           start at login, restart when it crashes, restart now on the current version
agentd service start | restart                 start it again after a Stop from the web UI; restart it
agentd service uninstall | status
agentd service logs [-n <lines>] [-f]
agentd version
```

| Option | Meaning |
|--------|---------|
| `--app <origin>` | Serve only the saved app with this origin. |
| `--dir <dir>` | Working directory of the agents. Default: the directory the daemon was started in. |
| `--config <file>` | Config file. Default: see below. |
| `--rotate`, `--token` | `acp` only: make a new token first; print the token alone. With any other command they are a mistake (exit 2), not ignored. |

`mcp --session <id>` (an app's session) and `mcp --page` (a page's session, with `AGENTD_MCP_SOCKET` and `AGENTD_MCP_TOKEN` in its environment) are the MCP stdio bridges that agents start through `agentd-mcp`; they are not meant to be run by hand.

## Apps, web pages and the gateway

One daemon serves many apps. An app is a server that implements `contract/define.acp.ts` (with the `sdk/goserver/` library, or by hand; MindChains is one): the daemon pairs with it, dials out to it and runs agents for it, with a socket, a set of agents and an MCP wrapper (`bin/<n>/agentd-mcp`) of its own per app, so sessions never mix. In its hello the daemon names the range of protocols it speaks (`protocolMin`, `protocolMax`); a daemon older than 0.7 names none and speaks exactly protocol 1. The daemon keeps the link when the app's reply names a protocol in its range and leaves the app when it does not (`speaks protocol X, this daemon speaks 1-2: disconnecting it`). A call of the app's server that the daemon does not know is answered at once with an error (`this agentd does not know <call>: update agentd`), not left to time out. Updates do not come from an app but from the release server (see Update).

**The gateway** is an HTTP and WebSocket server on `127.0.0.1`, port 47615 or the next free one up to 47624 (`AGENTD_LOCAL_PORT` replaces the first port; `0` means any free port). The daemon logs `local gateway on 127.0.0.1:<port>`, or `local gateway off` when all ten are taken (paired apps keep working). Every request is checked in order: the connection comes from a process of the daemon's own OS user (else `403 {granted:false, reason:"peer"}`, and a WebSocket upgrade is closed); `Host` is `127.0.0.1:<port>` or `localhost:<port>` (else 421, DNS rebinding); `Origin` is present and is `https`, or `http` on a loopback host (else 403; the browser sets it, page script cannot forge it; `/acp` alone also takes a request with no `Origin`, see The standard ACP endpoint). The origin is reflected in `Access-Control-Allow-Origin`.

| Request | Answer |
|---------|--------|
| `GET /v1/status` | `{name:"agentd", version, protocol, granted:{server, browser}}`; `protocol` is the version of the page protocol (`gateway.PageProtocol`, 1), which a page's SDK checks against the ones it speaks; it is bumped only when the page protocol changes incompatibly, and is not the protocol an app's server speaks to the daemon (`home.Protocol`, see Apps); `agents` only for an origin that is granted. `granted.browser` is false for a grant older than 30 days. `granted.server` is true while the daemon is linked to the app at the caller's origin (an app that Stop or a refused token detached is saved but not linked, and reads false so the page can offer to connect it again). |
| `POST /v1/connect {mode:"server", code}` | Shows the dialog. On Allow: pairs with the server **at the caller's origin** using `code`, saves the app and links to it at once (pairing an app that is already linked replaces its token and links again). A body that names a server is `400`: a page cannot point the daemon at an origin that is not its own. `200 {granted:true}` |
| `POST /v1/connect {mode:"browser"}` | Shows the dialog. On Allow: saves the origin as a browser grant (it may then open sessions) for 30 days. An origin whose grant is younger than that gets `200` without a dialog; a lapsed one is asked again, and Allow gives it a new grant with the folders just chosen. |
| `DELETE /v1/connect` | Forgets the caller's browser grant and the app at its origin, and ends their sessions (the page's open `/v1/session` sockets close with their agents). Never touches another origin's app. The daemon stays up so the page can connect again. |
| `GET /v1/session` (WebSocket) | Origins whose browser grant has not lapsed only. One socket is one set of agents, ended with the socket; at most 5 sessions per socket. Messages are in `daemon/pagetools/localproto.go`: the page sends `open`, `write`, `close`, `mcp`; the daemon sends `hello`, `opened`, `error`, `frame`, `mcp`. The page is the ACP client of the lines, and may serve the agent MCP tools (below). |
| `GET /acp/<agent>` (WebSocket) | The standard ACP transport: one connection is one agent process, spoken to in plain ACP. A web page needs its grant, a program the local token. See The standard ACP endpoint. |

**Tools from the page.** A page's agent can call tools the page itself serves. `opened` carries `mcp: {command:"<tmp>/agentd-XXXXXX/bin/page/agentd-mcp", args:[], env:[{name:"AGENTD_MCP_SOCKET", value}, {name:"AGENTD_MCP_TOKEN", value}]}`, the stdio MCP server the page lists (with a name) in `session/new`'s `mcpServers`. It is the only server a page may list: a `session/new`, `session/load`, `session/resume` or `session/fork`, or any other `session/...` request that carries `mcpServers`, whose list holds anything else (another command, an argument or a variable that is not this bridge's, an http or sse server) is answered with `-32602` and never reaches the agent. So is a `_meta` on those requests and on `authenticate`, because the Claude adapter takes its program, hooks, tools, environment and own `mcpServers` from `_meta.claudeCode.options` (claude-agent-acp 0.86.0, `dist/acp-agent.js:7015-7130`, and `:1544` for `authenticate`). The check reads a line as the agent does (exact keys, the last of two equal keys wins, a newline inside a line or a line that is not a JSON object is refused: `daemon/agents/pageguard.go`) and runs before the folder check. An app's server drives its sessions as it likes. The command is the pages' own wrapper (it runs `mcp --page`) by absolute path, as the ACP schema asks, so it starts whatever `PATH` the agent gives its MCP servers (verified with Claude's adapter 0.86: `test/integration/pagetools.real.test.ts`, run with `RUN_REAL_CLAUDE=1`); it sends the token to the daemon's Unix socket `<tmp>/agentd-XXXXXX/mcp.sock`, then relays each line of the agent's MCP client to the page as `{t:"mcp", id, line}`, and each `{t:"mcp", id, line}` of the page back to the agent. The socket's directory is mode 0700, so other OS users cannot reach it; the token (256 random bits, one per session) is in the environment, not the arguments, which other users can read with `ps`. It opens no port. The token is bound to the page's socket and the session, and dies with either, which ends the bridge (it exits 1); a token the daemon does not know is refused and the bridge exits 1 with the reason. If the agent starts the server again, the newer bridge replaces the older. A page `mcp` while no bridge is connected is an `error` for that session. Lines keep the 32 MiB cap, and the bridge's lines to a page wait, as the agent's output does, while the page's socket is behind. A daemon that cannot listen on the socket (a temporary path too long for one) says so in its log and sends no `mcp`.

Refusals are `{granted:false, reason, code?, message?, retryAfter?}`. `reason` is the value every page SDK since 0.4 knows; `code`, when there is one, is the more specific reason it refines, so that an older SDK keeps the reason it has a sentence for and a newer one says more. The reasons and the sentence of each are in `contract/testdata/page-reasons.json`, which the gateway's and both SDKs' tests check themselves against (`gateway.Reasons()`):
- `403 peer` (the caller's process belongs to another OS user, or its owner cannot be found), `421 host` (the Host is not the gateway's own), `403 origin` (no Origin, or not one a browser sends), `404 not-found`, `400 malformed`, `500 error` (the gateway could not read its registry; `message` says why).
- `403 denied`, `403 timeout`: the user said no, or did not answer. Either starts the origin's wait.
- `429 cooldown` (30 s after a deny or timeout, per origin) and `429 cooldown` with `code:"locked"` (three refusals within ten minutes from any origins lock every dialog until the oldest is ten minutes old, so new subdomains do not buy new dialogs; an Allow clears the count). Both carry `retryAfter`, the whole seconds until asking again can work (for a lockout, the longer of the two waits), and the same number as the `Retry-After` header, which a page can read (`Access-Control-Expose-Headers`).
- `409 busy` (one dialog at a time).
- On `/acp` also `405` and `426` `malformed` (not a GET, or not a WebSocket upgrade), `429 busy` (the connection limits, no `retryAfter`), `503 error` (the agent is not ready, with its own reason, or the daemon is stopping) and `400 malformed` (no agent named, or two different ones).
- `503 no-dialog` (the machine cannot ask: nothing is granted) and `503 no-dialog` with `code:"no-folder"` (the user allowed without choosing a folder: nothing is granted).
- `502 pairing` (the app refused the code; `message` is its answer) and `502 pairing` with `code:"not-saved"` (a browser grant could not be saved on this computer; `message` says why).

`sdk/web/` (`@agentd/client`) is the client for pages.

**The standard ACP endpoint** is `GET /acp/<agent>` upgraded to a WebSocket, the WebSocket profile of ACP's transports RFD ([Streamable HTTP & WebSocket Transport](https://agentclientprotocol.com/rfds/streamable-http-websocket-transport.md), Active since 2026-07-02, targeted for ACP v1). It lets an ACP client that knows nothing of agentd (a library such as `acp-go-sdk` or `@agentclientprotocol/sdk`, an editor, a script) talk to an agent the daemon runs: `ws://127.0.0.1:<port>/acp/claude`, `/acp/codex` or `/acp/grok`, or `/acp?agent=claude`. The port is the gateway's (the first free one of 47615-47624, logged at start, pinned by `AGENTD_LOCAL_PORT`); a page finds it with `GET /v1/status`. A bare `/acp` is `400` and lists the ready agents; an unknown agent is `404`; an agent that is not ready (an adapter still installing) is `503` with its own reason, before the upgrade.

- **One connection, one agent.** The daemon starts the agent when the socket opens and ends it (and everything it started) when the socket closes or drops; nothing outlives the connection. The client sends `initialize` first and may open several sessions in it, as the RFD says. The `101` carries `Acp-Connection-Id: <uuid v4>`, also in the log (`acp connection <id>: claude for <site>`).
- **Framing.** Every message is one WebSocket text frame of JSON-RPC, with no newline (one trailing `\n` or `\r\n` from the client is dropped). Binary frames and empty frames are ignored. A frame with a newline or carriage return inside it, or that is not UTF-8, closes the socket with `1007`: the agents' stdio framing allows one message per line, and two in a frame would be two messages nobody checked. A frame over the line cap (32 MiB) closes with `1009`. Each line the agent writes on stdout is one frame; its stderr is not sent (the last line of it goes into the close reason when the agent fails). When the agent exits the socket closes `1000` with `agent exited with code N`; the daemon stopping closes `1001`.
- **Who may connect.** The gateway's front door comes first and is unchanged: the connecting process must belong to the daemon's OS user (another user's upgrade is closed), and `Host` must be the gateway's own (`421`). Then: a request **with an `Origin`** is a web page. It needs the same live browser grant as `/v1/session` (`403 denied` before the upgrade otherwise, and it learns nothing about the agents), is held to the page rules (it may list no MCP server and set no `_meta`, there being no bridge to its tools on `/acp`; a request that breaks a rule gets `-32602` back; a line the daemon cannot read at all closes `1008`) and to the folders of its grant, and its connection ends when the grant is withdrawn or lapses. A request **with no `Origin`** is not a browser (a browser always sends `Origin` on a WebSocket handshake and script cannot remove it), but it is not necessarily a program that could start an agent itself: a dev container (Docker Desktop, OrbStack), a sandboxed app or an `ssh -R` tunnel reaches `127.0.0.1` through a forwarder that runs as the user, and the same-user check sees the forwarder. So it must present the **local token**, a secret in a file that only the user's own processes on the host can read: `Authorization: Bearer <token>`, or `?token=<token>` in the address for a client that takes only a URL (the header is the credential when both are given: a wrong header is not rescued by a right query). Without it the answer is `401`, with `WWW-Authenticate: Bearer realm="agentd"` and `{"granted":false,"reason":"denied","message":"this computer's agentd needs its local token: run `agentd acp` to get the address"}`, before the method, the path, the agent list, the readiness of an agent and the connection limits are looked at: such a caller learns nothing and takes no place among the connections. With the token it shows no dialog and has no folder limit or page rule: it could launch the adapter by hand, so this gives it nothing it did not have, except a connection that the menu bar counts ("Quit ends N agents") and the log names. An `Origin` that repeats, is empty or `null`, or is not in its serialized form is refused (`403 origin`), so a page cannot pass for a program.
- **The token.** 256 random bits (43 characters of URL-safe base64) in `$AGENTD_HOME/acp/token` (`~/.agentd/acp/token`), mode 0600 in a directory of 0700. The daemon makes it when it first starts and keeps it across restarts, so an address a client was configured with goes on working. It reads the file at each request, so `agentd acp --rotate` (a new token; the old one stops at once; open connections are not cut) needs no restart. A token file that is a symbolic link, a named pipe or a directory, that others could read, that its owner cannot read, that another user owns or that holds anything but a token is replaced, never followed (an empty directory in its place is removed; one that holds something is an error and nothing is deleted). The file that was opened is the file that is judged, so a rotation that lands during a read is not mistaken for an attack. An `acp` directory that others could enter is tightened to 0700 and the good token in it is kept; one that is a link or another user's is refused (and then no program gets in). When the system fails while the token is read (no descriptors left, an I/O error) the token is not replaced: that request is refused, and the next one reads the file again. The comparison takes the same time whatever the value. The token is never logged (the log has no query string and no header), never in an error, never in `acp.json` and never in the arguments of a process; an agent runs as the user, so it can read the file, as it can read `daemon.json`, which holds the apps' tokens. What can **read** the file can connect: a program on the host, an agent, a container that mounts the home. `?token=` ends up in the configuration of the client that holds the address, and in the `ps` list and the proxy logs on that client's side: use the header where the client supports it.
- **Finding the address.** `agentd acp [<agent>] [--rotate] [--token]` prints, a line each, the ready-made address of every agent that is ready (or the one named): `ws://127.0.0.1:47615/acp/claude?token=…`; `--token` prints the token alone; `--rotate` makes a new token first. The running daemon records `acp/acp.json` beside the token (`{"address":"ws://127.0.0.1:<port>/acp","agents":[...],"updated":"..."}`, never the token) and, when it ends, removes it only if it names its own port (two daemons on one home, such as agentd.app beside a terminal `agentd run`, do not take each other's record away); the record is judged by the file, so one that went away or names another port is put right when the daemon records next, and a missing one is put back within ten seconds; with no record the command assumes the first port and says agentd is not running, and when nothing answers on the recorded port it says so and still prints the address. Without a token (agentd has not run yet) it exits 1. agentd.app and the tray program show the same in their menu (**Other ACP apps**, **Copy ACP address: Claude Code**) and in the Settings window (**Use agentd from other apps**): the daemon sends them the base address and the token on the private channel they share (the state field `acp`, never logged), and they copy the address to the clipboard; the token is never drawn in the window.
- **Limits.** At most 8 connections in all and 4 for one site (`429 busy`, before the upgrade); a client that sends nothing, not even a pong, for 60 s is dropped (the daemon pings every 30 s); a client that leaves a message unread for 30 s is cut loose and its agent ended, and an agent that does not read its input cannot make the daemon hold more than 16 MiB (or 1024 messages) of the client's messages behind the one it is not reading; that one may be as large as the 32 MiB cap, and past the bound the client is closed with `1013` and the agent ended.
- **Not available here.** The page protocol's `open/write/close/mcp` envelope and the tools a page serves to its agent (`/v1/session` has them), and an app server's link (a paired app uses the Socket.IO link, not this).
- **Where the RFD is silent**, agentd chose: the agent in the path (`/acp/<agent>`, since a server hosts several), one trailing newline tolerated, and the close codes above. The RFD requires no `Origin` check and says nothing of authentication beyond that it is layered on top (headers, query parameters or subprotocols); agentd makes the `Origin` check because the endpoint is on a port every web page can reach, and asks a program for the token because the port is also reachable through forwarders.


**The dialog** is native: `osascript` on macOS (the text goes in as `argv`, never inside the script), `zenity` or `kdialog` on Linux. It names the site (the page's origin, from the `Origin` header, never from the body) and the agents that are ready by their titles ("Claude Code and Codex"), and says what Allow gives in the words of a user (never "daemon", "server" or "origin"): an app can start the user's agents at any time, even when none of its pages is open; a browser grant, whenever one of its pages is open; either way the agents run as the user and can read and change any of their files and run any program. An app's access lasts until the user disconnects it; a page's lasts 30 days, then the site has to ask again, and the user can disconnect it sooner. The folders chosen are only where agents start, and the text says so. Deny is the button with the focus when the dialog opens, where the tool can say so: `osascript` (`default button "Deny"`) and `zenity` (`--default-cancel`); `kdialog` documents no option for it, so there the toolkit decides which button has the focus. The dialogs also name the terminal command that disconnects (`agentd apps remove <site>`), as a machine that falls back to them may have no icon in the menu bar; the window does not. 60 s without an answer counts as Deny. After Allow a folder picker opens (several folders on macOS and with `zenity`, one with `kdialog`): the user chooses the folders the origin may start agents in; cancelling it counts as Deny. When agentd.app or the tray program shows the daemon's state, the question is one window of it instead: the same words (without the terminal command), the folders as a list with Add folder… and Remove, Deny and Allow (Allow needs a folder). Deny has the keyboard focus, so Return denies; Escape denies, and so does closing the window. The daemon sends the question to the app on the channel of its state (JSON lines: `consent`, `consent-end`; the app answers `consent-shown`, `consent-answer`) and asks in the native dialogs when no app answers `consent-shown` within 2 s, when it goes away, or when `AGENTD_CONSENT` is set. A machine with neither tool, or without a graphical session, answers `no-dialog`: the daemon never grants without asking. Windows has no dialog implementation yet.

**What Allow gives** is full access to the user's account on this computer. The app's server is the ACP client of its sessions and chooses their folder (`cwd` of `session/new`), the MCP servers the agent starts (any command), the agent's options and the answers to its permission requests (`grok` runs with `--always-approve`). A page is the ACP client of its sessions too, with the answers to their permission requests and their folder, but not the program or the options of the agent: it may list only the session's own tool bridge in `mcpServers` and may set no `_meta` (see Tools from the page). The agents run as the user, with the user's own settings, skills and MCP servers. The folders the user picked in the dialog keep the app's requests inside them: a request that names a folder outside them (`cwd`, `additionalDirectories` or `additionalRoots`, in its params at any depth, `_meta` included, spelled in any case) gets a JSON-RPC error (`-32602`) and never reaches the agent, and agents start in the first folder. A path with `..` in it is never inside, and a line the daemon cannot read as a JSON object with object params is dropped, because the agent's reader and the daemon's must not disagree about where a line points (`daemon/agents/folders.go`). Otherwise the daemon starts the agent the app names and passes the ACP lines through as they are. It is not a sandbox: an agent's shell can reach any file of the user's, and `agentd apps` shows the folders of each grant (`any folder` for one made without a dialog, such as `agentd pair`) and, for a page, when it was granted and when it ends. The access ends with Disconnect in the menu bar, `DELETE /v1/connect` from a page of the origin, `agentd apps remove <origin>`, or, for a page, 30 days after it was granted: a running daemon reads its registry every 2 s and ends the link and page sockets of an origin it no longer lists.

The gateway's checks stop web pages: a browser sets `Host` and `Origin` and page script cannot change them. They also stop the programs of other OS users on this computer: before anything else, the gateway looks up the owner of the connecting socket (`/proc/net/tcp` and `/proc/net/tcp6` on Linux, `lsof` on macOS) and answers only a process of its own user. An owner it cannot find counts as another user, and a lookup that fails is logged once. The lookup runs once per connection; on macOS it adds about 20 ms to the first request of a connection. A program of the same user can still send any `Origin` and use a browser grant of any origin: it already runs as the user, with the same access the grant gives.

**Browsers.** Chrome 142 shows its own "local network" prompt the first time a public page calls loopback ([Chrome](https://developer.chrome.com/blog/local-network-access)); a page that looks for the daemon at load shows it to everyone, so an app should look only when its daemon settings are open. A request to the literal `127.0.0.1` needs no `targetAddressSpace` option. Safari has blocked `http://127.0.0.1` and `ws://127.0.0.1` from https pages ([WebKit 173161](https://bugs.webkit.org/show_bug.cgi?id=173161)); whether current Safari still does is not verified here. A page whose browser blocks the call sees "no daemon", the same as when none runs.

## Tray icon

The daemon starts `app/` (a Go program built with [mygo](https://github.com/egoist/mygo), no cgo) as `agentd-tray child`, its icon in the macOS menu bar or the Windows and Linux notification area; without `child` the same program is agentd.app, which starts the daemon instead (below). The menu shows the version, what is connected (each app and web page with its own Disconnect), the running agents, Settings…, Open log (disabled when the daemon writes to its terminal), Restart (disabled when no service restarts the daemon) and Stop daemon (which says how many agents it ends). Settings… opens the tray's own window (native UI drawn by mygo, no webview; `app/settings.go`): the same status, each connected app and web page with Disconnect, each agent with Ready or the reason it is not, the switch "Install updates automatically", and Open log, Restart and Stop. The daemon feeds the tray state lines on its stdin and reads what the user chose, in the menu or the window, from its stdout (`app/menu.go`, `daemon/ui/state.go`); the tray ends when its stdin does, so a killed daemon takes its icon and window along. A tray that ends at once three times (no display, no notification area) is given up and the daemon runs without an icon.

- **Started by agentd.app.** With `AGENTD_UI_FD=<fd>` (the app hands over one end of a socket pair as fd 3) the daemon starts no tray program: it sends its state lines on that descriptor and takes the user's choices from it, the same JSON lines both ways (`daemon/ui/applink.go`), and it stops, ending its agents, when the app's end closes (`agentd.app closed its channel: stopping`). The app sets `AGENTD_MANAGED=1`, so Restart exits 75 and the app starts it again; it keeps the log (`~/.agentd/logs/daemon.log`), Open log and Quit itself. See `docs/design/desktop-app.md`.
- The program sits at `tray/<os>-<arch>/agentd-tray` (`.exe` on Windows) beside `agentd`; `scripts/build-tray.sh <outdir>` builds darwin arm64 and amd64, linux amd64 and arm64, and windows amd64, and `scripts/build-daemon.sh` packs them (from `$TRAY_DIR`, or built on the spot). The tarball is about 20 MB with them (each program about 9 MB, of which 3 MB is the native UI of the settings window), against 80 KB without.
- `AGENTD_TRAY=off` starts none; `AGENTD_TRAY=<path>` runs that program instead (tests).
- The updates switch applies at once and is saved in `settings.json` (see Files). Off, the daemon stops asking the releases page; a version it already unpacked stays `current` and runs at the next start. It is disabled, and says why, on a daemon that is not installed under `~/.agentd/daemon` or has no releases page: such a daemon never updates.
- Stop from the menu is the same as Stop from an app: the daemon exits 0 and a service leaves it stopped (`agentd service start` starts it again; agentd.app runs the daemon as its child instead, see `docs/design/desktop-app.md`).
- Under systemd the unit gets `DISPLAY`, `WAYLAND_DISPLAY` and `XAUTHORITY` from the shell that ran `service install`, so the tray finds the screen.
- Verified on macOS only, against a real daemon: the menu and its Stop entry; the settings window, its Disconnect, Stop and updates switch (off and on, saved and applied). The Linux and Windows builds compile and are not run.

## Agent adapters

Claude and Codex run through ACP adapters (`@agentclientprotocol/claude-agent-acp` 0.86.0, `codex-acp` 2.1.1; the
versions follow `adapters/package.json`). The daemon installs each one once, in the background, the moment it starts: not
when a chat session opens, so no timeout of a chat can cut an install.

- `npm install --prefix ~/.agentd/adapters/<agent>/.partial-<version>-<pid> --no-audit --no-fund <package>@<version>`,
  then the marker `.agentd-adapter.json` (the executable, read from the installed `package.json`'s `bin`), then one
  rename to `adapters/<agent>/<version>`. A directory with the final name and no marker is a leftover: it is removed and
  installed again. `.partial-*` directories are removed at the next start.
- The agent runs as `node <adapters/<agent>/<version>/node_modules/<package>/<bin>>`. The machine needs `npm` for the
  install only (the installer checks for it).
- `hello.agents` says `ready:false` with `Codex is setting up (first run only, a few minutes).` meanwhile; `open` for
  it answers `Codex is still setting up on this computer.` and starts nothing. When an install finishes or fails the
  daemon sends `agents(list)` to each app. A failure (no network, npm error) puts one plain line in `reason` (`Codex
  could not be set up: <what npm said, one line>. It will try again in 1 minute.`; npm's whole error goes to the log)
  and is retried after 60 s, doubling up to 30 min.
- npm runs in its own process group, and that group is killed on every way out of the daemon (SIGTERM, SIGINT, `stop`,
  `restart`, an update restart, a revoked token, `uncaughtException`, `unhandledRejection`); the partial directory
  it was writing is removed, and a leftover one is removed at the next start. `kill -9` of the daemon cannot run
  any of this: the orphaned npm finishes into its own partial directory, which is never taken for an install.
- `AGENTD_ADAPTERS=off` turns the install off (tests, e2e, debugging): an agent without a `AGENTD_AGENT_<NAME>`
  seam reports `ready:false, reason:"Setting up agents is turned off on this computer."` and npm never runs.
- Older version directories are removed: the current one and the previous one stay.
- Grok is not an adapter: it runs `grok` from PATH.

## Service, restart, update, rollback

- **Exit codes are the protocol with the service** (`home/exit.go`, `launcher.sh`): `0` stays stopped (a Stop from the
  web UI, a SIGTERM from the service manager, a revoked token), `75` starts again now (a Restart from the web UI, an
  update in place; the launcher starts it at once, without the throttle), any other code is a crash and restarts
  after the throttle. `service status` tells "stopped" (last exit 0) from "restarting after a crash".
- **One daemon per home.** `agentd run` takes an exclusive lock on `<home>/run/daemon.lock` before it does anything else
  (before it reads `daemon.json`, starts a link or the gateway, or writes a file of the home). A second daemon on the same
  home finds it held, prints one line, starts nothing, writes nothing and exits **0**:
  `agentd is already running on this computer (pid 4242, started by the app, 3 minutes ago): not starting a second one. Use that one, or quit it first.`
  Two daemons on one home would hold the same apps' tokens and flap against each other for ever (an app's server replaces
  a daemon's first socket when it connects again), and every agent would run twice. The code is 0 and not a new one
  because the exit codes are the protocol with the service manager (above): 0 stays stopped, so launchd, systemd and the
  launcher's loop leave it alone, while any other code would be started again at once and counted as a crash, and two quick
  crashes make the launcher roll a good version back. agentd.app shows "agentd did not start here: another agentd is
  already running on this computer" for an engine that stops with 0 within three seconds of its start. The lock is an
  operating-system lock (`flock`), not a pid file: the system releases it when the process ends in any way, a `kill -9`
  included, so there is never a stale lock to clean up, and it is not inherited by the agents and the tray program the
  daemon starts (close-on-exec), so one that outlives a killed daemon does not keep the next one out. The file also
  holds `{"pid","startedAt","kind"}` (`app`, `service` or `terminal`), written once the lock is held and used only to
  word the message above. A restart through exit 75 finds the lock free: the old process has exited first. A home where
  the lock cannot be made (a file system without locks, a file where `run/` should be) is told in one line and the daemon
  runs without it. Different homes (`AGENTD_HOME`) are independent daemons. `agentd service install` or `start` while
  agentd.app holds the home starts a service engine that says so and exits 0: it is not started again, and
  `service status` reads "stopped". On Windows the lock is a share-nothing open of the file, compile-checked only (the
  daemon is not a Windows product: use WSL).
- **Service.** macOS: LaunchAgent `~/Library/LaunchAgents/com.agentd.daemon.plist` (`RunAtLoad`,
  `KeepAlive = {SuccessfulExit = false}`, `ThrottleInterval` 5 s). Linux: systemd user unit
  `~/.config/systemd/user/agentd.service` (`Restart=on-failure`, `RestartSec=5`). Both set
  `AGENTD_MANAGED=1`, which makes `hello` say `managed: true`; a daemon started with `--foreground` or `run` is
  not managed, ignores a Restart from the server ("not managed: restart ignored") and only honours Stop. Both run `agentd run` and append stdout and stderr to
  `~/.agentd/logs/daemon.log`. `service install` copies `PATH`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and
  `AGENTD_*` of its shell into the definition (a service starts with an empty environment): run it again after you
  change `PATH`. Where neither manager answers (a container, WSL without systemd) `service install` starts the
  launcher's restart loop in the background (growing delay, 1 s to 60 s after a crash; exit 0 ends the loop); it does not
  survive a reboot.
  Linux without linger stops the daemon at logout: `loginctl enable-linger $USER` (printed, never run).
- **Working directory.** launchd would start the daemon in `/`, and agents run with auto-approve in the daemon's
  directory, so the definitions set it: `WorkingDirectory` = your home (plist), `WorkingDirectory=%h` (unit).
  `service install --dir <dir>` (an existing directory) uses that one instead; the daemon logs it at start
  (`serving agents in <dir>`).
- **Crash.** A panic ends the process with Go's exit code 2 and its stack in the log; the service manager (or the loop)
  starts a new one, and the next start ends the agent groups the crash left behind (`sessions.json`, see Files).
- **Control from the server.** `restart` and `stop` end every agent session (SIGTERM to each process group, SIGKILL after
  2 s; their `exit` frames go out first), close the socket and exit 75 or 0; no `open` is accepted meanwhile. A token the
  server refuses (revoked) exits 0 and is not retried; network trouble is retried inside the process. After a Stop,
  only `agentd service start` on the machine starts it again.
- **Log.** `daemon.log` is rotated at 5 MiB, 3 files kept (`.1`, `.2`). It is copied and truncated in place, because
  the service manager holds the file open: a few lines written during the copy can be lost.
- **Update.** Unless "Install updates automatically" is off in the tray's settings window, at start and every six hours the daemon asks the releases page (`AGENTD_RELEASE_URL`, else the `RELEASE_URL` baked in
  at build time, `https://github.com/nguyenvanduocit/agentd/releases` for a release; empty switches updates off) for
  `<releases>/latest/download/latest.json`; GitHub redirects that, and the tarball, to its storage host, which the daemon follows
  (https only; plain http only inside one origin, for the local stand-in). A different version must be signed (below); it
  is then downloaded from `<releases><url>` (`/download/v<version>/agentd-<version>.tgz`), checked against the sha256, unpacked into `~/.agentd/daemon/<version>` (the engine of this machine's platform becomes `agentd`; the tarball's other engines are dropped), and `current` is renamed
  onto it (atomic). When no session is running (checked every 5 s; a turn in progress is never cut) the daemon exits with
  code 75 and the launcher starts it again at once on the new version. A failed download, a wrong
  sha256 or a bad tarball changes nothing and is retried from 30 s up to 30 min; a bad signature changes nothing and is
  not retried (it is logged again at every check). Only a daemon installed under
  `~/.agentd/daemon/<version>/` updates itself; a source run or a copy outside that directory never does. The releases page,
  not an app, is the update source: a daemon that serves several apps updates the same way whichever of them leave.
- **Signed updates.** `scripts/build-daemon.sh` bakes the release public key set into every engine (`-X daemon/home.ReleaseKey`:
  the base64 of the SPKI DER of each key, separated by commas; `daemon/release.pub` and, when the file exists,
  `daemon/release-backup.pub`) and writes `signature` into `latest.json` (`cmd/sign-daemon`): the
  Ed25519 signature of `"agentd\n<version>\n<sha256>\n"`.
  Before it downloads anything (and for a version already on disk too) the daemon checks it against that set
  (`daemon/update`, `SignatureProblem`): a signature of any key of the set is good. A missing or wrong signature, or an
  engine without a key, refuses the update and logs `cannot update to <version>: refused: <why>`. Nothing turns the check
  off. Limits: signing covers the update channel only (an app's server still drives agents in auto-approve mode); an
  offer older than the running x.y.z is refused (a replayed old manifest cannot roll a daemon back), but a daemon can be
  kept on its version by serving it the same manifest for ever (the signature has no expiry). The backup key is kept
  offline: a lost or leaked primary key is replaced by a release signed with the backup key, which every daemon that
  carries the set takes; a daemon installed before the first release that bakes the set trusts only the primary key, and
  a lost primary key strands it (reinstall). There is no revocation list. The private key lives on the maintainer's
  machine and in the secrets of the GitHub environment `release`, which only the manually started release workflow
  uses (see Release in the root README): whoever can edit that workflow on `main` or read those secrets can sign an
  update every installed daemon takes. The backup key is not in GitHub.
- **Rollback.** The launcher keeps `daemon/previous`. A version that exits non-zero within 30 s of its start, twice in a
  row (`daemon/crashes`), is rolled back: `current` points at `previous`, the bad version is written to
  `daemon/rejected`, and the daemon does not update to that version again until the releases page offers a different one.

## Files

- `~/.agentd` (`$AGENTD_HOME`): `node/` (a downloaded Node.js), `daemon/<version>/`, `daemon/current`,
  `daemon/previous`, `daemon/rejected`, `daemon/crashes`, `bin/agentd`, `logs/daemon.log`, `run/loop.pid`, `run/daemon.lock` (the lock of the one daemon that runs on this home, with who holds it), `acp/token` (the
  token of the standard ACP endpoint, mode 0600 in a directory of 0700) and `acp/acp.json` (where the endpoint is; present while the daemon runs).
  Remove it all with `agentd service uninstall && rm -rf ~/.agentd`.
- Config `{apps:[{server, token}], browsers:[{origin, grantedAt}]}`, mode 0600, written through a temporary file and a rename: `$XDG_CONFIG_HOME` or `~/.config` + `/agentd/daemon.json` (Linux, macOS);
  `%APPDATA%\agentd\daemon.json` (Windows). `pair` and the gateway write it. A daemon with nothing saved runs and waits for an app to ask. A page's grant lapses 30 days after its `grantedAt` (the form `2026-10-19T00:00:00.000Z`; one that cannot be read as a time counts as lapsed); the entry stays in the file until the page is allowed again or `agentd apps remove` forgets it, and `agentd apps` lists it as expired. A running daemon ends the sockets of a lapsed page within 2 s, as it does for a removed one.
- Settings `{updates}` in `settings.json` beside the config file (`--config` moves both), written by the tray's settings window through a temporary file and a rename. Read at start; a missing file, or one that is not JSON (said in the log), is the defaults: updates on.
- Orphan record `sessions.json`: `$XDG_STATE_HOME` or `~/.local/state` + `/agentd/` (Windows: `%LOCALAPPDATA%`; not kept there).
  It lists the process groups of running agents with the start time of each leader. After `kill -9` of the daemon,
  the next start ends the groups that are still running.
- A temporary directory (mode 0700) with the `agentd-mcp` wrappers, one per app (`bin/<n>`) and one for pages (`bin/page`), first on the `PATH` of their agents, and the pages' bridge socket `mcp.sock`; removed on exit.

## Design in one line per block

- One socket on the `/agentd` namespace per app; every stdout line, stderr chunk and exit of every agent travels in one ordered
  `frame` event. The contract is `contract/define.acp.ts`; `../contract/server.generated.go` is generated from it and the daemon's
  client (`link/`) is written against its types, so a renamed method fails the build.
- A dropped socket ends every agent (SIGTERM, then SIGKILL after 2 s, to the whole process group) and frames of the
  old connection are never sent after the reconnect. Reconnects back off from 0.5 s to 15 s and send `hello` each time.
- A token the server refuses (`unauthorized`: unknown or revoked) detaches that app with no retry (pair again, then
  `service start`); the daemon exits 0 when it was the last app, no page grant is live and no program is connected over `/acp` (one that is keeps it up, and the log says so). A server that could not
  check the token now (`unavailable`, its token store failed) is retried with the reconnect backoff.
- `launcher.sh` (shell, linted by shellcheck) is the launcher; it ships beside the engines in the tarball and the
  daemon refreshes `~/.agentd/bin/agentd` from it after an update.
- The server names an agent (`claude`, `codex`, `grok`); the command table lives in `agents/agents.go`
  (adapter versions follow `adapters/package.json`). The ACP lines that follow are the app's: see "What Allow gives".
- The packages that decide (`cli`, `registry`, the `core` files of each) are pure functions of their inputs; `run`, `link`,
  `gateway`, `proc` and the other shells do the I/O.

## Test seam

`AGENTD_AGENT_<NAME>` (for example `AGENTD_AGENT_CLAUDE="/path/fake-agent --flag"`) replaces the command line
of that agent and counts it as ready. Tests and the e2e suite use it to run a fake agent. It is not a user feature.
Other seams, for tests only: `AGENTD_HOME` (replaces `~/.agentd`), `AGENTD_SERVICE_LABEL` and
`AGENTD_SERVICE_DIR` (name and directory of the service definition, so a test never touches the user's service),
`AGENTD_SERVICE_MANAGER=loop|launchd|systemd` (skips detection), `AGENTD_QUICK_SECONDS` (the launcher's 30 s),
`AGENTD_NODE_DIST` (the install script's Node.js mirror), `AGENTD_ADAPTERS=off` (no adapter install), `AGENTD_LOCAL_PORT` (the gateway's first port, `0` for any), `AGENTD_CONSENT=<program>` (runs in place of the dialog with the title and the text as arguments; exit 0 with one absolute folder per line on stdout is Allow for those folders, exit 0 without a folder is refused as `no-folder`, any other exit is Deny, a program that cannot be started is `no-dialog`; never copied into a service definition), `AGENTD_TRAY` (`off` or a program), `AGENTD_ADAPTER_SPEC_<NAME>` (the package spec the
adapter install gets, for example `file:/path/fake-adapter.tgz`; the install directory is named after a hash of it).

## Develop

```
go vet ./daemon/... ./cmd/... && go test ./daemon/... ./cmd/... ./scripts/pipeline/   # unit + integration against real processes (about 3 min)
go test -short ./daemon/...                                                          # skips the tests that build a release
RUN_REAL_CLAUDE=1 go test -run Real ./daemon/run/                                    # two short turns with the real Claude adapter (costs money)
(cd app && go vet ./... && go test ./...)                                            # the tray's menu model and the app's engine supervision
(cd sdk/web && bun test)                                                          # the SDK pages use
scripts/build-daemon.sh /tmp/dist   # download/v<version>/{agentd-<version>.tgz (agentd-<os>-<arch> x4, launcher.sh, package.json, tray/), latest.json (signed), install.sh}; dev key in .dev-signing/ (made on first use)
```

The integration tests build `cmd/agentd` and `cmd/fake-agent` once per run into a temporary directory and run each daemon as a
process with its own `AGENTD_HOME` and `HOME`. The app server they talk to is the `sdk/goserver/` library or the contract server in
`daemon/run/app_test.go`; installs and updates come from the release stand-in `cmd/agentd-server`, started in process by `scripts/pipeline`.
