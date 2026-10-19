# @agentd/client

What a web page uses to find the agentd daemon on the user's computer, ask it to connect, and run an agent through it. No dependencies, ESM with types. It talks to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624); the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

## Install

The package is the [agentd-client](https://github.com/nguyenvanduocit/agentd-client) repository on GitHub; each version is a tag.

```
bun add github:nguyenvanduocit/agentd-client#v0.2.1       # npm: npm install github:nguyenvanduocit/agentd-client#v0.2.1
```

Your lockfile pins the commit of the tag. A tag is never moved: a fix is a new version.

## Use

```ts
import { createLocalDaemon, installCommand, reasonText } from "@agentd/client"

// 1. The daemon on this computer: is it there, and can the user connect it to this app?
const local = createLocalDaemon({
  startPairing: async () => (await myServer.startDaemonPairing()).code, // the app's own call: a one-time code
  cancelPairing: () => myServer.cancelDaemonPairing(),
})
local.subscribe((s) => render(s)) // { found, connecting, error, canConnect }
await local.detect()              // when the settings tab opens, and when the window gets focus
if (local.state.canConnect) await local.connect() // the dialog opens on the user's computer

// 2. No daemon here: the one command that installs it, pairs it with this app and starts it.
const command = installCommand({ agentd: "https://<your agentd server>", app: location.origin, code })
```

- `createLocalDaemon({ startPairing, cancelPairing })` is a small observable, not tied to a framework: wrap `state` and `subscribe` in your framework's reactivity (a Vue `ref`, a React store). `detect()` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it).
- `installCommand({ agentd, app, code })` is the curl line, or `undefined` while an origin or the code is not safe to paste into a shell.
- `AGENTD_COMMANDS` are what the user runs on their machine: `status`, `logs`, `start`.
- Lower level: `detect()`, `connectServer(d, { code })`, `connectBrowser(d)`, `disconnect(d)`, `openSession(d, agent)`, `reasonText(reason)`.

The app connected is the one at **your page's own origin**: the daemon refuses a request that names another server. An app whose API lives on another origin cannot use this yet.

Call `detect` when the user shows interest (a settings tab, a "connect" step), not at page load: Chrome shows a local-network prompt on the first request to the user's computer.

## Develop

```
bun install && bun test && bun run typecheck
scripts/build-sdk.sh <dir>       # from the repo root: the package as an app installs it, in <dir>
scripts/release-sdk.sh <path of the agentd-client checkout>   # commits and tags v<version> there (pushing is yours)
```
