# The daemon in Go

The engine (`agentd run`, today `daemon/src/*.ts`, a Node bundle) is a Go program. The user chose this ("convert ts to
golang"; scope: the daemon engine) and left the design to the builder.

## Goals

- One static Go binary `agentd` per platform replaces `daemon.js`: the same CLI, the same files under `~/.agentd` and
  `~/.config/agentd`, the same gateway on 127.0.0.1:47615, the same socket.io contract with an app's server
  (`contract/define.acp.ts`), the same exit codes (0 stop, 75 start again), the same fd-3 channel to `agentd.app`.
- The daemon no longer needs Node.js to run. Node stays for the ACP adapters (`agent/package.json`: claude-agent-acp,
  codex-acp), which are npm packages the daemon installs and spawns; the app bundle and `install.sh` keep fetching it.
- Behavior is ported, not redesigned: each TS unit test becomes a Go test of the same case, each integration test
  drives the Go binary.

## Non-goals

- The browser SDK `sdk/client` and the generated browser contract client stay TypeScript (they run in pages).
- The tray program `tray/` stays its own Go module (mygo, GUI); it runs the engine binary instead of `node daemon.js`.
- A native Windows daemon.

## Layout

Packages of the root module `github.com/nguyenvanduocit/agentd`, under `daemon/`, flat and named by what they provide.
`cmd/agentd/main.go` is the entry (the CLI dispatch of `daemon/src/main.ts`). A package imports only packages of
an earlier wave.

| Wave | Package | Ports (`daemon/src/…`) |
|------|---------|------------------------|
| 1 | `daemon/home` | core/paths, layout, settings, executable, exit, backoff, server, wrapper; settings.ts, host.ts, version.ts, launcher.ts |
| 1 | `daemon/proc` | proc.ts, procgroup.ts, log.ts, state.ts, core/spawnplan, orphans, rotate, lines |
| 1 | `daemon/cli` | core/cli (the parser and USAGE) |
| 2 | `daemon/registry` | core/registry, config.ts, pair.ts, apps-cli.ts |
| 2 | `daemon/agents` | core/agents, core/adapters, adapters.ts, fleet.ts, session.ts |
| 2 | `daemon/service` | core/service, service.ts, launchd.ts, systemd.ts, manager.ts |
| 2 | `daemon/update` | core/update, core/signature, releases.ts, updater.ts, http.ts |
| 2 | `daemon/consent` | core/consent, consent.ts, core/peer, peer.ts |
| 3 | `daemon/link` | link.ts, apps.ts, bridge.ts, core/pagemcp, page-mcp.ts, and the Go client of `contract/` |
| 3 | `daemon/gateway` | core/local, core/localproto, local.ts, session-ws.ts |
| 3 | `daemon/tray` | core/tray, tray.ts, applink.ts, tray-menu.ts |
| 4 | `daemon/run` | daemon.ts, core/exit wiring; `cmd/agentd` |

Wave 5: the engine in `agentd.app` and in the release (`scripts/build-daemon.sh`, `build-desktop.sh`, `release.sh`,
`tray/engine.go`, `daemon/launcher.sh`, `install.sh.in`, `sign-daemon.ts`), the integration tests, and removing
`daemon/src`, `daemon/test`, `daemon/package.json` and the TS tooling around them.

## The artifact

- **Daemon tarball** `agentd-<version>.tgz` keeps its name, its `package/` shape and its `latest.json`
  `{version, url, sha256, signature}` (read by `release/`, `server/`, `sdk/client` and every installed daemon, so none
  of them changes). `package/` holds `agentd-<os>-<arch>` for darwin/linux × arm64/amd64, `launcher.sh`, `package.json`
  (`{name, version}`: the updater checks the version there) and `tray/` as today. The tarball already carries a tray build
  per platform, so one tarball for all platforms keeps the update protocol as it is.
- **Unpacking** (`daemon/update`): the updater picks `agentd-<GOOS>-<GOARCH>` from the tarball and installs it as
  `<home>/daemon/<version>/agentd`; `update.DefaultEntry` is `agentd`.
- **Launcher** (`daemon/launcher.sh`) runs `$D/current/agentd "$@"`; Node.js stays on its PATH for the ACP adapters.
- **agentd.app** carries `engine/agentd` (its platform's binary) and `node/` (adapters only); `tray/engine.go` runs it.
- **install.sh** still picks or downloads Node.js (the adapters need npm) and unpacks the tarball for the machine's platform.
- Daemons installed from a release before the Go engine (`daemon.js` bundles) update only through a re-run of the install
  command or through agentd.app; the old updater cannot unpack a Go binary. Publishing is the user's call.

## Rules for every port

- Same behavior, same wire bytes, same file formats, same log lines where a test or a doc reads them.
- Functional core, imperative shell: the TS `core/` files are pure; their Go version stays pure (no I/O, no globals,
  the clock and environment passed in).
- Standard library first. Dependencies already in `go.mod` (socket.io client, acp-go-sdk, mcp go-sdk) are allowed; a new one
  needs a reason written in the commit.
- Every unit test of `daemon/test/unit/<x>.test.ts` has a Go test of the same case in the package that ports `<x>`.
- `go vet`, `golangci-lint run` and `go test` pass on the package before it is done.

## Known risk

`sdk/goserver/fakedaemon_test.go:19-26` documents that the Go Socket.IO client now and then sends the namespace CONNECT
twice or never under load. The daemon's link must not inherit that: `daemon/link` started with a spike that connected 200
times against the app library and counted handshakes, to pick the library client with a watchdog (reconnect when no
CONNECT_ACK arrives) or a small client of its own (the daemon uses the websocket transport only). The spike chose the
small client: `daemon/link/sio.go:4-19` has the decision and its numbers, and `TestOwnClientConnectsOnceEvery200Times` is
its regression test. There is no watchdog.
