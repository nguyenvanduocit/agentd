# agentd as a desktop app

agentd is something a person installs and opens: `agentd.app` in Applications. Opening it runs agentd, with its icon in
the menu bar and a window; quitting it ends agentd and every agent it runs; it opens again at login.

## Goals

- macOS (Apple silicon and Intel): `agentd-darwin-<arch>.dmg`, drag to Applications, open. No terminal, no Node.js to
  install: the app carries its own.
- The app runs the engine in its own process, shows its state, and stops it on quit. Opening the app while it runs shows
  its window.
- The app updates itself, signed (mygo's updater), engine included.
- A web app offers the download when it finds no agentd, then "Connect this computer" through the gateway.

## Non-goals

- Linux, Windows, and agentd without agentd.app. agentd is the Mac app; `cmd/agentd` is the engine with no window, for
  the tests and the checks on Linux.
- Node.js as an engine runtime: the engine is Go; Node.js is only for the ACP adapters (Node packages).

## Anti-goals

- Two engines on one machine: the app takes over the launchd job of an agentd that an earlier version installed from a
  terminal.
- An app that quits and leaves agents running.

## The parts

### 1. The app (`app/`, Go, mygo)

One executable, `agentd`, in the bundle. Started with no arguments it is the app: single instance (a second open shows
the window), no Dock icon (`LSUIElement`), the menu-bar icon and the settings window (`settings.go`). It runs the engine
in its own process (`daemon/run`, a goroutine, see `one-process.md`), takes its state and the consent questions as Go
values, hands it the user's choices, restarts it when it asks (exit 75) or crashes (with backoff), shows it stopped when
it stops (exit 0, a Stop from a server) with Start in the window, and stops it on quit. "Start when I log in" is a
setting of the window (`App.SetOpenAtLogin`), on by default; a launch at login starts with no window. Started with a
command (`mcp`, `acp`, `apps`, `pair`, `run`, `version`) it is agentd's command line (`daemon/entry`).

### 2. The engine (`daemon/run`, Go)

`run.Daemon` with `Deps.UI` (the app's link, see `daemon/README.md`) shows its state to the app and asks the user in the
app's window. The app gives it `AGENTD_MANAGED=1` (a Restart from a server exits 75, and the app starts it again). The
engine has no release server: the app's updates carry it.

### 3. The bundle

`mygo build` makes the app from `app/` with `app/mygo.json`: name agentd, identifier `com.agentd.app`, the badge icon,
`LSUIElement`. The version of the engine is the version in `app/mygo.json` (`app/version.go`). Resources, per platform and
merged by mygo into that platform's app (`resources/darwin-<arch>/node` is `Contents/Resources/node`): the official
Node.js build that `scripts/build-desktop.sh` pins (`bin/node` and npm, checked against nodejs.org's SHASUMS256 when
fetched). `node` gets the JIT entitlements it needs under the hardened runtime (`macos.helperEntitlements`). The app
finds it with one rule: in the resources, else in the platform's directory of them (the layout under `go run`). It puts
`node/bin` first on the engine's PATH, so the adapters and their npm installs use it; an app that carries no Node.js
still runs the engine, and its adapters report not ready.

### 4. Taking over a terminal install

On start, when the launchd job `com.agentd.daemon` exists (an agentd that an earlier version installed from a terminal),
the app boots it out and removes its plist. The data stays where it is, so the app's engine serves the same apps.

### 5. Updates and releases

One GitHub release per version, tagged `v<version>`, carries the Mac app (`scripts/release.sh`): the disk images
under names that stay from release to release (`agentd-darwin-arm64.dmg`, `agentd-darwin-amd64.dmg`), its signed update
(`agentd-<version>-darwin-<arch>.tar.gz`, `update-darwin-<arch>.json`, and delta updates once a version is published),
and the SDK. The app's updater (mygo's, with a key of its own from `mygo keygen`, kept out of the repository) and the
SDK's download links read that release. `app/mygo.json` carries the app's version, set in the release's commit. The
window has "Check for Updates…"; the updater checks once a day on its own. The app's update carries the engine (it is in
the executable).

`mygo build` signs the app, with Node inside it, with the Developer ID Application certificate of team 53NJTFJB2R
(`macos.signingIdentity` in `app/mygo.json`) and the hardened runtime, submits the disk image to Apple's notary service
with the `notary` keychain profile (`macos.notarize`), and staples the ticket, so Gatekeeper opens the app without
asking. The profile holds an App Store Connect API key (`xcrun notarytool store-credentials notary --key …`); `.env`
keeps the key's ID, issuer and contents for another machine.

### 6. Web apps

`desktopDownloads()` of `@agentd/client` gives the links of the latest release's disk images (`appleSilicon`, `intel`);
a page shows them on a Mac when it finds no agentd, and once the app is open, `detect()` finds it and `connect()` pairs it.
Linux, Windows and phones get no install.
