# Multi-app daemon (design, written inside MindChains and moved with the daemon)

One daemon on a user's machine serves any number of apps. An app's web UI finds the daemon, asks to connect, and the daemon asks the user in a native OS dialog. The daemon shows an icon in the system menu.

## Goal

A user installs the daemon once. Every app that speaks the daemon protocol (`daemonrpc/define.acp.ts`) can detect it from its UI and request a connection; the user decides in a native dialog, per app, and sees and controls the daemon from the menu bar or notification area.

A browser app with no server of its own gives its agent tools that live in the page (navigate, search, read the current page): the page serves MCP over its session socket, and the agent reaches it through `agentd-mcp`.

## Non-goals

- Renaming the daemon, its home directory or its service labels.
- A reusable server library for other repos (`internal/daemonhub` imports `internal/agent`, `internal/store` and `rpc`).
- Scoping a grant to agents or folders: a grant is full access to the user's account (the session's ACP client chooses its folder, MCP servers and permission answers; agents run as the user). Confining an app would take an OS sandbox around the agents.
- An app whose API lives on another origin than its page: the app connected is the one at the page's own origin. Supporting a separate API origin needs a way for that origin to vouch for page origins (for example a file the daemon fetches from it).
- The daemon running natively on Windows (installer, service, dialog). The tray program is built for Windows; the daemon around it is a separate project.

Anti-goal: an origin that was never allowed running an agent; a page learning anything beyond "a daemon is here" and its own grants; a page seeing or changing the app of another origin; a page pointing the daemon at a server that is not its own origin; a bridge reaching a page other than the one whose session started it.

## Parts

### 1. Local gateway (`daemon/gateway/gateway.go`, pure parts in `daemon/gateway/local.go`)

HTTP and WebSocket on `127.0.0.1`: port 47615, the next free one up to 47624; `AGENTD_LOCAL_PORT` replaces the first (`0` = any). Every request is checked in this order: `Host` is `127.0.0.1:<port>` or `localhost:<port>` (else 421); `Origin` is present, https, or http on a loopback host (else 403); CORS reflects the origin (`Vary: Origin`, `Access-Control-Allow-Private-Network` when asked).

| Request | Answer |
|---------|--------|
| `GET /v1/status` | `{name:"agentd", version, granted:{server,browser}}`; `agents` only for a granted origin. `granted.server` means linked now. |
| `POST /v1/connect {mode:"server", code}` | Dialog. On Allow: pairs with the server at the caller's origin using `code`, saves, links at once (an app already linked is linked again with the new token). A body that names a server is `400`. |
| `POST /v1/connect {mode:"browser"}` | Dialog (none when already granted). On Allow: saves the origin as a browser grant. |
| `DELETE /v1/connect` | Forgets the caller's browser grant and the app at its origin; ends their sessions and the page's open sockets; the daemon stays up. |
| `GET /v1/session` (WebSocket) | Browser grants only. |

Refusals: `403 denied|timeout`, `429 cooldown` (30 s per origin, and a lockout of every dialog after three refusals from any origins within ten minutes), `409 busy` (one dialog at a time), `503 no-dialog` (fail closed), `400 malformed`, `502 pairing`. All ten ports taken: the daemon logs `local gateway off` and serves its apps.

### 2. Native dialog (`daemon/consent/ask.go`, `daemon/consent/text.go`)

`osascript` (text as `argv`, never in the script) on macOS; `zenity`, else `kdialog` on Linux. Deny is the default and cancel button; 60 s is Deny. The text names the origin from the `Origin` header and the mode: a server can start the ready agents at any time, even with no page open; a browser grant, whenever a page of the origin is open. It says that the agents run as the user and can read and change any of their files and run any program, until the user disconnects it. No tool or no graphical session: `no-dialog`. `AGENTD_CONSENT=<program>` replaces the dialog in tests (exit 0 = Allow); it is never copied into a service definition.

### 3. Browser sessions (`daemon/gateway/sessions.go`, `daemon/pagemcp/localproto.go`)

One socket is one fleet, ended when the socket closes; at most 5 sessions per socket. Page to daemon: `open`, `write`, `close`, `mcp`. Daemon to page: `hello`, `opened`, `error`, `frame`, `mcp`. The page is the ACP client.

### 3a. Tools from the page (`daemon/pagemcp/relay.go`, `daemon/pagemcp/bridge.go`, `mcp --page` in `daemon/bridge/page.go`)

A browser grant has no server and no token, so `agentd-mcp --session` has nowhere to go; the page serves the tools itself. On `open` the daemon mints a token (256 random bits) bound to that page socket and session id, and `opened.mcp` names the stdio server with the socket path and the token in `env` (process arguments are readable by other OS users): the pages' own wrapper `bin/page/agentd-mcp` (`mcp --page`) by absolute path, as the ACP schema asks for `McpServerStdio.command`, so it does not depend on the `PATH` the agent gives its MCP servers. The bridge connects to the daemon's Unix socket `<tmp>/agentd-XXXXXX/mcp.sock` (the directory is mode 0700: no other OS user reaches it, and no TCP port opens), sends the token, gets `{ok:true}` or `{ok:false, message}`, then relays lines: stdin to the page as `{t:"mcp", id, line}`, the page's `{t:"mcp", id, line}` to stdout. The token dies with the session or the page socket, which closes the bridge; a newer bridge with the same token replaces the older. Lines keep the 32 MiB cap of `LineSplitter`, and lines to the page pause, as frames do, while the page's socket is behind. Claude's ACP adapter starts the stdio MCP servers of `session/new` and does not advertise MCP over ACP, so a stdio bridge is the route. The SDK gives the page `session.mcp` (its end, as ndjson byte streams) and `session.mcpServer(name)` (the entry for `mcpServers`).

### 4. Apps registry (`daemon/registry/config.go`, `daemon/registry/registry.go`)

`daemon.json`: `{apps:[{server,token}], browsers:[{origin,grantedAt}]}`, mode 0600, written through a temporary file and a rename. `run` starts with nothing saved. `--app` serves one saved app. `agentd apps [list]` and `apps remove <origin>`; a running daemon reads the file every 2 s and ends the link and page sockets of an origin that is no longer in it.

### 5. One link and one fleet per app (`daemon/link/apps.go`)

Each app has its own socket, fleet and MCP wrapper (`bin/<n>/agentd-mcp --config <file> --app <origin>`). Updates come from the release server (see `agentd-extraction.md`), not from an app. Idle (updates) means no agent runs for any app or page. Stop from an app detaches it; the daemon exits 0 when its last app leaves and no browser grant exists. A refused token (`unauthorized`) detaches that app; a server that could not check the token (`unavailable`) is retried. Restart from any app restarts the daemon (a managed daemon only).

### 6. Client library (`daemon-client/`)

`detect`, `connectServer(d, {code})`, `connectBrowser`, `disconnect`, `openSession(d, agent)`, `reasonText`. A page imports it by path.

### 7. MindChains UI (`frontend/src/composables/createLocalDaemon.ts`, `frontend/src/components/DaemonLocal.vue`)

Settings > Daemon detects when it opens and when the window gets focus, never at page load (Chrome's local-network prompt). A daemon that is here and not connected is offered with "Connect this computer"; a refusal shows its reason in plain words and cancels the pending pairing; a look while a connection waits for its dialog changes nothing.

### 8. Tray icon (`tray/`, `daemon/tray/tray.go`, `daemon/tray/menu.go`, `daemon/tray/state.go`)

A Go program (own module, mygo, no cgo) the daemon starts. It reads state lines on stdin and prints the entry the user chose on stdout; it ends with its stdin. Menu: version, status line, each app and web page with Disconnect, Settings…, Open log (disabled when the daemon writes to its terminal), Restart (disabled when not managed), Stop daemon (says how many agents it ends). Settings… opens the tray's own native window (mygo `ui`, no webview; `tray/settings.go`): the same status, Disconnect per origin, each agent with Ready or its reason, the switch "Install updates automatically" (saved in `settings.json` beside the registry, applied at once: off ends the release checks; disabled on a daemon that cannot update), and the controls. The daemon pushes a state when it changes (checked every 2 s). A tray that ends quickly three times is given up. `AGENTD_TRAY=off|<path>`.

Packaging: `scripts/build-tray.sh` builds darwin arm64/amd64, linux amd64/arm64, windows amd64; `scripts/build-daemon.sh` packs them as `tray/<os>-<arch>/` from `$TRAY_DIR` or builds them when Go is installed. The tarball grows from 80 KB to about 20 MB (measured 20,199,035 bytes; the window's native UI adds about 3 MB per tray program). The Dockerfile has a `tray` stage.

## Constraints

- Loopback only; no other interface ever binds.
- A page cannot authenticate the daemon it finds: whatever answers first on 47615-47624 with `name:"agentd"` gets the page's pairing code or its browser session. Apps show every paired daemon in their daemons list so an unknown one is noticed.
- Chrome 142 prompts on the first public-to-loopback request; Safari has blocked `http://127.0.0.1` from https pages (WebKit 173161; current behaviour unverified here).
- `daemonrpc/define.acp.ts` and the Go server are unchanged.

## Assumptions (mine, not confirmed by the user)

- Browser-only apps get chat; page-side tools wait for a decision.
- The cooldown (30 s), the dialog timeout (60 s), the port range, the 5 sessions per socket and the 2 s tray refresh are defaults chosen here.
- The product keeps the name `agentd`.
- Every platform's tray ships in every tarball (one download, one signature) at the cost of its size.

## Decisions of the user

- The tray is a Go program built with mygo, for macOS, Linux and Windows; the daemon on Windows comes later (14:03).
- Verification runs natively on the user's machine, never in Docker.
- A grant is full access to the user's account, said plainly in the dialog and per mode (decided by the agentd session the user handed it to, 2026-10-19): agents have a shell and the ACP client answers their permission requests, so pinning a folder in `session/new` would confine nothing without an OS sandbox.

## Acceptance and evidence

1. An origin never allowed gets `granted:false`, no agent list and no session. `daemon/run/gateway_test.go`, `daemon/run/browser_test.go`.
2. A foreign `Host`, a missing or `null` `Origin`, or plain http on a non-loopback origin is refused before any dialog. `daemon/gateway/gateway_test.go`.
3. Allow pairs, saves and links without a restart; Deny saves nothing and the next request inside 30 s is `cooldown`. `daemon/gateway/gateway_test.go`; in the real app `e2e/daemon-local.mjs` (15 checks).
4. Two apps connect at once, each reaches only its own agents, Stop from one leaves the other. `daemon/run/multiapp_test.go`, `daemon/bridge/hub_test.go`.
5. A browser grant opens a WebSocket session; the agent ends with the socket. `daemon/run/browser_test.go`.
6. The real macOS dialog opened and gave `timeout` after its 3 s limit (`osascript` path). Allow and Deny clicks are covered through the `AGENTD_CONSENT` seam; a person clicking them is not.
7. The tray: against a real daemon on macOS, the menu lists the state and Stop daemon exits the daemon with 0 and ends the tray. Linux and Windows: built, not run.

## Review of the branch

An independent review (Opus) found eleven issues; the fixes are in this branch, each with a test that failed first (the one for shutdown with an open page socket was watched failing by restoring the old order). Fixed: a page's socket and agents surviving its revocation; any origin seeing or removing any app through `?server=`; a cooldown that new subdomains escape (now a lockout across origins) and a busy flag that blocked disconnect; re-pairing an app whose token swapped without relinking; the gateway closing before page sockets (a hang at Stop); the dialog naming a command that might not apply; a detached app reading as granted; updates ending when the first app left; lost updates in concurrent registry writes. Left as it is: the 5-session limit counts per socket, not per origin (a granted page already holds full agent access).
