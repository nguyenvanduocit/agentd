# @agentd/client

What a web page uses to find the agentd daemon on the user's computer, ask it to connect, and run an agent through it. No dependencies, ESM with types. It talks to the daemon's gateway on `127.0.0.1` (ports 47615 to 47624); the daemon shows the user a native dialog before anything is allowed. The protocol and its limits are in `daemon/README.md` ("Apps, web pages and the gateway").

## Install

The package is a file of agentd's GitHub release, `agentd-client-<version>.tgz`; install it by its URL:

```
bun add https://github.com/nguyenvanduocit/agentd/releases/download/v0.4.0/agentd-client-0.4.0.tgz       # npm: npm install <the same URL>
```

`v0.4.0` is the release it was published with, `0.4.0` the SDK's own version. Your lockfile records the file's sha512, and a
published file never changes: a fix is a new version in a new release.

## Use

Two ways to use the daemon: **server mode**, where the daemon connects to your app's server and the server runs the agents, and **browser mode**, where your page runs an agent on the user's computer itself.

### Server mode

```ts
import { createLocalDaemon, installCommand } from "@agentd/client"

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

### Browser mode

The page asks for its own grant, opens an agent, and speaks ACP to it with the official SDK ([`@agentclientprotocol/sdk`](https://www.npmjs.com/package/@agentclientprotocol/sdk), the app's own dependency): `session.readable` is the agent's stdout and `session.writable` its stdin, as `ndJsonStream` takes them.

```ts
import { PROTOCOL_VERSION, client, ndJsonStream } from "@agentclientprotocol/sdk"
import { createLocalDaemon, openSession } from "@agentd/client"

const local = createLocalDaemon({ mode: "browser" })
await local.detect()
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

Any MCP server whose transport reads and writes JSON-RPC messages works in place of `serveTools`. The agent may start the server again (a new `initialize` arrives on the same `session.mcp`). The bridge ends with the session and with the page's socket. A daemon older than 0.4 bridges no page server: `mcpServer()` throws and says so.

### Reference

- `createLocalDaemon(deps)` is a small observable, not tied to a framework: wrap `state` and `subscribe` in your framework's reactivity (a Vue `ref`, a React store). `deps` is `{ startPairing, cancelPairing }` (server mode, the default) or `{ mode: "browser" }`; both take an optional `client` to replace the calls to the daemon. `detect()` clears the last refusal, and is skipped while a connection waits for the dialog (the dialog returns the focus that triggers it). `connect()` puts the reason of a refusal in `state.error` in plain words. `disconnect()` takes back what this page was allowed and looks again.
- `openSession(d, agent)` gives an `AgentSession`: `readable` (stdout, each line ending in `\n`; it ends when the agent exits or the socket closes), `writable` (stdin; each complete line is sent, a partial one waits for its newline), and the line-level `write(line)`, `onFrame(cb)`, `onClose(cb)`, `close()`, `pid`, `cwd`. `readable` starts the first time it is taken and keeps no line from before: take it right after the session opens. `mcp` (`{ readable, writable }`) and `mcpServer(name)` (`{ name, command, args, env: [{ name, value }] }`, ACP's stdio MCP server) are for tools from the page.
- `AgentInfo` is `{ name, title, ready, reason }`; `title` is the name to show, the agent's `name` when the daemon is older than the title.
- `installCommand({ agentd, app, code })` is the curl line, or `undefined` while an origin or the code is not safe to paste into a shell.
- `AGENTD_COMMANDS` are what the user runs on their machine: `status`, `logs`, `start`.
- Lower level: `detect()`, `connectServer(d, { code })`, `connectBrowser(d)`, `disconnect(d)`, `reasonText(reason)`.

The app connected is the one at **your page's own origin**: the daemon refuses a request that names another server. An app whose API lives on another origin cannot use this yet.

Call `detect` when the user shows interest (a settings tab, a "connect" step), not at page load: Chrome shows a local-network prompt on the first request to the user's computer.

## Develop

```
bun install && bun test && bun run typecheck
scripts/build-sdk.sh <dir>       # from the repo root: <dir>/agentd-client-<version>.tgz, the file a release carries (same sources, same bytes)
```
