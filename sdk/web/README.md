# @agentd/client

What a web page uses to find the agentd daemon on the user's computer, ask it to connect, and run an agent through it. No dependencies, ESM with types. It talks to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624); the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

## Install

The package is a file of agentd's GitHub release, `agentd-client-<version>.tgz`; install it by its URL:

```
bun add https://github.com/nguyenvanduocit/agentd/releases/download/v0.6.0/agentd-client-0.5.0.tgz       # npm: npm install <the same URL>
```

`v0.6.0` is the release that carries it, `0.5.0` the SDK's own version (the same file is in every release since v0.5.0). Your lockfile records the file's sha512, and a
published file never changes: a fix is a new version in a new release.

## Use

Two ways to use the daemon: **server mode**, where the daemon connects to your app's server and the server runs the agents, and **browser mode**, where your page runs an agent on the user's computer itself.

### Server mode

```ts
import { createLocalDaemon, desktopDownloads, installCommand, recommendInstall } from "@agentd/client"

// 1. The daemon on this computer: is it there, and can the user connect it to this app?
const local = createLocalDaemon({
  startPairing: async () => (await myServer.startDaemonPairing()).code, // the app's own call: a one-time code
  cancelPairing: () => myServer.cancelDaemonPairing(),
})
local.subscribe((s) => render(s)) // { phase, found, connecting, error, canConnect, reason?, message?, incompatible? }
const stop = local.watch()        // when the settings tab opens: looks now and every 3 s until it finds agentd; stop() when it closes
if (local.state.canConnect) await local.connect() // the dialog opens on the user's computer

// 2. No daemon here: agentd.app for the user's Mac. Once it is open, watch() finds it and connect() pairs it.
const how = recommendInstall() // "dmg" on a Mac, "script" on Linux, "unsupported" elsewhere (Windows: use WSL)
if (how === "dmg") offer(desktopDownloads()) // { appleSilicon, intel }: the disk images of the latest release
// On Linux or from a terminal, the one command that installs it, pairs it with this app and starts it.
if (how === "script") offer(installCommand({ app: location.origin, code }))
```

### States

`state.phase` says where the connection stands, so an interface needs one switch and no combination of flags:

| `phase` | Meaning |
|---------|---------|
| `detecting` | The first look has not ended. Until `detect()` or `watch()` is called nothing has been looked for. |
| `absent` | No daemon answered: agentd is not installed, not running, or the browser blocked the call (Chrome's local-network prompt, Safari). The page cannot tell which, so say all three. |
| `available` | A daemon is here and has not been allowed to serve this app (browser mode: this page) yet. `canConnect` is true. |
| `connecting` | A connection waits for the user's answer in the daemon's dialog. |
| `connected` | The daemon serves this app (browser mode: this page). |
| `refused` | The last connection did not happen, or the daemon cannot talk to this page. |

A refusal sets `reason` (the daemon's: `denied`, `timeout`, `cooldown`, `busy`, `no-dialog`, `pairing`, `peer` …; a reason from a newer daemon arrives as it was sent), `message` (the daemon's own words, when it gave any) and `error` (a sentence for a person, never empty in this phase; translate from `reason` if you want other words). The next look clears it. A failed `disconnect()` is a refusal too.

A daemon states the version of its page protocol (`found.protocol`; a daemon that predates the statement sends none). When it is not one of `SUPPORTED_PROTOCOLS` the phase is `refused`, `incompatible` holds a sentence ("This agentd is too old for this page. Update agentd."), `error` repeats it and `canConnect` is false.

`watch(everyMs = 3000)` looks now and then again every `everyMs` while the phase is `detecting`, `absent`, `available` or `refused` because of `incompatible`, so a person who installs agentd and comes back to the page is found without a reload. It looks no more while a connection waits, once connected, and while a refusal is on the screen (a look would clear it before it is read). Listeners hear of a change only. It returns the function that stops it.

### Browser mode

The page asks for its own grant, opens an agent, and speaks ACP to it with the official SDK ([`@agentclientprotocol/sdk`](https://www.npmjs.com/package/@agentclientprotocol/sdk), the app's own dependency): `session.readable` is the agent's stdout and `session.writable` its stdin, as `ndJsonStream` takes them.

```ts
import { PROTOCOL_VERSION, client, ndJsonStream } from "@agentclientprotocol/sdk"
import { createLocalDaemon, openSession } from "@agentd/client"

const local = createLocalDaemon({ mode: "browser" })
local.watch()
if (local.state.canConnect) await local.connect() // canConnect: a daemon is here and this page has no grant yet

const found = local.state.found
if (found.found) {
  const session = await openSession(found, "claude") // found.agents lists them: name, title, ready, reason
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
}

await local.disconnect() // takes this page's grant back; open sessions end
```

`new ClientSideConnection((agent) => myClient, ndJsonStream(session.writable, session.readable))` works over the same streams.

### Tools from the page

A page can give its agent tools of its own (navigate, search, read what is on screen): the page runs an MCP server, and the agent reaches it through the daemon. `session.mcp` is the page's end, as bytes of JSON-RPC lines like `readable` and `writable`; `session.mcpServer(name)` is the stdio server to list in `session/new`, which the agent starts and which relays its MCP lines to `session.mcp`. Take `session.mcp` before `session/new`: lines that come before it is taken are not kept.

```ts
import { ndJsonStream, type Stream } from "@agentclientprotocol/sdk"

const session = await openSession(found, "claude")
void serveTools(ndJsonStream(session.mcp.writable, session.mcp.readable)) // JSON-RPC messages both ways
// ... initialize as above, then:
const { sessionId } = await acp.agent.request("session/new", { cwd: session.cwd, mcpServers: [session.mcpServer("page")] })

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

A page's grant lasts 30 days. After that `granted.browser` is `false` again, open sessions of the page end, and `connectBrowser` shows the dialog again; the app's grant (server mode) lasts until the user disconnects it.

Any MCP server whose transport reads and writes JSON-RPC messages works in place of `serveTools`. The agent may start the server again (a new `initialize` arrives on the same `session.mcp`). The bridge ends with the session and with the page's socket. A daemon older than 0.4 bridges no page server: `mcpServer()` throws and says so.

### Reference

- `createLocalDaemon(deps)` is a small observable, not tied to a framework: wrap `state` and `subscribe` in your framework's reactivity (a Vue `ref`, a React store). `deps` is `{ startPairing, cancelPairing }` (server mode, the default) or `{ mode: "browser" }`; both take an optional `client` to replace the calls to the daemon. `detect()` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it). `connect()` puts the refusal in `state` (`phase: "refused"`, `reason`, `message`, and `error` in plain words). `watch(everyMs)` and the phases are described under "States". `disconnect()` takes back what this page was allowed and looks again; if the daemon could not do it, the refusal is in `state`. Both take an optional `interval` (`(tick, everyMs) => stop`, default `setInterval`) for tests.
- `openSession(d, agent)` gives an `AgentSession`: `readable` (stdout, each line ending in `\n`; it ends when the agent exits or the socket closes), `writable` (stdin; each complete line is sent, a partial one waits for its newline), and the line-level `write(line)`, `onFrame(cb)`, `onClose(cb)`, `close()`, `pid`, `cwd`. `readable` starts the first time it is taken and keeps no line from before: take it right after the session opens. `mcp` (`{ readable, writable }`) and `mcpServer(name)` (`{ name, command, args, env: [{ name, value }] }`, ACP's stdio MCP server) are for tools from the page.
- `AgentInfo` is `{ name, title, ready, reason }`; `title` is the name to show, the agent's `name` when the daemon is older than the title.
- `desktopDownloads({ agentd })` is `{ appleSilicon, intel }`, the links of agentd.app's disk images in the latest release, or `undefined` while the releases page is not a plain http(s) URL.
- `installCommand({ agentd, app, code })` is the curl line, or `undefined` while an origin or the code is not safe to paste into a shell.
- `AGENTD_COMMANDS` are what the user of an install from the terminal runs on their machine: `status`, `logs`, `start`.
- `recommendInstall({ os })` is `"dmg"` (a Mac: agentd.app), `"script"` (Linux: the install command) or `"unsupported"` (Windows, phones: use WSL on Windows). Without an argument it reads the browser. `arch` is accepted and does not change the answer: both disk images are offered.
- Lower level: `detect()` (a found daemon carries `protocol` when it states one; `SUPPORTED_PROTOCOLS` are the ones this SDK speaks), `connectServer(d, { code })`, `connectBrowser(d)`, `reasonText(reason)` (a sentence for every reason, a reason from a newer daemon included), and `disconnect(d)`, which answers `{ disconnected: true }` or `{ disconnected: false, reason, message? }`.

The app connected is the one at **your page's own origin**: the daemon refuses a request that names another server. An app whose API lives on another origin cannot use this yet.

Call `detect` or `watch` when the user shows interest (a settings tab, a "connect" step), not at page load: Chrome shows a local-network prompt on the first request to the user's computer.

## Develop

```
bun install && bun test && bun run typecheck
scripts/build-sdk.sh <dir>       # from the repo root: <dir>/agentd-client-<version>.tgz, the file a release carries (same sources, same bytes)
```
