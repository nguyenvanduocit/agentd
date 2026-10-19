import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { spawn, spawnSync, type ChildProcess } from "node:child_process"
import { once } from "node:events"
import { readFileSync, statSync, writeFileSync } from "node:fs"
import { connect, type Socket } from "node:net"
import { dirname, join } from "node:path"
import { WebSocket } from "ws"
import { build, groupAlive, Sandbox, sh, TestDaemon, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const PAGE = "http://localhost:5173"
const OTHER = "http://localhost:5174"
let daemon: TestDaemon | undefined
let box: Sandbox | undefined
const sockets: WebSocket[] = []
const pids: number[] = []

const bridges: ChildProcess[] = []

afterEach(async () => {
  for (const b of bridges.splice(0)) b.kill("SIGKILL")
  for (const s of sockets.splice(0)) s.terminate()
  for (const p of pids.splice(0)) {
    try {
      process.kill(-p, "SIGKILL")
    } catch {
      /* gone */
    }
  }
  await daemon?.stop()
  box?.remove()
  daemon = box = undefined
})

/** A daemon with no apps that has allowed PAGE as a browser origin; the "claude" agent is `agent`, by default one that echoes every line. */
async function start(agent = "cat"): Promise<number> {
  box ??= new Sandbox()
  box.writeApps([], [{ origin: PAGE, grantedAt: "2026-10-19T00:00:00.000Z" }])
  daemon = await TestDaemon.start(box, [], { AGENTD_AGENT_CLAUDE: agent })
  return Number(await waitFor("the gateway port", () => /local gateway on 127\.0\.0\.1:(\d+)/.exec(daemon?.out ?? "")?.[1]))
}

interface Page {
  ws: WebSocket
  messages: Record<string, unknown>[]
  send: (m: object) => void
}

function dial(port: number, origin: string, host?: string): Promise<Page> {
  return new Promise((resolve, reject) => {
    const headers: Record<string, string> = { Origin: origin }
    if (host !== undefined) headers.Host = host
    const ws = new WebSocket(`ws://127.0.0.1:${port}/v1/session`, { headers })
    sockets.push(ws)
    const page: Page = { ws, messages: [], send: (m) => ws.send(JSON.stringify(m)) }
    ws.on("message", (d: Buffer) => page.messages.push(JSON.parse(d.toString()) as Record<string, unknown>))
    ws.on("error", () => undefined) // Bun can report one refusal twice; the first one rejects below
    ws.once("open", () => resolve(page))
    ws.once("error", reject)
  })
}

const next = (p: Page, what: string, pred: (m: Record<string, unknown>) => boolean) => waitFor(what, () => p.messages.find(pred))

/** A node script in the sandbox, run as the agent: `node <file> <args>`. */
function agentScript(name: string, source: string, ...args: string[]): string {
  box ??= new Sandbox()
  const file = join(box.work, name)
  writeFileSync(file, source)
  return ["node", file, ...args].join(" ")
}

/**
 * An agent that writes 64 KiB lines as fast as its stdout takes them. Every 50 ms it saves its pid and the bytes the
 * pipe has taken so far to `report`: the daemon holds at most that much of its output.
 */
const FLOOD = `
import { writeFileSync } from "node:fs"
const line = "x".repeat(65535) + "\\n"
let flushed = 0
const step = () => process.stdout.write(line, () => { flushed += line.length; setImmediate(step) })
setInterval(() => writeFileSync(process.argv[2], process.pid + " " + flushed), 50)
step()
`

function flooded(report: string): { pid: number; bytes: number } {
  const [pid, bytes] = readFileSync(report, "utf8").split(" ").map(Number)
  return { pid: pid ?? -1, bytes: bytes ?? 0 }
}

/** A page on a raw TCP socket: after the handshake it sends `messages` and then reads nothing until told to. */
async function stalledPage(port: number, messages: object[]): Promise<Socket> {
  const sock = connect(port, "127.0.0.1")
  await once(sock, "connect")
  sock.write(
    [
      "GET /v1/session HTTP/1.1",
      `Host: 127.0.0.1:${port}`,
      `Origin: ${PAGE}`,
      "Upgrade: websocket",
      "Connection: Upgrade",
      "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
      "Sec-WebSocket-Version: 13",
      "",
      "",
    ].join("\r\n"),
  )
  const [answer] = (await once(sock, "data")) as [Buffer]
  expect(answer.toString()).toStartWith("HTTP/1.1 101")
  sock.pause()
  // A masked text frame with an all-zero mask: the payload goes as it is. Each message here is under 126 bytes.
  for (const m of messages) {
    const payload = Buffer.from(JSON.stringify(m))
    sock.write(Buffer.concat([Buffer.from([0x81, 0x80 | payload.length, 0, 0, 0, 0]), payload]))
  }
  return sock
}

describe("a granted origin", () => {
  test("gets hello, opens an agent and talks to it through frames", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    const hello = await next(page, "hello", (m) => m.t === "hello")
    expect(hello.agents).toEqual(expect.arrayContaining([{ name: "claude", title: "Claude Code", ready: true, reason: "" }]))
    page.send({ t: "open", id: "s1", agent: "claude" })
    const opened = await next(page, "opened", (m) => m.t === "opened")
    pids.push(opened.pid as number)
    expect(opened.id).toBe("s1")
    page.send({ t: "write", id: "s1", line: '{"hello":"agent"}' })
    const frame = await next(page, "the echoed line", (m) => m.t === "frame" && m.kind === "out")
    expect(frame).toMatchObject({ id: "s1", data: '{"hello":"agent"}' })
  })

  test("an unknown agent is an error message, not a dead socket", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "rm -rf" })
    const err = await next(page, "error", (m) => m.t === "error")
    expect(err).toMatchObject({ id: "s1" })
    expect(String(err.message)).toContain('unknown agent "rm -rf"')
    expect(page.ws.readyState).toBe(WebSocket.OPEN)
  })

  test("closing the socket mid-turn ends the agent", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "claude" })
    const opened = await next(page, "opened", (m) => m.t === "opened")
    pids.push(opened.pid as number)
    expect(groupAlive(opened.pid as number)).toBe(true)
    page.ws.close()
    await waitFor("the agent group to end", () => !groupAlive(opened.pid as number), 8000)
  })

  test("a sixth session on one socket is refused", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    for (let i = 1; i <= 5; i++) {
      page.send({ t: "open", id: `s${i}`, agent: "claude" })
      pids.push((await next(page, `opened s${i}`, (m) => m.t === "opened" && m.id === `s${i}`)).pid as number)
    }
    page.send({ t: "open", id: "s6", agent: "claude" })
    const err = await next(page, "error for s6", (m) => m.t === "error" && m.id === "s6")
    expect(String(err.message)).toContain("at most 5")
  })

  test("a message that is not one of open, write, close is an error and changes nothing", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.ws.send("not json")
    const err = await next(page, "error", (m) => m.t === "error")
    expect(err.id).toBe("")
  })
})

describe("a page's limits", () => {
  test("a message from the page above the limit closes its socket as too big", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    await next(page, "hello", (m) => m.t === "hello")
    const closed = new Promise<number>((resolve) => page.ws.once("close", (code) => resolve(code)))
    page.send({ t: "write", id: "s1", line: "x".repeat(65 * 1024 * 1024) })
    expect(await closed).toBe(1009)
  })

  test("an agent line past the cap ends the session with an error that says why", async () => {
    const huge = agentScript("huge.mjs", 'process.stdout.write("x".repeat(33 * 1024 * 1024)); setInterval(() => undefined, 1000)\n')
    const port = await start(huge)
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "claude" })
    pids.push((await next(page, "opened", (m) => m.t === "opened")).pid as number)
    const err = await next(page, "the error frame", (m) => m.t === "frame" && m.kind === "err")
    expect(String(err.data)).toContain("longer than 32 MiB")
    await next(page, "the exit frame", (m) => m.t === "frame" && m.kind === "exit")
    expect(page.messages.some((m) => m.t === "frame" && m.kind === "out")).toBe(false)
  })

  test("a page that stops reading stops its agent's output instead of filling the daemon, and resumes it", async () => {
    box = new Sandbox()
    const report = join(box.work, "flood.count")
    const port = await start(agentScript("flood.mjs", FLOOD, report))
    const page = await stalledPage(port, [{ t: "open", id: "s1", agent: "claude" }])
    sockets.push({ terminate: () => page.destroy() } as unknown as WebSocket)
    const { pid } = await waitFor("the agent's report", () => {
      try {
        return flooded(report)
      } catch {
        return undefined
      }
    })
    pids.push(pid)
    await new Promise((r) => setTimeout(r, 3000))
    const stalled = flooded(report).bytes
    await new Promise((r) => setTimeout(r, 1000))
    // What the daemon holds: its socket's buffer (8 MiB) and the kernel's; without a pause it is hundreds of MB by now.
    expect(stalled).toBeLessThan(64 * 1024 * 1024)
    expect(flooded(report).bytes).toBeLessThan(stalled + 1024 * 1024) // stopped, not slowed
    expect(groupAlive(pid)).toBe(true)
    page.on("data", () => undefined)
    page.resume()
    await waitFor("the agent to write again", () => flooded(report).bytes > stalled + 64 * 1024 * 1024, 10_000)
  })
})

describe("taking the grant back", () => {
  test("DELETE from the page ends its open socket and the agents on it", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "claude" })
    const opened = await next(page, "opened", (m) => m.t === "opened")
    pids.push(opened.pid as number)
    const closed = new Promise<void>((resolve) => page.ws.once("close", () => resolve()))
    const res = await fetch(`http://127.0.0.1:${port}/v1/connect`, { method: "DELETE", headers: { Origin: PAGE } })
    expect(res.status).toBe(200)
    await closed
    await waitFor("the agent group to end", () => !groupAlive(opened.pid as number), 8000)
    await expect(dial(port, PAGE)).rejects.toBeDefined()
  })

  test("agentd apps remove, while the daemon runs, ends the page's open socket and the agents on it", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "claude" })
    const opened = await next(page, "opened", (m) => m.t === "opened")
    pids.push(opened.pid as number)
    const closed = new Promise<void>((resolve) => page.ws.once("close", () => resolve()))
    const cli = spawnSync("node", [(await build()).bundle, "apps", "remove", PAGE], { env: box?.env(), encoding: "utf8" })
    expect(cli.status).toBe(0)
    await closed
    await waitFor("the agent group to end", () => !groupAlive(opened.pid as number), 8000)
    await expect(dial(port, PAGE)).rejects.toBeDefined()
  })

  test("stopping the daemon does not wait for a page that keeps its socket open", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    page.send({ t: "open", id: "s1", agent: "claude" })
    pids.push((await next(page, "opened", (m) => m.t === "opened")).pid as number)
    const began = Date.now()
    daemon!.proc.kill("SIGTERM")
    expect(await Promise.race([daemon!.exited, new Promise((r) => setTimeout(r, 8000, "hung"))])).toBe(0)
    expect(Date.now() - began).toBeLessThan(8000)
  })
})

describe("refused upgrades", () => {
  test("an origin without a browser grant never gets a socket", async () => {
    const port = await start()
    await expect(dial(port, OTHER)).rejects.toBeDefined()
  })

  test("a granted origin with a foreign Host never gets a socket", async () => {
    const port = await start()
    await expect(dial(port, PAGE, "evil.example")).rejects.toBeDefined()
  })

  test("no Origin never gets a socket", async () => {
    const port = await start()
    await expect(
      new Promise((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/v1/session`, { origin: undefined, headers: {} })
        sockets.push(ws)
        ws.on("error", () => undefined)
        ws.once("open", resolve)
        ws.once("error", reject)
      }),
    ).rejects.toBeDefined()
  })
})

interface McpServer {
  command: string
  args: string[]
  env: { name: string; value: string }[]
}

/**
 * An agent that starts MCP servers as an ACP agent does from session/new: a line `{"start": <server>}` runs that
 * stdio server with the agent's environment plus the server's `env`; every other line goes to the server's stdin, the
 * server's stdout comes out as the agent's, and its exit is the line `{"bridgeExit": <code>}`.
 */
const MCP_AGENT = `
import { spawn } from "node:child_process"
import { createInterface } from "node:readline"
let server
createInterface({ input: process.stdin }).on("line", (line) => {
  const m = JSON.parse(line)
  if (m.start === undefined) return void server.stdin.write(line + "\\n")
  const env = { ...process.env, ...Object.fromEntries(m.start.env.map((e) => [e.name, e.value])) }
  server = spawn(m.start.command, m.start.args, { env, stdio: ["pipe", "pipe", "inherit"] })
  createInterface({ input: server.stdout }).on("line", (l) => process.stdout.write(l + "\\n"))
  server.on("exit", (code) => process.stdout.write(JSON.stringify({ bridgeExit: code }) + "\\n"))
})
`

const MCP_INIT = { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "agent", version: "1" } } }
const MCP_INITIALIZED = { jsonrpc: "2.0", method: "notifications/initialized" }
const MCP_LIST = { jsonrpc: "2.0", id: 2, method: "tools/list" }
const MCP_CALL = { jsonrpc: "2.0", id: 3, method: "tools/call", params: { name: "current_page", arguments: {} } }

/** The page's own MCP server: one tool, `current_page`, that says where the page is. */
function serveTools(page: Page, id: string): void {
  page.ws.on("message", (d: Buffer) => {
    const m = JSON.parse(d.toString()) as { t: string; id: string; line: string }
    if (m.t !== "mcp" || m.id !== id) return
    const req = JSON.parse(m.line) as { id?: number; method: string }
    if (req.id === undefined) return
    const results: Record<string, object> = {
      initialize: { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "page", version: "1" } },
      "tools/list": { tools: [{ name: "current_page", description: "Where the page is", inputSchema: { type: "object" } }] },
      "tools/call": { content: [{ type: "text", text: "/projects/42" }] },
    }
    page.send({ t: "mcp", id, line: JSON.stringify({ jsonrpc: "2.0", id: req.id, result: results[req.method] }) })
  })
}

/** `agentd-mcp --page` run by the test, outside any agent, with the environment of `server` (or `env` in its place). */
async function pageBridge(server: McpServer, env: Record<string, string> = {}) {
  const b = await build()
  const own = Object.fromEntries(server.env.map((e) => [e.name, e.value]))
  const p = spawn("node", [b.bundle, "mcp", "--page"], { env: { ...process.env, ...own, ...env }, stdio: ["pipe", "pipe", "pipe"] })
  bridges.push(p)
  let out = ""
  let err = ""
  p.stdout.on("data", (d: Buffer) => (out += d.toString()))
  p.stderr.on("data", (d: Buffer) => (err += d.toString()))
  const exited = new Promise<number | null>((resolve) => p.once("exit", resolve))
  return { p, out: () => out, err: () => err, exited, send: (m: object) => p.stdin.write(JSON.stringify(m) + "\n") }
}

/** A page with session `id` open on the agent; returns the page and what `opened` said. */
async function openOn(port: number, id = "s1") {
  const page = await dial(port, PAGE)
  page.send({ t: "open", id, agent: "claude" })
  const opened = await next(page, `opened ${id}`, (m) => m.t === "opened" && m.id === id)
  pids.push(opened.pid as number)
  return { page, opened, mcp: opened.mcp as McpServer }
}

const mcpLine = (page: Page, id: string, what: string, pred: (line: string) => boolean) =>
  next(page, what, (m) => m.t === "mcp" && m.id === id && pred(String(m.line)))

describe("a page's MCP server", () => {
  test("a page's agent finds agentd-mcp on its PATH, in the pages' own wrapper directory", async () => {
    const port = await start(sh("command -v agentd-mcp; sleep 60"))
    const { page } = await openOn(port)
    const line = await next(page, "the wrapper path", (m) => m.t === "frame" && m.kind === "out")
    expect(String(line.data)).toMatch(/agentd-[^/]+\/bin\/page\/agentd-mcp$/)
  })

  test("opened names the stdio server to pass to session/new: the pages' agentd-mcp by absolute path, with the daemon's private socket and a token in env", async () => {
    const port = await start()
    const { mcp } = await openOn(port)
    // absolute, as the ACP schema asks: the server starts whatever PATH the agent gives its MCP servers
    expect(mcp.command).toMatch(/^\/.+\/bin\/page\/agentd-mcp$/)
    expect(mcp.args).toEqual([])
    expect(mcp.env.map((e) => e.name)).toEqual(["AGENTD_MCP_SOCKET", "AGENTD_MCP_TOKEN"])
    const [socket, token] = mcp.env.map((e) => e.value)
    expect(statSync(socket!).isSocket()).toBe(true)
    expect(dirname(socket!)).toMatch(/agentd-[0-9A-Za-z]{6}$/)
    expect(statSync(dirname(socket!)).mode & 0o077).toBe(0) // no other OS user may reach the socket
    expect(Buffer.from(token!, "base64url").length).toBeGreaterThanOrEqual(16)
  })

  test("the agent's initialize, tools/list and tools/call reach the page's server, and its answers reach the agent", async () => {
    const port = await start(agentScript("mcp-agent.mjs", MCP_AGENT))
    const { page, mcp } = await openOn(port)
    serveTools(page, "s1")
    page.send({ t: "write", id: "s1", line: JSON.stringify({ start: mcp }) })
    for (const m of [MCP_INIT, MCP_INITIALIZED, MCP_LIST, MCP_CALL]) page.send({ t: "write", id: "s1", line: JSON.stringify(m) })
    const answer = (id: number) => next(page, `the agent's answer ${id}`, (m) => m.t === "frame" && m.kind === "out" && String(m.data).includes(`"id":${id},`))
    expect(JSON.parse(String((await answer(1)).data))).toMatchObject({ result: { serverInfo: { name: "page" } } })
    expect(JSON.parse(String((await answer(2)).data))).toMatchObject({ result: { tools: [{ name: "current_page" }] } })
    expect(JSON.parse(String((await answer(3)).data))).toMatchObject({ result: { content: [{ type: "text", text: "/projects/42" }] } })
    await mcpLine(page, "s1", "the notification", (l) => l.includes("notifications/initialized"))
  })

  test("a wrong token is refused: the bridge says why and exits 1", async () => {
    const port = await start(agentScript("mcp-agent.mjs", MCP_AGENT))
    const { page, mcp } = await openOn(port)
    const wrong = { ...mcp, env: mcp.env.map((e) => (e.name === "AGENTD_MCP_TOKEN" ? { ...e, value: "guessed" } : e)) }
    page.send({ t: "write", id: "s1", line: JSON.stringify({ start: wrong }) })
    await next(page, "the bridge's exit", (m) => m.t === "frame" && String(m.data) === '{"bridgeExit":1}')
    const err = page.messages.filter((m) => m.t === "frame" && m.kind === "err").map((m) => String(m.data)).join("")
    expect(err).toContain("agentd-mcp: the daemon refused the bridge (no open session has this token")
    expect(page.messages.some((m) => m.t === "mcp")).toBe(false)
  })

  test("a bridge without its environment says what it needs and exits 1", async () => {
    await start()
    const bridge = await pageBridge({ command: "agentd-mcp", args: [], env: [] }, { AGENTD_MCP_SOCKET: "", AGENTD_MCP_TOKEN: "" })
    expect(await bridge.exited).toBe(1)
    expect(bridge.err()).toContain("AGENTD_MCP_SOCKET and AGENTD_MCP_TOKEN")
  })

  test("closing the page ends its bridge, which exits 1", async () => {
    const port = await start()
    const { page, mcp } = await openOn(port)
    const bridge = await pageBridge(mcp)
    bridge.send(MCP_INIT)
    await mcpLine(page, "s1", "initialize on the page", (l) => l.includes('"initialize"'))
    page.ws.close()
    expect(await bridge.exited).toBe(1)
    expect(bridge.err()).toContain("agentd-mcp: the daemon ended the bridge")
  })

  test("closing the session ends its bridge, and its token opens no other", async () => {
    const port = await start()
    const { page, mcp } = await openOn(port)
    const bridge = await pageBridge(mcp)
    bridge.send(MCP_INIT)
    await mcpLine(page, "s1", "initialize on the page", (l) => l.includes('"initialize"'))
    page.send({ t: "close", id: "s1" })
    expect(await bridge.exited).toBe(1)
    const late = await pageBridge(mcp)
    expect(await late.exited).toBe(1)
    expect(late.err()).toContain("refused the bridge")
  })

  test("a page line for a session with no bridge is an error that says so", async () => {
    const port = await start()
    const { page } = await openOn(port)
    page.send({ t: "mcp", id: "s1", line: "{}" })
    const err = await next(page, "the error", (m) => m.t === "error" && m.id === "s1")
    expect(String(err.message)).toContain("no MCP bridge")
  })

  test("another page's token reaches that page only, even with the same session id", async () => {
    const port = await start()
    const a = await openOn(port)
    const b = await openOn(port)
    const bridge = await pageBridge(b.mcp)
    bridge.send(MCP_INIT)
    await mcpLine(b.page, "s1", "initialize on page b", (l) => l.includes('"initialize"'))
    a.page.send({ t: "mcp", id: "s1", line: '{"jsonrpc":"2.0","id":1,"result":{"from":"a"}}' })
    b.page.send({ t: "mcp", id: "s1", line: '{"jsonrpc":"2.0","id":1,"result":{"from":"b"}}' })
    await waitFor("b's answer on the bridge", () => bridge.out().includes('"from":"b"'))
    expect(bridge.out()).not.toContain('"from":"a"')
    expect(a.page.messages.some((m) => m.t === "mcp")).toBe(false)
  })

  test("a bridge started again for the session replaces the older one", async () => {
    const port = await start()
    const { page, mcp } = await openOn(port)
    const older = await pageBridge(mcp)
    older.send(MCP_INIT)
    await mcpLine(page, "s1", "the older initialize", (l) => l.includes('"initialize"'))
    const newer = await pageBridge(mcp)
    expect(await older.exited).toBe(1)
    newer.send({ ...MCP_INIT, id: 7 })
    await mcpLine(page, "s1", "the newer initialize", (l) => l.includes('"id":7'))
    page.send({ t: "mcp", id: "s1", line: '{"jsonrpc":"2.0","id":7,"result":{}}' })
    await waitFor("the answer on the newer bridge", () => newer.out().includes('"id":7'))
  })
})
