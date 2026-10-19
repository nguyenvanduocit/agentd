# agentd as a desktop app

agentd is something a person installs and opens, not a service they install from a terminal: `agentd.app` in
Applications. Opening it runs agentd, with its icon in the menu bar and a window; quitting it ends agentd and every agent
it runs; it opens again at login. The user asked for this ("làm cái này thành desktop app luôn đi, đừng làm daemon first
nữa") and left every choice below to the builder.

## Why

Daemon-first had no way back from a Stop: the menu's Stop ends the daemon and its icon, and launchd leaves it stopped, so
only a terminal could start it again (the agentd.app launcher of 0.3.1 patched that). Installing needed a terminal too
(`curl … | bash`). A desktop app has the lifecycle people already know: open it to start it, quit it to stop it.

## Goals

- macOS (Apple silicon and Intel): `agentd-darwin-<arch>.dmg`, drag to Applications, open. No terminal, no Node.js to
  install: the app carries its own.
- The app is the parent: it starts the engine, shows its state, and ends it on quit. Opening the app while it runs
  shows its window.
- Pairings, adapters and logs carry over from a CLI install (`~/.agentd`, `~/.config/agentd/daemon.json`).
- The app updates itself, signed (mygo's updater), engine included.
- A web app offers the download when it finds no agentd, then "Connect this computer" through the gateway, as today.

## Non-goals

- A Windows or Linux desktop app in this step (mygo builds both; the engine does not run on Windows, and Linux keeps the
  headless install).
- Node.js as an engine runtime: the engine is a Go binary; Node.js stays only for the ACP adapters (Node packages).

## Anti-goals

- Two engines on one machine (an app and a CLI service): the app takes over a CLI install's service.
- An app that quits and leaves agents running, or an engine that outlives its app.

## The parts

### 1. The app (`tray/`, Go, mygo)

The tray program becomes the app. One binary, two modes:

- **app** (an `agentd.app` bundle, or `agentd-tray app`): single instance (a second open shows the window), no Dock
  icon (`LSUIElement`), the menu-bar icon and the window of today's tray (`settings.go`). It starts the engine, takes its
  states and sends the user's choices over a socket pair (fd 3 of the engine), restarts it when it asks (exit 75) or
  crashes (with backoff), shows it stopped when it stops (exit 0, a Stop from a server) with Start in the window, and ends
  it on quit. "Open at login" is a setting of the window (`App.SetOpenAtLogin`), on by default; a launch at login starts
  with no window.
- **child** (`agentd-tray child`, what a CLI-installed daemon starts): today's tray, reading states on stdin.

### 2. The engine (`cmd/agentd`, Go)

The app runs `<resources>/engine/agentd run` (`AGENTD_APP_ENGINE` names another binary, for development and tests).
`agentd run` with `AGENTD_UI_FD=<fd>` talks to the app on that fd (JSON lines, the tray's protocol both ways) instead
of starting a tray program; it ends when the fd closes. The app runs it with `AGENTD_MANAGED=1` (a Restart from a server
exits 75, and the app starts it again). The engine of the app is bundled with no release server: it never updates
itself, the app's updates carry it. The desktop app is agentd.app, so the engine makes no launcher app of its own.

### 3. The bundle

`mygo build` makes the app from `tray/` with `tray/mygo.json`: name agentd, identifier `com.agentd.app`, the badge icon,
`LSUIElement`. Resources, per platform and merged by mygo into that platform's app (`resources/darwin-<arch>/engine/agentd`
is `Contents/Resources/engine/agentd`): `darwin-<arch>/engine/agentd`, the engine built by `scripts/build-desktop.sh`
(`CGO_ENABLED=0 go build ./cmd/agentd` for that architecture, with the app's version and no release key or release
URL), and `darwin-<arch>/node/` (the official Node.js build that `daemon/install.sh.in` pins, `bin/node` and npm, checked
against nodejs.org's SHASUMS256 when fetched). `node` gets the JIT entitlements it needs under the hardened runtime
(`macos.helperEntitlements`); the engine, a static Go binary, needs none. The app finds both with one rule: in the
resources, else in the platform's directory of them (the layout under `go run`). It puts `node/bin` first on the
engine's PATH, so the adapters and their npm installs use it; an app that carries no Node.js still runs the engine, and
its adapters report not ready.

### 4. Taking over a CLI install

On start, when the launchd job `com.agentd.daemon` exists (an `agentd service install`), the app boots it out and removes
its plist, and says so in the window. The data stays where it is, so the app's engine serves the same apps.

### 5. Updates and releases

One GitHub release per version, tagged `v<version>`, carries everything (`scripts/release.sh`): the app's disk images
under names that stay from release to release (`agentd-darwin-arm64.dmg`, `agentd-darwin-amd64.dmg`), its signed update
(`agentd-<version>-darwin-<arch>.tar.gz`, `update-darwin-<arch>.json`, and delta updates once a version is published),
the daemon tarball with `latest.json` and `install.sh`, and the SDK. The latest release then serves every reader at once:
the app's updater (mygo's, with a key of its own from `mygo keygen`, kept out of the repository like the release key),
installs from a terminal, and the SDK's download links. `tray/mygo.json` carries the app's version, set in the release's
commit. The window has "Check for Updates…"; the updater checks once a day on its own. The app's update carries the engine
(`engine/agentd`), so the engine never updates itself.

Signing is ad hoc until a Developer ID is chosen (a decision of the user): Gatekeeper then asks once (Open Anyway).

### 6. Web apps

`desktopDownloads()` of `@agentd/client` gives the links of the latest release's disk images (`appleSilicon`, `intel`);
a page shows them when it finds no agentd, and once the app is open, `detect()` finds it and `connect()` pairs it as it
pairs an installed daemon. `installCommand` stays for Linux and installs from a terminal.

## Order of work

1. Engine: the UI fd, app mode, `appentry.ts` removed.
2. App: modes, engine supervision, window controls (Start, Open at login), CLI takeover.
3. Bundle: `mygo.json`, `scripts/build-desktop.sh` (engine per architecture, Node, `mygo build`).
4. Updates and the release script; SDK download URL; docs.
5. Verify: unit tests for the pure parts, an integration test of the engine on an fd, the built app run against a
   sandboxed home, a real browser finding its engine.
