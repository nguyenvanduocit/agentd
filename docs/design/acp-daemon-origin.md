> This is the first design of the daemon, written inside MindChains. Where it names `mindchains-daemon`, `/daemon`, `~/.mindchains` or the app serving installs and updates, read `agentd`, `/agentd`, `~/.agentd` and the release server: `agentd-extraction.md` is the current design.

# ACP Daemon

Chat agents run on the user's machine. A Node.js daemon (`mindchains-daemon`) spawns the agent, relays its ACP stream to the server over socketrpc, and the server stays the ACP client. The daemon connects out to each server it serves; its only listening port is a gateway on 127.0.0.1 for discovery and consent (`2026-10-19-daemon-multi-app-design.md`).

## Goal

A user's own agents, MCP servers, skills and folders work in MindChains chat. The server provides the graph tools and executes them. No agent process, no agent sign-in and no agent CLI lives on the server.

## Decisions

| # | Decision | Why |
|---|----------|-----|
| 1 | Daemon only. The server never spawns an agent. | One path, no server shell risk. |
| 2 | The server is the ACP client; the daemon relays JSON lines. | `acpClient`, `Session`, `Manager` keep their shape. The agent runs its own tools, because `Initialize` sends no `ClientCapabilities` (`internal/agent/acp.go:257-260`). |
| 3 | The daemon is Node.js (TypeScript, bundled to one JS file for Node >= 20): daemon, `pair` and `mcp` subcommands. | The Claude and Codex ACP adapters are Node packages (`internal/agent/specs.go:36`). One runtime. |
| 4 | The daemon contract is `daemonrpc/define.acp.ts`. socketrpc-gen generates the Go server code and the TypeScript client beside it, in `daemonrpc/`. | The daemon's handlers type-check against the contract: a renamed method fails the build. No hand-written wire. |
| 5 | `open` takes an agent name. The daemon owns the command table. | The server cannot make the daemon run an arbitrary binary. |
| 6 | MCP reaches the server through a stdio bridge: `mindchains-mcp`, a wrapper script around `node <daemon.js> mcp --session <id>`, opens its own socket. The agents of a server app reach the server only through the daemon's outbound socket and this bridge. | Spike H4. `internal/agenttools/server.go` stays off the public mux. |
| 7 | Pairing is started from the web UI: a single-use 10-minute code embedded in one `curl ... \| bash` command that installs, pairs and starts the daemon as a service. | One step for the user, and the machine needs nothing but `curl`, `bash` and `tar`. |
| 8 | A user pairs any number of daemons. Each has an id, a name (the machine's hostname until renamed) and its own token. Settings lists them with state, version, os and agents, and renames, disconnects (revokes) and adds them. A user has at most one pending pairing code; asking again replaces it. | One user, several machines. |
| 9 | Auto-approve: the server sets the agent's never-ask mode (`internal/agent/specs.go` `Spec.Mode`). | Owner's decision. Card content can reach an agent holding the user's shell and files: say so in the UI copy. |
| 10 | The tool lock is removed on the server (`readInit`, `primeToolList`, `AllowedTool`, `webTools`, `strictMCPConfig`). Claude sessions load `settingSources: ["user","project","local"]`. | The user's skills, MCP servers and settings must reach the agent. |
| 11 | Adapters start through `npx` at the versions pinned in `agent/package.json` (claude-agent-acp 0.86.0, codex-acp 2.1.1); Grok is `grok agent --always-approve stdio` from `PATH`. The installer guarantees `node` and `npx`: it uses the machine's Node >= 20, and otherwise installs a pinned Node LTS under `~/.mindchains/node`, and the daemon puts that directory first on the agents' `PATH`. | `hello` reports `ready:false` with a reason when `grok` is missing. |
| 12 | The daemon's working directory for a session is the directory the daemon was started in (`--dir` overrides). | Matches how `claude` behaves. |
| 13 | The server serves the daemon as a tarball with its version in the name, and serves `install.sh`: a script rendered per request with the server's origin, the daemon version and its sha256 baked in. The UI shows one command: `curl -fsSL <origin>/daemon/install.sh \| bash -s -- --code <code>`. | No npm registry account, no `npx` on the machine, and the daemon version always matches the server. The script verifies the tarball against the baked-in sha256. |
| 14 | A conversation runs on one daemon. The choice is the key `daemon` in the conversation's `config` JSON (beside `agent`) and is offered as the option `daemon` among the conversation's options, listing the user's online daemons. A new conversation takes the daemon that connected last. When the chosen daemon is offline the turn fails with a message that names it and says to pick another; the next session starts from the stored transcript, as when the agent is switched. Agents and their model options come from the chosen daemon's `hello` and catalog. | Several daemons need a rule for who answers; reusing `config` needs no schema change. |
| 15 | The installer registers the daemon as a user service so it starts at login and restarts when it dies: a launchd LaunchAgent on macOS (`KeepAlive` with `SuccessfulExit=false`, `ThrottleInterval`), a systemd user unit on Linux (`Restart=on-failure`, `RestartSec`). Exit codes are the protocol with the service: 0 means stay stopped (a stop from the UI, a revoked token), 75 means restart now (a restart from the UI, a finished update), anything else is a crash and restarts after the throttle. Where no service manager exists the launcher runs the daemon in a restart loop with backoff. The daemon logs and exits non-zero on `uncaughtException` and `unhandledRejection`, so the service restarts it. `mindchains-daemon service install\|uninstall\|status` manages it; `install.sh --no-service` and `--foreground` skip it. Logs go to `~/.mindchains/logs/daemon.log`, rotated. | A daemon that needs a terminal is not a daemon. |
| 16 | The daemon updates itself. The `hello` reply carries the daemon version the server serves, its tarball path, its sha256 and the release key's signature of the version and sha256. When the version differs from the running one, the daemon checks the signature against the release public key baked into its bundle, downloads the tarball, checks the sha256, unpacks it to `~/.mindchains/daemon/<version>`, points `~/.mindchains/daemon/current` at it, and exits when no session is running so the service restarts it on the new version. A failed update leaves the running version in place and retries with backoff. | The daemon version always matches the server's contract. |
| 17 | The launcher keeps the previous version. When a new version exits non-zero within 30 s of starting, twice in a row, the launcher points `current` back at the previous version and the daemon does not try that version again until the server serves a different one. | A broken release must not strand every machine: a crash-looping daemon cannot download its own fix. |
| 18 | Settings controls a daemon: Restart and Stop (and Disconnect, which revokes). The server sends `restart` or `stop` over the daemon's socket; the daemon ends every agent session, flushes, and exits with 75 or 0 (decision 15). Restart is offered only when `hello` says the daemon is `managed`; a daemon started with `--foreground` can only be stopped. The UI warns when `sessions` > 0 (the running turns fail) and says that a stopped daemon cannot be started from the web: it is started on its machine with `mindchains-daemon service start`. A revoked daemon exits 0 and does not retry. Only the owner of the daemon can control it. | A kill switch and a restart from the place the user already is, without a second restart loop fighting the service. |

## Assumptions (not confirmed by the user)

- Pairing started from the web UI (decision 7) replaces the device-code flow chosen earlier.
- Pairing the same machine twice creates a second entry; the first stays offline until the user disconnects it.
- Updates are signed. `scripts/build-daemon.sh` signs `"mindchains-daemon\n<version>\n<sha256>\n"` with the Ed25519 release key (a Fly build secret; its public half is `daemon/release.pub`) and bakes the public key into the bundle. A daemon refuses an offer whose signature does not verify, and a bundle without a key refuses every offer. Limits:
  - Signing protects the update channel only. The server still drives the daemon's agents in auto-approve mode, so a compromised server can still run commands on a paired machine through an agent turn; it can no longer replace the daemon itself.
  - `install.sh` does not check the signature: a first install already trusts the server's TLS and the script it serves, and a compromised server can serve another script.
  - No anti-rollback: a compromised server can offer an older signed version again.
  - No key rotation: a lost or leaked release key means reinstalling every daemon.
  - Daemons built before signing do not check: they accept the first signed release like any other, and check every update after it.
- An update waits for the daemon to be idle; a turn in progress is never cut off for an update.
- The per-tenant sign-in directories under `AUTH_DIR` on Fly are left untouched; no deploy, no data deletion in this change.
- Distribution by tarball and `install.sh` from the server (decision 13), not the npm registry.
- The installer downloads a pinned Node LTS from nodejs.org (sha256 checked against that release's `SHASUMS256.txt`) when the machine has no Node >= 20.
- The service starts at login (launchd, systemd user). Starting at boot without a login needs `loginctl enable-linger` and root on Linux; the installer prints that command and does not run it.
- macOS and Linux only; on Windows the script says to use WSL.

## Wire

Namespace `/daemon` on the existing Socket.IO server, own auth middleware (the default namespace keeps the cookie middleware, `internal/app/app.go:41`).

- Daemon handshake auth: `{role:"daemon", token}`. Bridge: `{role:"mcp", token, session}`. The server checks that `session` belongs to the token's user.
- Pairing: `POST /daemon/pair {code, hostname, os, arch}` -> `{token}`. The server stores `sha256(token)`, never the token.
- One ordered stream per socket: stdout lines, stderr and exit all travel in `frame`, because generated code runs each event name on its own queue (`rpc/server.generated.go` `rpc_queuedListener`).
- `maxHttpBufferSize` is 64 MiB (spike H3: the 1 MB default kills the session on a large tool result). acp-go-sdk allows 10 MiB per line.
- Socket drop means the daemon kills the agent's process group; the session fails like a dead process today. No resume.
- Install: `GET /daemon/install.sh` (rendered from a template embedded in the server, with the origin and the version and sha256 of the tarball in `DAEMON_DIR/install.json` baked in); `GET /daemon/mindchains-daemon-<version>.tgz`. The Dockerfile builds the tarball and `install.json` with `scripts/build-daemon.sh <outdir>` (bun bundles `daemon/src` to `dist/daemon.js` for Node, then packs `package.json` with a `bin` entry and the bundle; no runtime dependencies).
- `install.sh --code <code>` does, in order: pick Node (system >= 20, else a pinned LTS into `~/.mindchains/node`); download the tarball, check sha256, unpack into `~/.mindchains/daemon/<version>` and point `current` at it; write the launcher `~/.mindchains/bin/mindchains-daemon` (sets `PATH`, runs `node current/daemon.js "$@"`, keeps the previous version for rollback); run `pair --server <origin> --code <code>` when a code is given; install and start the service, then print its status. `--foreground` skips the service and `exec`s the daemon in the terminal; `--no-service` installs and pairs only. Without `--code` it starts the saved pairing.
- Service files: macOS `~/Library/LaunchAgents/com.mindchains.daemon.plist`; Linux `~/.config/systemd/user/mindchains-daemon.service`. Both run the launcher with `run`. Uninstall removes the service and leaves `~/.mindchains` in place; `rm -rf ~/.mindchains` removes the rest.
- The UI shows `curl -fsSL <origin>/daemon/install.sh | bash -s -- --code <code>` while a code is pending. A daemon that is offline shows `mindchains-daemon service status` and the launcher path.

## Browser contract (`frontend/src/rpc/define.ts`)

`DaemonList {daemons: DaemonInfo[{id,name,hostname,state: online|offline,version,os,agents: DaemonAgent[{name,title,ready,reason}],lastSeen}], pairing: {code,expiresAt}, serverVersion}`.
Server functions: `getDaemons`, `startDaemonPairing`, `cancelDaemonPairing`, `renameDaemon(id, name)`, `revokeDaemon(id)`, each returning the list. Client function: `onDaemons(list)`, pushed to every tab of the user when a daemon connects, disconnects, is renamed or revoked, or the pairing changes.
Daemon contract: `hello(info) -> HelloReply{version,url,sha256,signature}` (decision 16).
The five `*AgentLogin*` / `logoutAgent` functions and `AgentLogin` are removed.

## Removed

`internal/agentlogin/`, the sign-in parts of `internal/tenant/tenant.go`, `ConfigDir`/`ConfigEnv`/`StripEnv`/`agentEnv`/`ErrNotConnected`/`PerTenant` in `internal/agent`, `AuthDir` and the agent binary settings in config, the agent CLIs in the `Dockerfile`, the frontend `AgentAuth*`/`AgentConnect`/`useAgentLogin`/`createAgentLogin` and the Settings > Auth tab.

## Acceptance

1. `go test ./...`, `golangci-lint`, `bun test`, `vue-tsc`, `bun run lint` pass on the changed scope; the daemon type-checks and its tests pass.
2. The daemon type-checks against the generated client, and fails to type-check when a method of `define.acp.ts` is renamed.
3. With a real Claude adapter on this machine: chat turn through the daemon, graph tool call through the bridge, a local skill listed (`settingSources`), the gateway on 127.0.0.1 as the only listening port in the daemon's process tree.
4. Socket drop mid-turn leaves no agent process.
5. Chat is gated with a clear message when no daemon is online; an agent the user has not signed in to reports "sign in on your machine" instead of a raw adapter error.
6. Two daemons of one user are online together: Settings lists both, renames one and disconnects the other (the disconnected one is cut off and its token no longer connects); a conversation answers on the daemon chosen for it, and a conversation whose daemon is offline says which one.
7. The installed service starts the daemon, and killing the daemon process (`kill -9`) brings it back within the service's restart delay; an `uncaughtException` exits non-zero and the service restarts it. Evidence on this machine with a uniquely named test service that is removed afterwards (`launchctl list` before and after).
8. Control: Restart from Settings brings a managed daemon back with a new pid and a reconnect; Stop leaves it stopped (no restart, offline in the list) and Restart is refused for a daemon that is not managed; another user's daemon id cannot be controlled; revoking a daemon makes it exit 0 and stay stopped. Evidence with the same uniquely named test service as 7.
9. Auto-update: a server that serves a newer daemon version makes a running daemon download it, verify the sha256, wait for idle, restart on the new version and reconnect with the new `version`; a wrong sha256 changes nothing; an offer the release key did not sign is not downloaded; a new version that crashes at start is rolled back to the previous one (decision 17).

## Out of scope

Deploy, deleting data on Fly, Windows (runtime and service), anti-rollback and release key rotation for updates, starting at boot without a login, resuming a turn after a socket drop.
