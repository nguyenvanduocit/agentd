import { afterEach, describe, expect, test } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { connect, type Socket } from "node:net"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { bridgeHello } from "../../src/core/pagemcp.ts"
import { McpRelay, type McpPage } from "../../src/page-mcp.ts"

const dirs: string[] = []
const relays: McpRelay[] = []
const socks: Socket[] = []
afterEach(async () => {
  for (const s of socks.splice(0)) s.destroy()
  for (const r of relays.splice(0)) await r.close()
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

async function relay(): Promise<McpRelay> {
  const dir = mkdtempSync(join(tmpdir(), "mc-relay-"))
  dirs.push(dir)
  const r = await McpRelay.listen(join(dir, "mcp.sock"))
  relays.push(r)
  return r
}

/** A page that keeps the lines it gets and is never behind. */
function page(): McpPage & { lines: string[] } {
  const lines: string[] = []
  return {
    lines,
    send: (line) => {
      lines.push(line)
    },
    backlog: () => undefined,
  }
}

async function until(fn: () => boolean): Promise<void> {
  const deadline = Date.now() + 5000
  while (!fn()) {
    if (Date.now() > deadline) throw new Error("timed out")
    await new Promise((r) => setTimeout(r, 10))
  }
}

interface Bridge {
  sock: Socket
  /** The lines the daemon sent: its answer to the hello, then the page's lines. */
  lines: string[]
  closed: Promise<void>
}

/** A bridge as agentd-mcp --page is one: it says hello with `token` and waits for the answer. */
async function bridge(path: string, token: string): Promise<Bridge> {
  const sock = connect(path)
  socks.push(sock)
  const lines: string[] = []
  let rest = ""
  sock.on("data", (d: Buffer) => {
    const parts = (rest + d.toString()).split("\n")
    rest = parts.pop() ?? ""
    lines.push(...parts)
  })
  const closed = new Promise<void>((resolve) => sock.once("close", () => resolve()))
  sock.write(bridgeHello(token) + "\n")
  await until(() => lines.length > 0)
  return { sock, lines, closed }
}

describe("McpRelay", () => {
  test("a token is unguessable: at least 128 random bits, new for every binding", async () => {
    const r = await relay()
    const a = r.bind(page())
    const b = r.bind(page())
    expect(Buffer.from(a.token, "base64url").length).toBeGreaterThanOrEqual(16)
    expect(a.token).not.toBe(b.token)
  })

  test("a bridge with the token reaches its page, and the page's lines reach the bridge", async () => {
    const r = await relay()
    const p = page()
    const binding = r.bind(p)
    const b = await bridge(r.path, binding.token)
    expect(JSON.parse(b.lines[0]!)).toEqual({ ok: true })
    b.sock.write('{"id":1}\n\n{"id":2}\n')
    await until(() => p.lines.length === 2)
    expect(p.lines).toEqual(['{"id":1}', '{"id":2}'])
    expect(binding.toBridge('{"id":1,"result":{}}')).toBe(true)
    await until(() => b.lines.length === 2)
    expect(b.lines[1]).toBe('{"id":1,"result":{}}')
  })

  test("an unknown token is refused with a reason, closed, and reaches no page", async () => {
    const r = await relay()
    const p = page()
    r.bind(p)
    const b = await bridge(r.path, "not-a-token")
    expect(JSON.parse(b.lines[0]!)).toEqual({ ok: false, message: expect.stringContaining("no open session has this token") })
    await b.closed
    expect(p.lines).toEqual([])
  })

  test("a refused connection gets no second try in the same breath", async () => {
    const r = await relay()
    const p = page()
    const binding = r.bind(p)
    const sock = connect(r.path)
    socks.push(sock)
    let answer = ""
    sock.on("data", (d: Buffer) => (answer += d.toString()))
    sock.write(`${bridgeHello("not-a-token")}\n${bridgeHello(binding.token)}\n{"id":1}\n`)
    await new Promise<void>((resolve) => sock.once("close", () => resolve()))
    expect(answer.trim().split("\n")).toHaveLength(1)
    expect(p.lines).toEqual([])
  })

  test("a page line while no bridge is connected is not delivered", async () => {
    const r = await relay()
    expect(r.bind(page()).toBridge("{}")).toBe(false)
  })

  test("ending the binding closes its bridge and refuses its token from then on", async () => {
    const r = await relay()
    const binding = r.bind(page())
    const b = await bridge(r.path, binding.token)
    binding.end()
    await b.closed
    expect(binding.toBridge("{}")).toBe(false)
    const again = await bridge(r.path, binding.token)
    expect(JSON.parse(again.lines[0]!)).toMatchObject({ ok: false })
  })

  test("a newer bridge with the same token replaces the older one", async () => {
    const r = await relay()
    const p = page()
    const binding = r.bind(p)
    const older = await bridge(r.path, binding.token)
    const newer = await bridge(r.path, binding.token)
    await older.closed
    expect(JSON.parse(newer.lines[0]!)).toEqual({ ok: true })
    binding.toBridge('{"to":"newer"}')
    await until(() => newer.lines.length === 2)
    expect(older.lines).toEqual(['{"ok":true}'])
    newer.sock.write('{"from":"newer"}\n')
    await until(() => p.lines.length === 1)
    expect(p.lines).toEqual(['{"from":"newer"}'])
  })

  test("a page that is behind pauses its bridge's lines until it has room", async () => {
    const r = await relay()
    let room: (() => void) | undefined
    const lines: string[] = []
    const p: McpPage = {
      send: (line) => {
        lines.push(line)
      },
      backlog: () => (lines.length === 1 ? new Promise<void>((resolve) => (room ??= resolve)) : undefined),
    }
    const b = await bridge(r.path, r.bind(p).token)
    b.sock.write("1\n")
    await until(() => lines.length === 1)
    b.sock.write("2\n")
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(lines).toEqual(["1"])
    room?.()
    await until(() => lines.length === 2)
    expect(lines).toEqual(["1", "2"])
  })
})
