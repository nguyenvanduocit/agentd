# agentd

A daemon that runs coding agents (Claude Code, Codex, Grok) on a user's own computer for the web apps the user allows, and what an app needs to use it.

```
web app (page)  ──@agentd/client──▶  agentd on this computer  ──▶  agent processes (ACP over stdio)
      │                                      ▲
      └── app server ◀── hub (Go library) ───┘   the daemon dials out to each app it is paired with
```

| Part | Where | What |
|------|-------|------|
| Daemon | `daemon/` | Node.js (bundled by bun). Pairs with any number of apps, runs their agents, listens only on `127.0.0.1` for web pages (detect, native consent dialog, browser sessions), updates itself from the release server, runs as a launchd or systemd user service. |
| Tray | `tray/` | A Go program ([mygo](https://github.com/egoist/mygo), no cgo): the daemon's icon in the menu bar or notification area. Built for macOS, Linux and Windows. |
| Hub | `hub/` | Go library for an app's server: pairing, the `/agentd` socket namespace, agent sessions as `hub.Conn`, the user's daemons list. No dependency on any app. |
| Page SDK | `sdk/client/` | `@agentd/client`: detect the daemon, ask to connect, run an agent from a page, the install command. Built from here and published as the [agentd-client](https://github.com/nguyenvanduocit/agentd-client) repository, which apps install from. |
| Release server | `server/`, `cmd/agentd-server` | Serves `install.sh` and the signed daemon release (`latest.json` + tarball). Read-only over a `dist/` directory. |
| Contract | `contract/` | `define.acp.ts` (the daemon protocol) and the code `socketrpc-gen` generates from it (Go server, TypeScript client). |
| Example | `examples/minimal-app/` | A Go app on the hub with a page on the SDK: the smallest second app. Also the end-to-end fixture. |

Design: `docs/design/` (`agentd-extraction.md` is the one for this repository, `multi-app-daemon.md` the daemon's own).

## Build and test (all natively)

```
scripts/gen-contract.sh                  # regenerate contract/ after editing define.acp.ts (scripts/check-generated.sh in CI)
(cd daemon && bun install && bun run typecheck && bun run lint && bun run test)   # 380 tests: unit + integration against real processes
(cd sdk/client && bun install && bun run typecheck && bun test)
(cd tray && go test ./...)
go vet ./hub/ ./server/ ./contract/ ./cmd/... ./examples/... && go test ./hub/ ./server/
scripts/e2e.sh                           # the whole product through a real browser (ego-browser): example app, install.sh, update
```

## Release

```
RELEASE=1 RELEASE_URL=https://<agentd host> DAEMON_SIGNING_KEY=.release-signing/daemon-signing.key scripts/build-daemon.sh dist
scripts/build-sdk.sh dist                 # bump sdk/client/package.json first when its sources changed: a version never changes
go build -o agentd-server ./cmd/agentd-server
./agentd-server -dist dist -public-url https://<agentd host> -addr :7100
```

The release key's public half is `daemon/release.pub` and is baked into every daemon, which refuses an update it did not sign. The private half (`.release-signing/`, git-ignored) never leaves the machine that builds releases. `RELEASE_URL` is baked into the daemon as the place it takes updates from (`AGENTD_RELEASE_URL` overrides it at run time).

## An app on agentd

1. Server (Go): `hub.New(hub.Options{Tokens: ...})`, `h.Mount(socketServer, mux)`, then `h.For(userID).StartPairing()` for the code, `h.Open(ctx, userID, daemonID, "claude")` for an agent. `examples/minimal-app/main.go` is the whole thing.
2. Page: `bun add github:nguyenvanduocit/agentd-client#v<version>` (`sdk/client/README.md`), `createLocalDaemon(...)` for "Connect this computer", `installCommand(...)` for the install line.
3. A user installs with the command the page shows: `curl -fsSL <agentd host>/install.sh | bash -s -- --app <your origin> --code <code>`.

A page can connect only the app at its own origin.
