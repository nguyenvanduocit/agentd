# agentd

A Mac app that runs coding agents (Claude Code, Codex, Grok) on a user's own computer for the web apps the user allows, and what an app needs to use it. A person installs `agentd.app` and opens it.

```
web app (page)  ──@agentd/client──▶  agentd on this computer  ──▶  agent processes (ACP over stdio)
      │                                      ▲
      └── app server ◀── sdk/goserver ───────┘   the daemon dials out to each app it is paired with
```

### What a user installs: one app

| Where | What |
|-------|------|
| `app/` | `agentd.app`, a Go program ([mygo](https://github.com/egoist/mygo), no cgo): it runs the engine (`daemon/run`) in its own process, carries the Node.js its agents' adapters need, shows the engine's state in the menu bar and a window, asks the user before an app or page gets access, stops the engine on Quit, opens at login. Started with a command (`agentd mcp`, `acp`, `apps`, `pair`, `version`) the same executable is agentd's command line (`daemon/entry`). `scripts/build-desktop.sh` builds it. See `docs/design/desktop-app.md` and `docs/design/one-process.md`. |
| `daemon/`, `cmd/agentd` | The engine, in Go. agentd.app links it in (`run.Daemon`); `cmd/agentd` runs it with no window, for the tests and the checks on Linux. It pairs with any number of apps, runs their agents, and listens only on `127.0.0.1` for web pages (detect, consent, browser sessions). Its packages are listed in `daemon/README.md`. It also speaks ACP's standard WebSocket transport on `GET /acp/<agent>` (same port; a web page needs the same grant as for `/v1/session`, a program on this computer needs the local token that `agentd acp` prints), so an ACP client that knows nothing of agentd can use the agents it runs. |

### What an app developer uses (`sdk/`, `contract/`)

| Where | What |
|-------|------|
| `sdk/web/` | `@agentd/client`, for web pages and ES apps: detect the engine, ask to connect, run an agent from a page, and the download links of agentd.app. Built from here and attached to each GitHub release as `agentd-client-<version>.tgz`, which apps install from. |
| `sdk/goclient/` | `github.com/nguyenvanduocit/agentd/sdk/goclient`, for Go apps: the same client for a Go program that talks to the daemon's local gateway as a page does (every call names the app's origin). Standard library and `gorilla/websocket` only; installed with `go get` at the release tag. |
| `sdk/goserver/` | `github.com/nguyenvanduocit/agentd/sdk/goserver`, the Go library for an app's server: pairing, the `/agentd` socket namespace, agent sessions as `goserver.Conn`, the user's daemons list. No dependency on any app. [README](sdk/goserver/README.md). |
| `contract/` | `define.acp.ts` (the daemon protocol) and the Go server code `socketrpc-gen` generates from it. |

### What develops and tests agentd itself

| Where | What |
|-------|------|
| `examples/minimal-app/` | A Go app on `sdk/goserver` with a page on `sdk/web`: the smallest second app. Also the end-to-end fixture. |
| `e2e/`, `scripts/` | The end-to-end scenarios, and the build, release and CI scripts. |
| `adapters/` | Pins the versions of the Claude and Codex ACP adapters the daemon installs. |

Design: `docs/design/` (`desktop-app.md` and `one-process.md` for agentd.app, `multi-app-daemon.md` the daemon's own).

## Build and test (all natively)

```
scripts/gen-contract.sh                  # regenerate contract/ after editing define.acp.ts (scripts/check-generated.sh in CI)
go vet ./... && go test ./...                          # the daemon, the SDKs and the release scripts: unit + integration against real processes
(cd sdk/web && bun install && bun run typecheck && bun test)
(cd app && go test ./...)
scripts/build-desktop.sh                 # agentd.app and its .dmg in app/build/darwin-<arch>/ (DESKTOP_ARCHES=arm64 for one)
scripts/e2e.sh                           # the whole product through a real browser (ego-browser): the example app and an engine it pairs with
```

## Release

A release is a GitHub release `v<version>` of this repository. It carries agentd.app for each Mac (`agentd-darwin-arm64.dmg`, `agentd-darwin-amd64.dmg`, the names `desktopDownloads()` links to), its signed update (`agentd-<version>-darwin-<arch>.tar.gz` and `update-darwin-<arch>.json`, which installed apps read from the latest release), and `agentd-client-<sdk version>.tgz` (the page SDK). The tag is also the Go module version of `github.com/nguyenvanduocit/agentd`.

```
APP_UPDATE_KEY=.release-signing/mygo-update.key scripts/release.sh 0.5.0            # builds and checks, uploads nothing
APP_UPDATE_KEY=.release-signing/mygo-update.key scripts/release.sh 0.5.0 --publish  # needs gh, a clean tree and HEAD pushed
```

Set `app/mygo.json`'s version to the release's in its commit (the script refuses another), and bump `sdk/web/package.json` before a release whose SDK sources changed: a published file never changes.

The app's update key (`mygo keygen`) has its public half in `app/mygo.json` (`updates.publicKey`), baked into every app. The private half is git-ignored (`.release-signing/` on the maintainer's machine) and is also a secret of the GitHub environment `release` (deployments limited to `main`), which only `.github/workflows/release.yml` uses; that workflow starts by hand (`workflow_dispatch`), never on a pull request, and its actions are pinned by commit SHA. Whoever can edit that workflow on `main`, or read the environment's secrets, can sign an update that every installed app takes. The engine inside the app does not update itself: the app's update replaces it.

## An app on agentd

1. Server (Go), in [`sdk/goserver/README.md`](sdk/goserver/README.md): `h := goserver.New(goserver.Options{Tokens: ...})` and `h.Attach(mux)` put the daemons' socket and the pairing endpoint on your web server; `h.For(userID).StartPairing()` gives the user a code; `h.OpenLatest(ctx, userID, "claude")` (or `h.Open(ctx, userID, daemonID, "claude")` for the computer the user chose) returns the agent's ACP stream. Every error is a value to test with `errors.Is` and `errors.As`; the README lists them. `examples/minimal-app/main.go` is the whole thing in about 170 lines. A paired daemon that connected but speaks protocols that do not meet the app's is not online: its entry in the daemons list has `Incompatible` set to a sentence naming the side to update, and `Open` returns a `goserver.IncompatibleError`. An app that serves MCP tools to its agents (`goserver.Options{Tools: ...}`) gives each session the bridge in `session/new` (`goserver.BridgeCommand(grantID)`).
2. Page: `bun add https://github.com/nguyenvanduocit/agentd/releases/download/v<version>/agentd-client-<sdk version>.tgz` (`sdk/web/README.md`), `createLocalDaemon(...)` for "Connect this computer", `desktopDownloads()` for the Mac disk images.
3. A user on a Mac downloads `agentd.app`, opens it, and connects it from the page. Linux, Windows and phones have no install.

A page can connect only the app at its own origin. Two limits an app works within:

- One process serves the `goserver.Server`: pairing codes, the count of wrong codes and the daemons' sockets live in its memory. An app that runs several server instances sends `/agentd`, `/agentd/pair` and the calls that start a pairing to one of them.
- A page takes the first daemon that answers on `127.0.0.1:47615-47624` and has no way to prove it is agentd: a program already listening there receives the pairing code the page sends. The daemons list (`goserver.List`) shows every paired daemon with its name, hostname and when it was last seen, so an app shows the list where the user notices one they did not install.
