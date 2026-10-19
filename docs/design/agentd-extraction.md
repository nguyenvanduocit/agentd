# agentd: the daemon as its own product (design of this repository)

The daemon, its tray, its protocol, its releases and an SDK for apps live in this repository. MindChains is one app that uses the hub library and the SDK.

## Why

The daemon is no longer MindChains-specific (see `2026-10-19-daemon-multi-app-design.md`): one daemon serves many apps, and the next app should not copy 4.8k lines of Go hub, a Node daemon and an installer. Today an app that wants a daemon must also build and serve the daemon's tarball, render its `install.sh`, hold the release key and be the update source. Extracted, an app implements one library and one page SDK; the daemon's distribution belongs to the daemon.

## Goals

- `agentd` is a self-contained product: daemon, tray, protocol, releases, Go hub library, page SDK, docs, CI.
- A second app can be written against the SDK alone (proved by `examples/minimal-app`).
- MindChains stops building, signing and serving the daemon. It depends on `agentd` for the hub library and the page SDK.
- The page SDK is a file of each GitHub release (`agentd-client-<version>.tgz`) that apps install by URL.

## Non-goals

- Deploying the agentd server, buying or choosing its domain, publishing to npmjs (needs the user).
- The daemon on native Windows; cross-origin app servers (a page connects the app at its own origin).
- In-place migration of daemons already installed under the old names (see "Existing installs").
- Renaming MindChains' own browser contract (`startDaemonPairing`, `DaemonList` in `frontend/src/rpc/define.ts`): it is MindChains' UI contract.

## Identity (decided by the user)

Name `agentd`. Repo this repository, Go module `github.com/nguyenvanduocit/agentd`, command `agentd`, MCP wrapper `agentd-mcp`, home `~/.agentd`, service `com.agentd.daemon` (launchd) and `agentd.service` (systemd), env prefix `AGENTD_`, config `agentd/daemon.json`, npm scope `@agentd`.

## Layout of the new repo

```
agentd/
  contract/        define.acp.ts + generated Go server code and TS client (was daemonrpc/)
  daemon/          the Node daemon (was daemon/)
  tray/            the Go tray program, its own module (was tray/)
  hub/             Go library for apps: pairing, sockets, sessions, management API (was internal/daemonhub)
  server/          agentd-server: install.sh and the signed daemon releases
  sdk/client/      @agentd/client: the page SDK (was daemon-client/, grown)
  examples/minimal-app/   a Go app on hub/ with a page on the SDK; also the e2e fixture
  scripts/         build, sign, pack
  docs/            README, protocol, SDK guide, release guide
  .github/workflows/
```

## Design

### 1. Protocol (`contract/`)

`define.acp.ts` keeps `frame`, `hello`, `agents`, `mcp`, `open`, `write`, `close`, `restart`, `stop`, `mcpOut`. `HelloReply` carries only `protocol` (a number, 1): the update offer leaves the protocol because the GitHub release owns updates. The socket namespace is `/agentd` and the pairing endpoint `POST /agentd/pair`, both mounted by the hub on the app.

### 2. Releases (GitHub Releases; `server/` is a local stand-in)

A release is a GitHub release `v<version>` of this repository, made by `scripts/release.sh`, with four files:

| File | What |
|------|------|
| `install.sh` | The installer of that release (`daemon/install.sh.in` with the version, tarball URL and sha256 filled in at build time). Usage: `curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh \| bash -s -- --app <app origin> --code <code>`. |
| `latest.json` | `{version, url, sha256, signature}`, the signature over `"agentd\n<version>\n<sha256>\n"`. |
| `agentd-<version>.tgz` | The daemon tarball (with `tray/<os>-<arch>/`). |
| `agentd-client-<sdk version>.tgz` | The page SDK, installed by its URL. |

`cmd/agentd-server` serves a directory of such files under the same URLs (`/latest/download/<file>` redirecting to `/download/v<version>/<file>`), for development and the end-to-end tests; nothing in a release depends on it.

### 3. Daemon (`daemon/`)

- Updates come from the GitHub release: at start and every 6 h the daemon fetches `<releases>/latest/download/latest.json` (the releases page is `AGENTD_RELEASE_URL`, baked in at build, overridable by env; GitHub's redirects are followed over https) and runs the existing verify, stage, idle, restart and rollback logic. `AppHub`'s update-source plumbing is deleted.
- `agentd pair --app <origin> --code <code>` (was `--server`). `agentd apps`, `agentd service ...`, `agentd version` as today.
- Per-app MCP wrapper `bin/<n>/agentd-mcp`.
- The release key (`release.pub`) is the current key: the private half moves as a git-ignored file, never committed.
- Everything else (gateway, dialog, browser sessions, tray, registry) moves unchanged apart from names.

### 4. Hub library (`hub/`, Go)

The app side of the protocol, with no import of MindChains.

```go
h := hub.New(hub.Options{
    Tokens:  tokenStore,   // hub.TokenStore: ByHash, ByID, List, Add, Rename, Delete, Seen (was Tokens)
    Tools:   toolServer,   // hub.Tools: Owns, Serve (the MCP tools of a session); optional
    Notify:  nil,          // optional
})
h.Mount(socketServer, mux)             // /agentd namespace and POST /agentd/pair
conn, err := h.Open(ctx, userID, daemonID, "claude")   // hub.Conn: Read, Write, Close, Done, Cwd, Stderr (ACP lines)
h.For(userID).StartPairing() / List() / Rename() / Revoke() / Restart() / Stop() / Watch()
```

- Neutral types replace MindChains' `rpc.DaemonList`: `hub.List{Daemons []hub.Daemon, Pairing hub.Pairing}` with the same fields. MindChains converts with a small adapter into its own `rpc` types.
- `hub.Conn` is the interface MindChains' `agent.Conn` is today; MindChains adapts the two (return types of interface methods must match exactly).
- `hub.TokenStore` holds `hub.Daemon` records (id, userID, name, token hash, timestamps); MindChains' store implements it over `store.Daemon`.
- `DownloadDir`, `PublicURL`'s install role and the `HelloReply` offer are gone: install and updates are the GitHub release's.
- `PublicURL` stays only where pairing needs the app's origin.

### 5. Page SDK (`sdk/client/`, npm `@agentd/client`)

Framework-agnostic TypeScript, no dependencies, ESM with `.d.ts`.

```ts
detect(opts?): Promise<Detected>
connectServer(d, {code}) / connectBrowser(d) / disconnect(d) / openSession(d, agent) / reasonText(reason)
installCommand({ app, code, agentd? }): string          // the curl line, with agentd's GitHub releases page as default
createLocalDaemon({ startPairing, cancelPairing, client? })   // observable controller: state, subscribe, detect, connect
```

`createLocalDaemon` is today's `createLocalDaemon.ts` without Vue: `{ state: {found, connecting, error, canConnect}, subscribe(fn), detect(), connect() }`. MindChains wraps it in Vue refs; another app wraps it in its own framework. Versions are semver; the server lists them in the packument.

### 6. How an app consumes it

- Go: `require github.com/nguyenvanduocit/agentd vX`. Until the repo is published, `replace github.com/nguyenvanduocit/agentd => ../agentd`.
- JS: `bun add https://github.com/nguyenvanduocit/agentd/releases/download/v<version>/agentd-client-<sdk version>.tgz`; the lockfile pins the file's sha512.
- The app mounts the hub, builds the install command with `installCommand`, and uses the controller in its settings page.

### 7. MindChains after the move

Deleted from MindChains: `daemon/`, `daemonrpc/`, `tray/`, `daemon-client/`, `internal/daemonhub/`, `scripts/{build-daemon,build-tray,sign-daemon}`, the daemon and tray stages of the `Dockerfile`, `DAEMON_DIR`, `/daemon/install.sh` and the tarball routes, `.release-signing/` (moved), the daemon CI jobs. Added: adapters (`hub.Conn` to `agent.Conn`, `hub.List` to `rpc.DaemonList`, the token store), the SDK dependency, the install command from the SDK, `go.mod`/`bunfig.toml` entries. The Vite proxy `/daemon` becomes `/agentd`.

### 8. Existing installs

Daemons installed as `mindchains-daemon` use the old names, the old namespace `/daemon` and update from MindChains. After the move MindChains no longer serves those: they stop updating and are refused. No in-place migration: the user installs `agentd` and runs `mindchains-daemon service uninstall`. The one daemon known to run (the author's, `com.mindchains.daemon`) is reinstalled by hand.

## Constraints

- No secret is committed: the release private key and any `.env` stay untracked.
- Nothing runs in Docker; verification is native on the author's Mac. Dockerfile edits are never built here.
- Nothing is pushed, published or deployed without the user's say-so.
- `daemonrpc` regeneration stays `socketrpc-gen` (TS client, Go server); `--client go` is not available.
- A page may connect only the app at its own origin.

## Open decisions (defaults proceed unless the user changes them)

1. **agentd server's public URL.** Baked into the daemon (`AGENTD_RELEASE_URL`) and the SDK (`installCommand`). Default for development: `http://127.0.0.1:7100`; a release needs a real domain. Blocks a release, not the build.
2. **Repo visibility and remote.** Default: local repo only, no GitHub repo created. A private repo needs credentials (`GOPRIVATE`, a token) in MindChains' Fly build.
3. **Existing installs.** Default: reinstall (section 8).
4. **Registry availability.** MindChains' frontend and image builds will depend on the agentd server being reachable for `bun install`. Default: accept it; the lockfile pins integrity.
5. **Commits.** Default: `git init` the new repo and leave everything uncommitted until asked.

## Order of work

1. Skeleton and moves with renames; daemon tests green in the new repo.
2. Release server (install.sh, latest.json, tarballs) with tests.
3. Daemon: update from the GitHub release, `--app`, `agentd-mcp`, `HelloReply` reduced.
4. Hub library: copy `internal/daemonhub`, cut its MindChains imports, port its tests.
5. Page SDK: `installCommand`, vanilla controller, build and pack; served by the server.
6. `examples/minimal-app` end to end through a real browser.
7. MindChains: consume, adapt, delete, simplify the Dockerfile; its tests and the `daemon` and `daemon-local` e2e scenarios pass against the local agentd.
8. Run it on this machine as before; independent review; fix pass.

## Acceptance

1. In `agentd`: daemon, tray, hub, server, SDK and example tests pass natively; `examples/minimal-app` connects a real daemon through a real browser.
2. `bun add <the release asset URL>` installs the SDK that `scripts/build-sdk.sh` builds.
3. The install command from the SDK installs, pairs and runs a daemon against the example app and against MindChains.
4. MindChains builds, lints and passes its Go, frontend and e2e (`daemon`, `daemon-local`) suites with none of the moved code left in its tree (`rg` finds no reference to the deleted paths).
5. An update offered by the releases page updates a running daemon; a wrong signature does not.
