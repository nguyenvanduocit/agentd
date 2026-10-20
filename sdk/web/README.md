# @agentd/client

What a web page uses to find the agentd daemon on the user's computer, ask it to connect, and run an agent through it. No dependencies, ESM with types. It talks to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624); the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

## Install

The package is a file of agentd's GitHub release, `agentd-client-<version>.tgz`; install it by its URL:

```
bun add https://github.com/nguyenvanduocit/agentd/releases/download/v0.8.0/agentd-client-0.6.0.tgz       # npm: npm install <the same URL>
```

The release in the URL is the one that carries the file, and the number in the file's name is the SDK's own version. Your lockfile records the file's sha512, and a published file never changes: a change of the SDK is a new version in a new release (`scripts/release.sh` refuses a release whose SDK sources changed under the old version).

This README describes the SDK of the source tree. What it marks *since 0.7.0* is in the SDK from agentd 0.7.0 (SDK version 0.6.0) on, and not in the file the line above installs; `scripts/build-sdk.sh <dir>` builds the SDK of this tree, and `scripts/pins.sh` moves the line above to a new release.

## Use

Two ways to use the daemon: **server mode**, where the daemon connects to your app's server and the server runs the agents, and **browser mode**, where your page runs an agent on the user's computer itself.

### Server mode

`myServer` below is your own API client: its two calls reach your server, which mints a one-time pairing code (with `sdk/goserver`: `StartPairing`) and cancels it.

```ts
import { createLocalDaemon, desktopDownloads, installCommand, recommendInstall } from "@agentd/client"

// 1. The daemon on this computer: is it there, and can the user connect it to this app?
const local = createLocalDaemon({
  startPairing: async () => (await myServer.startDaemonPairing()).code, // the app's own call: a one-time code
  cancelPairing: () => myServer.cancelDaemonPairing(),
})
local.subscribe(render, { immediate: true }) // draw the state now, and again after every change (the options are since 0.7.0)
const stop = local.watch() // when the settings tab opens: looks now and every 3 s; stop() when it closes
document.querySelector("#connect")?.addEventListener("click", () => void local.connect()) // the dialog opens on the user's computer

// 2. No daemon here: agentd.app for the user's Mac. Once it is open, watch() finds it and connect() pairs it.
const how = recommendInstall() // "dmg" on a Mac, "script" on Linux, "unsupported" elsewhere (Windows: use WSL)
if (how === "dmg") offer(desktopDownloads()) // { appleSilicon, intel }: the disk images of the latest release
// On Linux or from a terminal, the one command that installs it, pairs it with this app and starts it.
if (how === "script") {
  const { code } = await myServer.startDaemonPairing()
  offer(installCommand({ app: location.origin, code }))
}
```

`watch()` is not instant: until its first look ends the phase is `detecting` and nothing can be connected, so connect from a click (when the phase is `available`), not right after `watch()`.

### The states, in one switch

`state.phase` says where the connection stands, so an interface needs one switch and no combination of flags. The type is one case per phase, so what a case reads is there without a check: `s.found.version` in `connected`, `s.reason` in `refused`.

| `phase` | Meaning |
|---------|---------|
| `detecting` | The first look has not ended. Until `detect()` or `watch()` is called nothing has been looked for. |
| `absent` | No daemon answered: agentd is not installed, not running, or the browser blocked the call (Chrome's local-network prompt, Safari). The page cannot tell which, so say all three. When a daemon did answer and refused this page, `reason` and `error` say why (see the table below). |
| `available` | A daemon is here and has not been allowed to serve this app (browser mode: this page) yet. `canConnect` is true. |
| `connecting` | A connection waits for the user's answer in the daemon's dialog. |
| `connected` | The daemon serves this app (browser mode: this page). |
| `refused` | The last connection did not happen (`reason`, `message`, `error`), or the daemon cannot talk to this page (`reason: "incompatible"`). |

A whole panel, as a function of the state:

```ts
import { recommendInstall, type LocalState } from "@agentd/client"

type Action = "install" | "download" | "connect" | "disconnect"

function panel(s: LocalState): { title: string; body: string; action?: Action } {
  switch (s.phase) {
    case "detecting":
      return { title: "Looking for agentd…", body: "" }
    case "absent":
      return {
        title: "agentd is not running on this computer",
        body: s.error || "It is not installed, not running, or your browser blocked the page from reaching it.",
        ...(recommendInstall() === "unsupported" ? {} : { action: recommendInstall() === "dmg" ? ("download" as const) : ("install" as const) }),
      }
    case "available":
      return { title: "agentd is running", body: `agentd ${s.found.version} is ready to be connected to this app.`, action: "connect" }
    case "connecting":
      return { title: "Waiting for you", body: "Answer the dialog that opened on this computer." }
    case "connected":
      return { title: "Connected", body: `agentd ${s.found.version} serves this app.`, action: "disconnect" }
    case "refused":
      return { title: "Not connected", body: s.error, ...(s.reason === "incompatible" ? {} : { action: "connect" as const }) }
  }
}
```

A refusal sets `reason` (see the table below; a reason from a newer daemon arrives as it was sent), `message` (the daemon's own words, when it gave any), `retryAfter` (seconds to wait, for `cooldown` and `locked`) and `error` (a sentence for a person, never empty in this phase; translate from `reason` if you want other words). The next look clears it. A failed `disconnect()` is a refusal too.

A daemon states the version of its page protocol (`found.protocol`; a daemon that predates the statement sends none). When it is not one of `SUPPORTED_PROTOCOLS` the phase is `refused`, `reason` is `incompatible`, `incompatible` holds a sentence ("This agentd is too old for this page. Update agentd."), `error` repeats it and `canConnect` is false.

`watch(everyMs = 3000)` looks now and then again every `everyMs`, so a person who installs agentd and comes back to the page is found without a reload, and a daemon that left or a grant that lapsed is noticed (since 0.7.0: a daemon that does not answer once while connected is given a second look before the page says it left). It looks no more while a connection waits, and while a refusal is on the screen (a look would clear it before it is read). Listeners hear of a change only. It returns the function that stops it.

### Why a request is refused

`reasonText(reason)` has a sentence for each; the list is the daemon's, kept in one file for the gateway and both SDKs (`contract/testdata/page-reasons.json`). The daemon sends the legacy `reason` and, for three cases, a more specific `code`; the SDK reports the code when it knows it (since 0.7.0), so an older SDK still gets a reason it knows.

| `reason` | Meaning | What to do |
|----------|---------|------------|
| `denied` | The user declined in the dialog. | Say so; let them try again. |
| `timeout` | Nobody answered the dialog in time (it counts as declined). | Ask them to answer it. |
| `cooldown` | They declined a moment ago. | Wait `retryAfter` seconds. |
| `locked` | Several requests were declined in a row; the daemon asks nobody for a few minutes. | Wait `retryAfter` seconds. |
| `busy` | Another request waits for an answer on that computer. | Ask them to answer that one first. |
| `no-dialog` | The computer cannot show the dialog (no graphical session), so nothing can be granted. | Use a computer with a screen. |
| `no-folder` | The user pressed Allow and chose no folder. | Ask again; a folder is needed. |
| `pairing` | The app's server refused the pairing code (server mode). | Start the connection again for a new code. |
| `not-saved` | agentd could not save the connection. | Look at its log. |
| `origin` | The page's address is one agentd does not accept: no `Origin` header (a script outside a browser), a path, or a scheme other than https and loopback http. | Fix the address; see "Test against a real daemon". |
| `host` | The address the page used to reach agentd is not `127.0.0.1` or `localhost`. | Open the page from its usual address. |
| `malformed` | agentd did not understand the request. | Update agentd. |
| `not-found` | This agentd does not know the request. | Update agentd. |
| `error` | agentd failed; also the reason for a failure of the app's own pairing call. | Look at its log. |
| `peer` | agentd runs as another user than the program that asked. | Run both as one user. |
| `unreachable` | The SDK itself: nothing answered. | Check that agentd is running. |
| `cancelled` | The SDK itself: you stopped the request with an `AbortSignal`. The dialog may still be open; answering Allow there still connects. | Look again with `detect()`. |
| `incompatible` | The SDK itself: the daemon speaks a protocol this page does not. | Update agentd or the app. |

### Browser mode

The page asks for its own grant, opens an agent, and speaks ACP to it with the official SDK ([`@agentclientprotocol/sdk`](https://www.npmjs.com/package/@agentclientprotocol/sdk), the app's own dependency): `session.readable` is the agent's stdout and `session.writable` its stdin, as `ndJsonStream` takes them.

```ts
import { PROTOCOL_VERSION, client, ndJsonStream } from "@agentclientprotocol/sdk"
import { createLocalDaemon } from "@agentd/client"

const local = createLocalDaemon({ mode: "browser" })
local.subscribe(render, { immediate: true })
local.watch()

// The user clicks "Connect this computer"; the phase was "available" while the button was offered.
document.querySelector("#connect")?.addEventListener("click", async () => {
  if (!(await local.connect())) return // refused: local.state.error says why, in plain words
  const session = await local.openSession("claude") // local.state.found.agents lists them: name, title, ready, reason (since 0.7.0; before: openSession(found, "claude"))
  const acp = client({ name: "my page" })
    .onNotification("session/update", ({ params }) => render(params.update))
    .onRequest("session/request_permission", async ({ params }) => {
      const optionId = await askTheUser(params)
      return { outcome: optionId === null ? { outcome: "cancelled" } : { outcome: "selected", optionId } }
    })
    .connect(ndJsonStream(session.writable, session.readable))
  await acp.agent.request("initialize", { protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} })
  const { sessionId } = await acp.agent.request("session/new", { cwd: session.cwd, mcpServers: [] })
  await acp.agent.request("session/prompt", { sessionId, prompt: [{ type: "text", text: "Hello" }] })
})

document.querySelector("#disconnect")?.addEventListener("click", () => void local.disconnect()) // takes this page's grant back; open sessions end
```

`new ClientSideConnection((agent) => myClient, ndJsonStream(session.writable, session.readable))` works over the same streams. `local.openSession(agent)` rejects with a `SessionError` (`reason`: `unreachable` when the socket could not be opened, `error` when the daemon refused the open, with its words in `message`; since 0.7.0). Without a `LocalDaemon`, `openSession(found, agent)` opens one on a daemon that `detect()` found.

### Tools from the page

A page can give its agent tools of its own (navigate, search, read what is on screen): the page runs an MCP server, and the agent reaches it through the daemon. `session.mcp` is the page's end, as bytes of JSON-RPC lines like `readable` and `writable`; `session.mcpServer(name)` is the stdio server to list in `session/new`, which the agent starts and which relays its MCP lines to `session.mcp`. Take `session.mcp` before `session/new`: lines that come before it is taken are not kept.

```ts
import { PROTOCOL_VERSION, client, ndJsonStream, type Stream } from "@agentclientprotocol/sdk"
import { createLocalDaemon } from "@agentd/client"

const local = createLocalDaemon({ mode: "browser" })
await local.detect()
const session = await local.openSession("claude")
void serveTools(ndJsonStream(session.mcp.writable, session.mcp.readable)) // JSON-RPC messages both ways

const acp = client({ name: "my page" }).connect(ndJsonStream(session.writable, session.readable))
await acp.agent.request("initialize", { protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} })
const { sessionId } = await acp.agent.request("session/new", { cwd: session.cwd, mcpServers: [session.mcpServer("page")] })
await acp.agent.request("session/prompt", { sessionId, prompt: [{ type: "text", text: "What page am I on?" }] })

async function serveTools({ readable, writable }: Stream) {
  const out = writable.getWriter()
  for await (const msg of readable) {
    if (!("method" in msg) || !("id" in msg)) continue // a notification, or an answer
    const result =
      msg.method === "initialize"
        ? { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "page", version: "1" } }
        : msg.method === "tools/list"
          ? { tools: [{ name: "current_page", inputSchema: { type: "object" } }] }
          : msg.method === "tools/call"
            ? { content: [{ type: "text", text: location.pathname }] }
            : undefined
    await out.write(result === undefined ? { jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: `no method ${msg.method}` } } : { jsonrpc: "2.0", id: msg.id, result })
  }
}
```

What a page may send: in any `session/...` request, `mcpServers` is `[]` or the entry of `session.mcpServer(name)` and nothing else (another command, an argument, a variable, or an http/sse server is refused), and `session/new`, `session/load`, `session/resume`, `session/fork` and `authenticate` carry no `_meta`. The daemon answers a refused request with JSON-RPC error `-32602` (invalid params) and never passes it to the agent; a line that is not a single JSON-RPC message gets an `error` message, `not passed to the agent: ...`. A page's grant is for the page's own tools, not for choosing which programs the agent starts or how the agent is configured. A server app that connects the daemon to its own server (server mode) has no such limit.

A page's grant lasts 30 days. After that `granted.browser` is `false` again, open sessions of the page end, `watch()` notices (the phase goes from `connected` to `available`), and `connect()` shows the dialog again; the app's grant (server mode) lasts until the user disconnects it.

Any MCP server whose transport reads and writes JSON-RPC messages works in place of `serveTools`. The agent may start the server again (a new `initialize` arrives on the same `session.mcp`). The bridge ends with the session and with the page's socket. A daemon older than 0.4 bridges no page server: `mcpServer()` throws and says so.

### Test against a real daemon (no browser)

Everything above runs in a script, so a test can talk to a real agentd. The daemon requires an `Origin` header, which a browser sets and a script does not: give the SDK a `fetch` and a `WebSocket` that add it (checked with bun 1.3 and Node 26, whose global `WebSocket` takes a `headers` option; since 0.7.0 `createLocalDaemon` takes them, with the ports to look on).

```ts
import { createLocalDaemon, type FetchLike } from "@agentd/client"

const ORIGIN = "http://127.0.0.1:5199" // https, or http on loopback; no path, no trailing slash

const withOrigin: FetchLike = (url, init) => {
  const headers = new Headers(init?.headers)
  headers.set("Origin", ORIGIN)
  return fetch(url, { ...init, headers })
}

class OriginWebSocket extends WebSocket {
  constructor(url: string | URL) {
    super(url, { headers: { Origin: ORIGIN } } as unknown as string[]) // the second argument is bun's and Node's, not the DOM's
  }
}

const local = createLocalDaemon({ mode: "browser", ports: [47681], fetch: withOrigin, WebSocket: OriginWebSocket })
await local.detect()
if (local.state.phase === "available") await local.connect() // the daemon's dialog: the test program below answers it
if (local.state.phase !== "connected") throw new Error(local.state.error)

const session = await local.openSession("claude")
session.onFrame((f) => console.log(f.kind, f.data))
session.write('{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}')
```

The daemon of the test is started with its own home, so it never touches the real one. The variables marked *tests only* are not a feature of agentd; they exist so that a test needs neither a screen nor a real agent:

```sh
T=$(mktemp -d)
cat >"$T/allow.sh" <<'EOF'
#!/bin/sh
# The program that answers the dialog: it gets the title and the text as arguments. Exit 0 is Allow, and what it
# prints, one absolute folder per line, is the folders chosen (an Allow without a folder is refused as "no-folder").
echo "$PWD"
EOF
cat >"$T/agent.sh" <<'EOF'
#!/bin/sh
# A fake agent: answers every line with an ACP initialize result.
while read -r line; do printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}\n'; done
EOF
chmod +x "$T/allow.sh" "$T/agent.sh"

# The engine itself, not the launcher: the launcher looks for it under AGENTD_HOME, which this run has moved.
ENGINE=~/.agentd/daemon/current/agentd   # after an install from a terminal; or: go build -o agentd ./cmd/agentd, in the agentd repository

HOME=$T AGENTD_HOME=$T/agentd XDG_CONFIG_HOME=$T/config XDG_STATE_HOME=$T/state \
AGENTD_LOCAL_PORT=47681 \
AGENTD_CONSENT=$T/allow.sh \
AGENTD_AGENT_CLAUDE=$T/agent.sh \
AGENTD_ADAPTERS=off AGENTD_TRAY=off AGENTD_RELEASE_URL= \
  "$ENGINE" run
```

`AGENTD_CONSENT` (tests only) stands for both dialogs, `AGENTD_AGENT_<NAME>` (tests only) replaces the command line of an agent and counts it as ready, `AGENTD_LOCAL_PORT` moves the gateway off the ports of a real agentd, and `AGENTD_ADAPTERS=off` stops the daemon from installing the agents' adapters. Exit 1 from the dialog program is a Deny: use it to test the `denied`, `cooldown` and `locked` paths without clicking.

### The daemons list of your server

An app's server (`sdk/goserver`) answers its page with the user's daemons and the pending pairing code. `DaemonList`, `DaemonInfo` and `Pairing` type that JSON (since 0.7.0; a test compares them with the Go structs):

```ts
import type { DaemonList } from "@agentd/client"

const list = (await (await fetch("/api/daemons")).json()) as DaemonList // your own route: goserver's List as JSON
for (const d of list.daemons) {
  const behind = list.latestVersion !== "" && d.version !== "" && d.version !== list.latestVersion
  console.log(`${d.name}: ${d.state}${d.incompatible ? ` (${d.incompatible})` : ""}${behind ? ", an update is available" : ""}`)
}
```

### Reference

- `createLocalDaemon(deps)` is a small observable, not tied to a framework: wrap `state` and `subscribe` in your framework's reactivity (a Vue `ref`, a React store). `deps` is `{ startPairing, cancelPairing }` (server mode, the default) or `{ mode: "browser" }`; both take an optional `client` to replace the calls to the daemon, or (since 0.7.0) `ports`, `fetch`, `timeoutMs` and `WebSocket` for the calls it makes itself. `detect()` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it). `connect()` puts the refusal in `state`. `watch(everyMs)` and the phases are described above. `disconnect()` takes back what this page was allowed and looks again; if the daemon could not do it, the refusal is in `state`. Both take an optional `interval` (`(tick, everyMs) => stop`, default `setInterval`) for tests. `subscribe(listener, { immediate })` and `openSession(agent)` are since 0.7.0.
- `openSession(d, agent)` gives an `AgentSession`: `readable` (stdout, each line ending in `\n`; it ends when the agent exits or the socket closes), `writable` (stdin; each complete line is sent, a partial one waits for its newline), and the line-level `write(line)`, `onFrame(cb)`, `onClose(cb)`, `close()`, `pid`, `cwd`. `readable` starts the first time it is taken and keeps no line from before: take it right after the session opens. `mcp` (`{ readable, writable }`) and `mcpServer(name)` (`{ name, command, args, env: [{ name, value }] }`, ACP's stdio MCP server) are for tools from the page.
- `AgentInfo` is `{ name, title, ready, reason }`; `title` is the name to show, the agent's `name` when the daemon is older than the title.
- `desktopDownloads({ agentd })` is `{ appleSilicon, intel }`, the links of agentd.app's disk images in the latest release, or `undefined` while the releases page is not a plain http(s) URL.
- `installCommand({ agentd, app, code })` is the curl line, or `undefined` while an origin or the code is not safe to paste into a shell. Without `app` and `code` (browser mode, since 0.7.0) it only installs and starts agentd.
- `AGENTD_COMMANDS` are what the user of an install from the terminal runs on their machine: `status`, `logs`, `start`, each by the path of the launcher the installer writes, `~/.agentd/bin/agentd`, because neither install puts `agentd` on PATH. The desktop app has no command line: it shows these in its own window.
- `recommendInstall({ os })` is `"dmg"` (a Mac: agentd.app), `"script"` (Linux: the install command) or `"unsupported"` (Windows, phones: use WSL on Windows). Without an argument it reads the browser. `arch` is accepted and does not change the answer: both disk images are offered.
- Lower level: `detect()` (a found daemon carries `protocol` when it states one; when no daemon answered but one refused this page, `{ found: false, refused }` says why, since 0.7.0; `SUPPORTED_PROTOCOLS` are the ones this SDK speaks), `connectServer(d, { code })`, `connectBrowser(d)` (both answer `{ granted: true }` or `{ granted: false, reason, message?, retryAfter? }`, and take `{ fetch, signal }`), `reasonText(reason)` (a sentence for every reason, one from a newer daemon included), and `disconnect(d)`, which answers `{ disconnected: true }` or `{ disconnected: false, reason, message? }`.

The app connected is the one at **your page's own origin**: the daemon refuses a request that names another server. An app whose API lives on another origin cannot use this yet.

Call `detect` or `watch` when the user shows interest (a settings tab, a "connect" step), not at page load: Chrome shows a local-network prompt on the first request to the user's computer.

## Develop

```
bun install && bun test && bun run typecheck
scripts/build-sdk.sh <dir>       # from the repo root: <dir>/agentd-client-<version>.tgz, the file a release carries (same sources, same bytes)
```

`bun test` compiles every `ts` block of this README against the sources (`src/readme.test.ts`), so the examples above cannot drift from the API.
