# agentd

Runs your coding agents (Claude, Codex, Grok) on your own machine for the apps you allow. An app's server is the ACP
client; the daemon starts the agent process, relays its stdio lines over a socket, and runs the agent with your MCP
servers, skills and folders. It serves any number of apps (each one dials in from the daemon's side) and shows an icon in
the menu bar. Its only listening port is a gateway on `127.0.0.1`, which web pages use to find it and to ask for a
connection. Node.js >= 20.

## Install

An app's web page shows one command; it installs, pairs and starts the daemon as a service of your user:

```
curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash -s -- --app <app origin> --code <code>
```

`install.sh` is a file of agentd's latest GitHub release; `<app origin>` is the app that gave you the command and `<code>` its
one-time pairing code.

macOS (arm64, x64) and Linux (x64, arm64, glibc). It needs `curl`, `tar`, `bash` and `shasum` or `sha256sum`, no
sudo, and writes only under your home directory. Windows: use WSL. The script checks the daemon tarball against the
sha256 baked into it when the release was built, uses the machine's Node.js >= 20 (with `npm`), or else downloads the pinned LTS
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

`~/.agentd/bin/agentd` is the launcher; it runs `node ~/.agentd/daemon/current/daemon.js`.

```
agentd pair --app <origin> --code <code>      pair this machine with an app
agentd [run] [--app <origin>] [--dir <dir>]    connect and serve agents
agentd apps [list]                             the apps and web pages this machine is connected to
agentd apps remove <origin>                    disconnect one of them (a running daemon ends its sessions within 2 s)
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

`mcp --session <id>` (an app's session) and `mcp --page` (a page's session, with `AGENTD_MCP_SOCKET` and `AGENTD_MCP_TOKEN` in its environment) are the MCP stdio bridges that agents start through `agentd-mcp`; they are not meant to be run by hand.

## Apps, web pages and the gateway

One daemon serves many apps. An app is a server that implements `contract/define.acp.ts` (with the `hub/` library, or by hand; MindChains is one): the daemon pairs with it, dials out to it and runs agents for it, with a socket, a set of agents and an MCP wrapper (`bin/<n>/agentd-mcp`) of its own per app, so sessions never mix. Updates do not come from an app but from the release server (see Update).

**The gateway** is an HTTP and WebSocket server on `127.0.0.1`, port 47615 or the next free one up to 47624 (`AGENTD_LOCAL_PORT` replaces the first port; `0` means any free port). The daemon logs `local gateway on 127.0.0.1:<port>`, or `local gateway off` when all ten are taken (paired apps keep working). Every request is checked in order: the connection comes from a process of the daemon's own OS user (else `403 {granted:false, reason:"peer"}`, and a WebSocket upgrade is closed); `Host` is `127.0.0.1:<port>` or `localhost:<port>` (else 421, DNS rebinding); `Origin` is present and is `https`, or `http` on a loopback host (else 403; the browser sets it, page script cannot forge it). The origin is reflected in `Access-Control-Allow-Origin`.

| Request | Answer |
|---------|--------|
| `GET /v1/status` | `{name:"agentd", version, granted:{server, browser}}`; `agents` only for an origin that is granted. `granted.server` is true while the daemon is linked to the app at the caller's origin (an app that Stop or a refused token detached is saved but not linked, and reads false so the page can offer to connect it again). |
| `POST /v1/connect {mode:"server", code}` | Shows the dialog. On Allow: pairs with the server **at the caller's origin** using `code`, saves the app and links to it at once (pairing an app that is already linked replaces its token and links again). A body that names a server is `400`: a page cannot point the daemon at an origin that is not its own. `200 {granted:true}` |
| `POST /v1/connect {mode:"browser"}` | Shows the dialog. On Allow: saves the origin as a browser grant (it may then open sessions). A granted origin gets `200` without a dialog. |
| `DELETE /v1/connect` | Forgets the caller's browser grant and the app at its origin, and ends their sessions (the page's open `/v1/session` sockets close with their agents). Never touches another origin's app. The daemon stays up so the page can connect again. |
| `GET /v1/session` (WebSocket) | Browser-granted origins only. One socket is one set of agents, ended with the socket; at most 5 sessions per socket. Messages are in `src/core/localproto.ts`: the page sends `open`, `write`, `close`, `mcp`; the daemon sends `hello`, `opened`, `error`, `frame`, `mcp`. The page is the ACP client of the lines, and may serve the agent MCP tools (below). |

**Tools from the page.** A page's agent can call tools the page itself serves. `opened` carries `mcp: {command:"<tmp>/agentd-XXXXXX/bin/page/agentd-mcp", args:[], env:[{name:"AGENTD_MCP_SOCKET", value}, {name:"AGENTD_MCP_TOKEN", value}]}`, the stdio MCP server the page lists (with a name) in `session/new`'s `mcpServers`. The command is the pages' own wrapper (it runs `mcp --page`) by absolute path, as the ACP schema asks, so it starts whatever `PATH` the agent gives its MCP servers (verified with Claude's adapter 0.86: `test/integration/pagetools.real.test.ts`, run with `RUN_REAL_CLAUDE=1`); it sends the token to the daemon's Unix socket `<tmp>/agentd-XXXXXX/mcp.sock`, then relays each line of the agent's MCP client to the page as `{t:"mcp", id, line}`, and each `{t:"mcp", id, line}` of the page back to the agent. The socket's directory is mode 0700, so other OS users cannot reach it; the token (256 random bits, one per session) is in the environment, not the arguments, which other users can read with `ps`. It opens no port. The token is bound to the page's socket and the session, and dies with either, which ends the bridge (it exits 1); a token the daemon does not know is refused and the bridge exits 1 with the reason. If the agent starts the server again, the newer bridge replaces the older. A page `mcp` while no bridge is connected is an `error` for that session. Lines keep the 32 MiB cap, and the bridge's lines to a page wait, as the agent's output does, while the page's socket is behind. A daemon that cannot listen on the socket (a temporary path too long for one) says so in its log and sends no `mcp`.

Refusals: `403 {granted:false, reason:"peer"}` (the caller's process belongs to another OS user, or its owner cannot be found), `403 {granted:false, reason:"denied"|"timeout"}`, `429 cooldown` (30 s after a deny or timeout, per origin; and three refusals within ten minutes from any origins lock every dialog until the oldest is ten minutes old, so new subdomains do not buy new dialogs; an Allow clears the count), `409 busy` (one dialog at a time), `503 no-dialog` (the machine cannot ask: nothing is granted), `400 malformed`, `502 pairing` (the app refused the code). `sdk/client/` (`@agentd/client`) is the client for pages.

**The dialog** is native: `osascript` on macOS (the text goes in as `argv`, never inside the script), `zenity` or `kdialog` on Linux. It names the page's origin (from the `Origin` header, never from the body) and says what Allow gives, in the words of the next paragraph: a server can start the user's agents at any time, even when none of its pages is open; a browser grant, whenever one of its pages is open; either way the agents run as the user and can read and change any of their files and run any program, until the user disconnects it. Deny is the default button. 60 s without an answer counts as Deny. A machine with neither tool, or without a graphical session, answers `no-dialog`: the daemon never grants without asking. Windows has no dialog implementation yet.

**What Allow gives** is full access to the user's account on this computer. Whoever drives a session (the app's server, or the origin's pages) is its ACP client and chooses its folder (`cwd` of `session/new`), the MCP servers the agent starts (any command), the agent's options and the answers to its permission requests (`grok` runs with `--always-approve`). The agents run as the user, with the user's own settings, skills and MCP servers. The daemon narrows none of that: it starts the agent the app names and passes the ACP lines through as they are. Keeping an app to one folder would take an OS sandbox around the agents. The access ends with Disconnect in the menu bar, `DELETE /v1/connect` from a page of the origin, or `agentd apps remove <origin>`: a running daemon reads its registry every 2 s and ends the link and page sockets of an origin it no longer lists.

The gateway's checks stop web pages: a browser sets `Host` and `Origin` and page script cannot change them. They also stop the programs of other OS users on this computer: before anything else, the gateway looks up the owner of the connecting socket (`/proc/net/tcp` and `/proc/net/tcp6` on Linux, `lsof` on macOS) and answers only a process of its own user. An owner it cannot find counts as another user, and a lookup that fails is logged once. The lookup runs once per connection; on macOS it adds about 20 ms to the first request of a connection. A program of the same user can still send any `Origin` and use a browser grant of any origin: it already runs as the user, with the same access the grant gives.

**Browsers.** Chrome 142 shows its own "local network" prompt the first time a public page calls loopback ([Chrome](https://developer.chrome.com/blog/local-network-access)); a page that looks for the daemon at load shows it to everyone, so an app should look only when its daemon settings are open. A request to the literal `127.0.0.1` needs no `targetAddressSpace` option. Safari has blocked `http://127.0.0.1` and `ws://127.0.0.1` from https pages ([WebKit 173161](https://bugs.webkit.org/show_bug.cgi?id=173161)); whether current Safari still does is not verified here. A page whose browser blocks the call sees "no daemon", the same as when none runs.

## Tray icon

The daemon starts `tray/` (a Go program built with [mygo](https://github.com/egoist/mygo), no cgo) as `agentd-tray child`, its icon in the macOS menu bar or the Windows and Linux notification area; without `child` the same program is agentd.app, which starts the daemon instead (below). The menu shows the version, what is connected (each app and web page with its own Disconnect), the running agents, Settings…, Open log (disabled when the daemon writes to its terminal), Restart (disabled when no service restarts the daemon) and Stop daemon (which says how many agents it ends). Settings… opens the tray's own window (native UI drawn by mygo, no webview; `tray/settings.go`): the same status, each connected app and web page with Disconnect, each agent with Ready or the reason it is not, the switch "Install updates automatically", and Open log, Restart and Stop. The daemon feeds the tray state lines on its stdin and reads what the user chose, in the menu or the window, from its stdout (`tray/menu.go`, `src/core/tray.ts`); the tray ends when its stdin does, so a killed daemon takes its icon and window along. A tray that ends at once three times (no display, no notification area) is given up and the daemon runs without an icon.

- **Started by agentd.app.** With `AGENTD_UI_FD=<fd>` (the app hands over one end of a socket pair as fd 3) the daemon starts no tray program: it sends its state lines on that descriptor and takes the user's choices from it, the same JSON lines both ways (`src/applink.ts`), and it stops, ending its agents, when the app's end closes (`agentd.app closed its channel: stopping`). The app sets `AGENTD_MANAGED=1`, so Restart exits 75 and the app starts it again; it keeps the log (`~/.agentd/logs/daemon.log`), Open log and Quit itself. See `docs/design/desktop-app.md`.
- The program sits at `tray/<os>-<arch>/agentd-tray` (`.exe` on Windows) beside `daemon.js`; `scripts/build-tray.sh <outdir>` builds darwin arm64 and amd64, linux amd64 and arm64, and windows amd64, and `scripts/build-daemon.sh` packs them (from `$TRAY_DIR`, or built on the spot when Go is installed). The tarball is about 20 MB with them (each program about 9 MB, of which 3 MB is the native UI of the settings window), against 80 KB without.
- `AGENTD_TRAY=off` starts none; `AGENTD_TRAY=<path>` runs that program instead (tests).
- The updates switch applies at once and is saved in `settings.json` (see Files). Off, the daemon stops asking the releases page; a version it already unpacked stays `current` and runs at the next start. It is disabled, and says why, on a daemon that is not installed under `~/.agentd/daemon` or has no releases page: such a daemon never updates.
- Stop from the menu is the same as Stop from an app: the daemon exits 0 and a service leaves it stopped (`agentd service start` starts it again; agentd.app runs the daemon as its child instead, see `docs/design/desktop-app.md`).
- Under systemd the unit gets `DISPLAY`, `WAYLAND_DISPLAY` and `XAUTHORITY` from the shell that ran `service install`, so the tray finds the screen.
- Verified on macOS only, against a real daemon: the menu and its Stop entry; the settings window, its Disconnect, Stop and updates switch (off and on, saved and applied). The Linux and Windows builds compile and are not run.

## Agent adapters

Claude and Codex run through ACP adapters (`@agentclientprotocol/claude-agent-acp` 0.86.0, `codex-acp` 2.1.1; the
versions follow `agent/package.json`). The daemon installs each one once, in the background, the moment it starts: not
when a chat session opens, so no timeout of a chat can cut an install.

- `npm install --prefix ~/.agentd/adapters/<agent>/.partial-<version>-<pid> --no-audit --no-fund <package>@<version>`,
  then the marker `.agentd-adapter.json` (the executable, read from the installed `package.json`'s `bin`), then one
  rename to `adapters/<agent>/<version>`. A directory with the final name and no marker is a leftover: it is removed and
  installed again. `.partial-*` directories are removed at the next start.
- The agent runs as `node <adapters/<agent>/<version>/node_modules/<package>/<bin>>`. The machine needs `npm` for the
  install only (the installer checks for it).
- `hello.agents` says `ready:false` with `installing the Codex adapter (first run only, this can take several minutes)`
  meanwhile; `open` for it answers `the codex adapter is still installing on this machine` and starts nothing. When an
  install finishes or fails the daemon sends `agents(list)` to each app. A failure (no network, npm error) puts the tail
  of npm's stderr in `reason` and is retried after 60 s, doubling up to 30 min.
- npm runs in its own process group, and that group is killed on every way out of the daemon (SIGTERM, SIGINT, `stop`,
  `restart`, an update restart, a revoked token, `uncaughtException`, `unhandledRejection`); the partial directory
  it was writing is removed, and a leftover one is removed at the next start. `kill -9` of the daemon cannot run
  any of this: the orphaned npm finishes into its own partial directory, which is never taken for an install.
- `AGENTD_ADAPTERS=off` turns the install off (tests, e2e, debugging): an agent without a `AGENTD_AGENT_<NAME>`
  seam reports `ready:false, reason:"adapter install is disabled"` and npm never runs.
- Older version directories are removed: the current one and the previous one stay.
- Grok is not an adapter: it runs `grok` from PATH.

## Service, restart, update, rollback

- **Exit codes are the protocol with the service** (`src/core/exit.ts`, `launcher.sh`): `0` stays stopped (a Stop from the
  web UI, a SIGTERM from the service manager, a revoked token), `75` starts again now (a Restart from the web UI, an
  update in place; the launcher starts it at once, without the throttle), any other code is a crash and restarts
  after the throttle. `service status` tells "stopped" (last exit 0) from "restarting after a crash".
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
- **Crash.** `uncaughtException` and `unhandledRejection` are logged and end the process with exit code 1; the service
  manager (or the loop) starts a new one.
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
  is then downloaded from `<releases><url>` (`/download/v<version>/agentd-<version>.tgz`), checked against the sha256, unpacked into `~/.agentd/daemon/<version>`, and `current` is renamed
  onto it (atomic). When no session is running (checked every 5 s; a turn in progress is never cut) the daemon exits with
  code 75 and the launcher starts it again at once on the new version. A failed download, a wrong
  sha256 or a bad tarball changes nothing and is retried from 30 s up to 30 min; a bad signature changes nothing and is
  not retried (it is logged again at every check). Only a daemon installed under
  `~/.agentd/daemon/<version>/` updates itself; a source run, a test bundle or an npx copy never does. The releases page,
  not an app, is the update source: a daemon that serves several apps updates the same way whichever of them leave.
- **Signed updates.** `scripts/build-daemon.sh` bakes the release public key into the bundle (`AGENTD_RELEASE_KEY`,
  base64 of the SPKI DER; `../daemon/release.pub` for releases) and writes `signature` into `latest.json`: the
  Ed25519 signature of `"agentd\n<version>\n<sha256>\n"`.
  Before it downloads anything (and for a version already on disk too) the daemon checks it with that key
  (`src/core/signature.ts`); a missing or wrong signature, or a bundle without a key, refuses the update and logs
  `cannot update to <version>: refused: <why>`. Nothing turns the check off. Limits: signing covers the update channel
  only (an app's server still drives agents in auto-approve mode); an offer older than the running x.y.z is refused (a replayed old manifest cannot roll a daemon back), but a daemon
  can be kept on its version by serving it the same manifest for ever (the signature has no expiry); no key rotation (losing the release key means reinstalling daemons).
- **Rollback.** The launcher keeps `daemon/previous`. A version that exits non-zero within 30 s of its start, twice in a
  row (`daemon/crashes`), is rolled back: `current` points at `previous`, the bad version is written to
  `daemon/rejected`, and the daemon does not update to that version again until the releases page offers a different one.

## Files

- `~/.agentd` (`$AGENTD_HOME`): `node/` (a downloaded Node.js), `daemon/<version>/`, `daemon/current`,
  `daemon/previous`, `daemon/rejected`, `daemon/crashes`, `bin/agentd`, `logs/daemon.log`, `run/loop.pid`.
  Remove it all with `agentd service uninstall && rm -rf ~/.agentd`.
- Config `{apps:[{server, token}], browsers:[{origin, grantedAt}]}`, mode 0600, written through a temporary file and a rename: `$XDG_CONFIG_HOME` or `~/.config` + `/agentd/daemon.json` (Linux, macOS);
  `%APPDATA%\agentd\daemon.json` (Windows). `pair` and the gateway write it. A daemon with nothing saved runs and waits for an app to ask.
- Settings `{updates}` in `settings.json` beside the config file (`--config` moves both), written by the tray's settings window through a temporary file and a rename. Read at start; a missing file, or one that is not JSON (said in the log), is the defaults: updates on.
- Orphan record `sessions.json`: `$XDG_STATE_HOME` or `~/.local/state` + `/agentd/` (Windows: `%LOCALAPPDATA%`; not kept there).
  It lists the process groups of running agents with the start time of each leader. After `kill -9` of the daemon,
  the next start ends the groups that are still running.
- A temporary directory (mode 0700) with the `agentd-mcp` wrappers, one per app (`bin/<n>`) and one for pages (`bin/page`), first on the `PATH` of their agents, and the pages' bridge socket `mcp.sock`; removed on exit.

## Design in one line per block

- One socket on the `/agentd` namespace per app; every stdout line, stderr chunk and exit of every agent travels in one ordered
  `frame` event. The contract is `contract/define.acp.ts`; `../contract/client.generated.ts` is generated from it,
  so a renamed method fails `tsc`.
- A dropped socket ends every agent (SIGTERM, then SIGKILL after 2 s, to the whole process group) and frames of the
  old connection are never sent after the reconnect. Reconnects back off from 0.5 s to 15 s and send `hello` each time.
- A token the server refuses (`unauthorized`: unknown or revoked) detaches that app with no retry (pair again, then
  `service start`); the daemon exits 0 when it was the last app and no page grant exists. A server that could not
  check the token now (`unavailable`, its token store failed) is retried with the reconnect backoff.
- `launcher.sh` (shell, linted by shellcheck) is the launcher; it ships beside `daemon.js` in the tarball and the
  daemon refreshes `~/.agentd/bin/agentd` from it after an update.
- The server names an agent (`claude`, `codex`, `grok`); the command table lives in `src/core/agents.ts`
  (adapter versions follow `agent/package.json`). The ACP lines that follow are the app's: see "What Allow gives".
- `src/core` is pure (decisions, parsing, paths); the rest does the I/O. The lint rule `src/core` forbids I/O imports there.

## Test seam

`AGENTD_AGENT_<NAME>` (for example `AGENTD_AGENT_CLAUDE="/path/fake-agent --flag"`) replaces the command line
of that agent and counts it as ready. Tests and the e2e suite use it to run a fake agent. It is not a user feature.
Other seams, for tests only: `AGENTD_HOME` (replaces `~/.agentd`), `AGENTD_SERVICE_LABEL` and
`AGENTD_SERVICE_DIR` (name and directory of the service definition, so a test never touches the user's service),
`AGENTD_SERVICE_MANAGER=loop|launchd|systemd` (skips detection), `AGENTD_QUICK_SECONDS` (the launcher's 30 s),
`AGENTD_NODE_DIST` (the install script's Node.js mirror), `AGENTD_ADAPTERS=off` (no adapter install), `AGENTD_LOCAL_PORT` (the gateway's first port, `0` for any), `AGENTD_CONSENT=<program>` (runs in place of the dialog with the title and the text as arguments; exit 0 is Allow; never copied into a service definition), `AGENTD_TRAY` (`off` or a program), `AGENTD_ADAPTER_SPEC_<NAME>` (the package spec the
adapter install gets, for example `file:/path/fake-adapter.tgz`; the install directory is named after a hash of it).

## Develop

```
cd daemon
bun install
bunx tsc --noEmit
bun run lint
bun test test/unit                 # pure logic, no processes
bun run test                       # unit + integration; the integration build (about 35 MB of binaries) lives in a directory of the run, removed at its end
bun test test/integration          # real `node daemon.js` under the real launcher and install.sh, against a Go app-server double; needs go (scripts/go.sh), lsof, curl, tar, bash
AGENTD_TEST_NETWORK=1 bun test test/integration/install.test.ts   # also downloads Node.js from nodejs.org (about 50 MB)
AGENTD_TEST_LAUNCHD=1 bun test test/integration/service.test.ts   # macOS: one real launchd job with a unique label, removed afterwards
RUN_REAL_CLAUDE=1 bun test test/integration/claude.real.test.ts   # two short turns with the real Claude adapter (costs money)
(cd ../tray && go vet ./... && go test ./...)   # the tray's menu model
(cd ../sdk/client && bun test)                  # the SDK pages use
bun test --coverage test/unit
../scripts/build-daemon.sh /tmp/dist   # download/v<version>/{agentd-<version>.tgz (daemon.js, launcher.sh, package.json), latest.json (signed), install.sh}; dev key in ../.dev-signing/ (made on first use)
```

The app-server double is `test/server/main.go` (build tag `daemontest`, so it stays out of `go build ./...`): it serves the generated
`contract` code on `/agentd`, a loopback `/ctl` API for the tests, and `POST /agentd/pair`. Installs and updates in the tests
come from the real local stand-in for GitHub Releases (`cmd/agentd-server`, started by `test/integration/harness.ts`) or from the `ReleaseStub` in
`test/integration/versions.ts`, which a test steers (what `latest.json` says, download counts and delays).
