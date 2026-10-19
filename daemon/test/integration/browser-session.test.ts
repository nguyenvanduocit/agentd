import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { WebSocket } from "ws"
import { build, groupAlive, Sandbox, TestDaemon, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const PAGE = "http://localhost:5173"
const OTHER = "http://localhost:5174"
let daemon: TestDaemon | undefined
let box: Sandbox | undefined
const sockets: WebSocket[] = []
const pids: number[] = []

afterEach(async () => {
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

/** A daemon with no apps that has allowed PAGE as a browser origin; the "claude" agent echoes every line. */
async function start(): Promise<number> {
  box = new Sandbox()
  box.writeApps([], [{ origin: PAGE, grantedAt: "2026-10-19T00:00:00.000Z" }])
  daemon = await TestDaemon.start(box, [], { AGENTD_AGENT_CLAUDE: "cat" })
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

describe("a granted origin", () => {
  test("gets hello, opens an agent and talks to it through frames", async () => {
    const port = await start()
    const page = await dial(port, PAGE)
    const hello = await next(page, "hello", (m) => m.t === "hello")
    expect(hello.agents).toEqual(expect.arrayContaining([{ name: "claude", ready: true, reason: "" }]))
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
