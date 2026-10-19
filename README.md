# agentd

A desktop app that runs coding agents (Claude Code, Codex, Grok) on a user's own computer for the web apps the user allows, and what an app needs to use it. On macOS a person installs `agentd.app` and opens it; headless machines run the same engine as a daemon.

```
web app (page)  ──@agentd/client──▶  agentd on this computer  ──▶  agent processes (ACP over stdio)
      │                                      ▲
      └── app server ◀── hub (Go library) ───┘   the daemon dials out to each app it is paired with
```

| Part | Where | What |
|------|-------|------|
| Desktop app | `tray/` | `agentd.app`, a Go program ([mygo](https://github.com/egoist/mygo), no cgo): it runs the engine with the Node.js it carries, shows its state in the menu bar and a window, ends it on Quit, opens at login. `scripts/build-desktop.sh` builds it. See `docs/design/desktop-app.md`. |
| Engine (daemon) | `daemon/` | Node.js (bundled by bun). Pairs with any number of apps, runs their agents, listens only on `127.0.0.1` for web pages (detect, native consent dialog, browser sessions). Run by agentd.app, or on a headless machine as a launchd or systemd user service that updates itself from the GitHub release. |
| Tray | `tray/` | The same program as `agentd-tray child`: the icon and settings window of a daemon installed from the terminal. Built for macOS, Linux and Windows. |
| Hub | `hub/` | Go library for an app's server: pairing, the `/agentd` socket namespace, agent sessions as `hub.Conn`, the user's daemons list. No dependency on any app. |
| Release | `release/` | Go library for an app's server: the daemon version the latest release offers (its signed `latest.json`), for the "update pending" hint of the daemons list. |
| Page SDK | `sdk/client/` | `@agentd/client`: detect the daemon, ask to connect, run an agent from a page, the download links of agentd.app and the install command. Built from here and attached to each GitHub release as `agentd-client-<version>.tgz`, which apps install from. |
| Local releases | `server/`, `cmd/agentd-server` | A stand-in for GitHub Releases for development and the tests: serves the files `scripts/build-daemon.sh` writes under the URLs GitHub gives release assets. Read-only over a `dist/` directory. |
| Contract | `contract/` | `define.acp.ts` (the daemon protocol) and the code `socketrpc-gen` generates from it (Go server, TypeScript client). |
| Example | `examples/minimal-app/` | A Go app on the hub with a page on the SDK: the smallest second app. Also the end-to-end fixture. |

Design: `docs/design/` (`agentd-extraction.md` is the one for this repository, `multi-app-daemon.md` the daemon's own).

## Build and test (all natively)

```
scripts/gen-contract.sh                  # regenerate contract/ after editing define.acp.ts (scripts/check-generated.sh in CI)
(cd daemon && bun install && bun run typecheck && bun run lint && bun run test)   # unit + integration against real processes
(cd sdk/client && bun install && bun run typecheck && bun test)
(cd tray && go test ./...)
scripts/build-desktop.sh                 # agentd.app and its .dmg in tray/build/darwin-<arch>/ (DESKTOP_ARCHES=arm64 for one)
go vet ./hub/ ./release/ ./server/ ./contract/ ./cmd/... ./examples/... && go test ./hub/ ./release/ ./server/
scripts/e2e.sh                           # the whole product through a real browser (ego-browser): example app, install.sh, update (stop any running daemon first)
```

## Release

A release is a GitHub release `v<version>` of this repository. It carries agentd.app for each Mac (`agentd-darwin-arm64.dmg`, `agentd-darwin-amd64.dmg`, the names `desktopDownloads()` links to) and its signed update (`agentd-<version>-darwin-<arch>.tar.gz` and `update-darwin-<arch>.json`, which installed apps read from the latest release), the daemon (`agentd-<version>.tgz`, with the trays), `latest.json` (the signed offer daemons read), `install.sh`, and `agentd-client-<sdk version>.tgz` (the page SDK). The tag is also the Go module version of `github.com/nguyenvanduocit/agentd`.

```
KEYS="DAEMON_SIGNING_KEY=.release-signing/daemon-signing.key APP_UPDATE_KEY=.release-signing/mygo-update.key"
env $KEYS scripts/release.sh 0.5.0            # builds and checks, uploads nothing
env $KEYS scripts/release.sh 0.5.0 --publish  # needs gh, a clean tree and HEAD pushed
```

Set `tray/mygo.json`'s version to the release's in its commit (the script refuses another), and bump `sdk/client/package.json` before a release whose SDK sources changed: a published file never changes.

The release key's public half is `daemon/release.pub` and is baked into every daemon, which refuses an update it did not sign; the app's update key (`mygo keygen`) has its public half in `tray/mygo.json` (`updates.publicKey`), baked into every app the same way. The private halves (`.release-signing/`, git-ignored) never leave the machine that builds releases. The releases page (`RELEASE_URL`, `https://github.com/nguyenvanduocit/agentd/releases` by default for a release) is baked into the daemon as the place it takes updates from (`AGENTD_RELEASE_URL` overrides it at run time). For development, `agentd-server -dist <dist>` serves a `scripts/build-daemon.sh <dist>` output under the same URLs.

## An app on agentd

1. Server (Go): `hub.New(hub.Options{Tokens: ...})`, `h.Mount(socketServer, mux)`, then `h.For(userID).StartPairing()` for the code, `h.Open(ctx, userID, daemonID, "claude")` for an agent. `examples/minimal-app/main.go` is the whole thing. An app that serves MCP tools to its agents (`hub.Options{Tools: ...}`) gives each session the bridge in `session/new`: `command, args := hub.BridgeCommand(grantID)` as its stdio MCP server. Agents arrive with the daemon's order and titles (`Info.Agents[].Title`). For the "update pending" hint: `latest := release.Watch(ctx, http.DefaultClient, release.Releases, time.Hour)` and `hub.Options{LatestVersion: latest.Version}`; a daemon of the list is behind when `release.Older(d.Version, list.LatestVersion)`.
2. Page: `bun add https://github.com/nguyenvanduocit/agentd/releases/download/v<version>/agentd-client-<sdk version>.tgz` (`sdk/client/README.md`), `createLocalDaemon(...)` for "Connect this computer", `installCommand(...)` for the install line.
3. A user installs with the command the page shows: `curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash -s -- --app <your origin> --code <code>`.

A page can connect only the app at its own origin. Two limits an app works within:

- One process serves the hub: pairing codes, the count of wrong codes and the daemons' sockets live in its memory. An app that runs several server instances sends `/agentd`, `/agentd/pair` and the calls that start a pairing to one of them.
- A page takes the first daemon that answers on `127.0.0.1:47615-47624` and has no way to prove it is agentd: a program already listening there receives the pairing code the page sends. The daemons list (`hub.List`) shows every paired daemon with its name, hostname and when it was last seen, so an app shows the list where the user notices one they did not install.
