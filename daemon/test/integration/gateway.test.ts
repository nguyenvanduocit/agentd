import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { request } from "node:http"
import { createServer, type Server } from "node:net"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { WebSocket } from "ws"
import type { Question } from "../../src/core/consent.ts"
import { addBrowser, emptyRegistry } from "../../src/core/registry.ts"
import { startGateway, type Gateway, type GatewayDeps } from "../../src/local.ts"
import type { PeerOwners } from "../../src/peer.ts"
import { build, Sandbox, TestDaemon, TestServer, TOKEN, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const PAGE = "http://localhost:5173"
const dirs: string[] = []
const servers: TestServer[] = []
const blockers: Server[] = []
const gateways: Gateway[] = []
let daemon: TestDaemon | undefined
let box: Sandbox | undefined

afterEach(async () => {
  await daemon?.stop()
  for (const s of servers.splice(0)) s.stop()
  for (const b of blockers.splice(0)) b.close()
  for (const g of gateways.splice(0)) await g.close()
  box?.remove()
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
  daemon = undefined
  box = undefined
})

/** A program for AGENTD_CONSENT: it appends a line to `ran`, waits `delayS`, exits `code`. */
function consent(code: number, delayS = 0): { file: string; ran: () => number } {
  const dir = mkdtempSync(join(tmpdir(), "mc-gw-"))
  dirs.push(dir)
  const file = join(dir, "consent")
  const ranFile = join(dir, "ran")
  writeFileSync(file, `#!/bin/sh\necho "$1" >> ${ranFile}\nsleep ${delayS}\nexit ${code}\n`)
  chmodSync(file, 0o755)
  return { file, ran: () => (existsSync(ranFile) ? readFileSync(ranFile, "utf8").split("\n").filter(Boolean).length : 0) }
}

async function startDaemon(env: Record<string, string>, apps: TestServer[] = []) {
  box = new Sandbox()
  if (apps.length > 0) box.writeApps(apps.map((s) => ({ server: s.origin, token: TOKEN })))
  daemon = await TestDaemon.start(box, [], { AGENTD_LOCAL_PORT: "0", AGENTD_AGENT_CLAUDE: "sh -c 'sleep 60'", ...env })
  const port = await waitFor("the gateway port", () => /local gateway on 127\.0\.0\.1:(\d+)/.exec(daemon?.out ?? "")?.[1])
  return Number(port)
}

interface Reply {
  status: number
  body: Record<string, unknown>
  headers: Record<string, string | string[] | undefined>
}

function call(port: number, method: string, path: string, o: { origin?: string | null; host?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<Reply> {
  return new Promise((resolve, reject) => {
    const headers: Record<string, string> = { Host: o.host ?? `127.0.0.1:${port}`, ...o.headers }
    if (o.origin !== null && o.origin !== undefined) headers.Origin = o.origin
    const payload = o.body === undefined ? undefined : JSON.stringify(o.body)
    if (payload !== undefined) headers["Content-Type"] = "application/json"
    const req = request({ host: "127.0.0.1", port, method, path, headers }, (res) => {
      let text = ""
      res.on("data", (d: Buffer) => (text += d.toString()))
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: text === "" ? {} : (JSON.parse(text) as Record<string, unknown>), headers: res.headers }))
    })
    req.on("error", reject)
    req.end(payload)
  })
}

const connect = (port: number, body: unknown, origin = PAGE) => call(port, "POST", "/v1/connect", { origin, body })
const savedApps = () => (JSON.parse(readFileSync(box!.configFile, "utf8")) as { apps: { server: string }[] }).apps.map((a) => a.server)

describe("refusals before any dialog", () => {
  test("a foreign Host is 421; a missing, null or untrusted Origin is 403; the dialog never runs", async () => {
    const c = consent(0)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    expect((await call(port, "GET", "/v1/status", { origin: PAGE, host: "evil.example" })).status).toBe(421)
    expect((await call(port, "GET", "/v1/status", { origin: null })).status).toBe(403)
    expect((await call(port, "GET", "/v1/status", { origin: "null" })).status).toBe(403)
    expect((await call(port, "GET", "/v1/status", { origin: "http://evil.example" })).status).toBe(403)
    expect((await call(port, "POST", "/v1/connect", { origin: "http://evil.example", body: { mode: "browser" } })).status).toBe(403)
    expect(c.ran()).toBe(0)
  })

  test("a malformed body is 400 and does not open the dialog", async () => {
    const c = consent(0)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    expect((await connect(port, { mode: "server" })).status).toBe(400)
    expect((await connect(port, { mode: "root" })).status).toBe(400)
    expect(c.ran()).toBe(0)
  })

  test("a preflight with Private Network Access is answered for a trusted origin", async () => {
    const port = await startDaemon({})
    const r = await call(port, "OPTIONS", "/v1/connect", { origin: PAGE, headers: { "Access-Control-Request-Private-Network": "true", "Access-Control-Request-Method": "POST" } })
    expect(r.status).toBe(204)
    expect(r.headers["access-control-allow-origin"]).toBe(PAGE)
    expect(r.headers["access-control-allow-private-network"]).toBe("true")
  })
})

describe("status", () => {
  test("an origin that was never allowed learns the version and nothing else", async () => {
    const port = await startDaemon({})
    const r = await call(port, "GET", "/v1/status", { origin: PAGE })
    expect(r.status).toBe(200)
    expect(r.body).toMatchObject({ name: "agentd", granted: { server: false, browser: false } })
    expect(r.body.agents).toBeUndefined()
    expect(r.headers["access-control-allow-origin"]).toBe(PAGE)
  })
})

describe("connect a server app", () => {
  // The app is the one at the page's own origin: the page here is the app's server.
  const body = { mode: "server", code: "TESTCODE" }

  test("Allow pairs, saves and links without a restart; the origin then sees the agents", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const c = consent(0)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    const r = await connect(port, body, app.origin)
    expect(r.status).toBe(200)
    expect(r.body).toEqual({ granted: true })
    expect(c.ran()).toBe(1)
    expect(savedApps()).toEqual([app.origin])
    await app.waitHellos(1)
    const st = await call(port, "GET", "/v1/status", { origin: app.origin })
    expect(st.body).toMatchObject({ granted: { server: true, browser: false } })
    expect((st.body.agents as { name: string }[]).map((a) => a.name)).toEqual(["claude", "codex", "grok"])
  })

  test("a request that names a server is refused before any dialog", async () => {
    const c = consent(0)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    expect(await connect(port, { ...body, server: "https://api.example.com" })).toMatchObject({ status: 400, body: { reason: "malformed" } })
    expect(c.ran()).toBe(0)
  })

  test("Deny saves nothing, and the same origin waits out a cooldown", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const c = consent(1)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    expect(await connect(port, body, app.origin)).toMatchObject({ status: 403, body: { granted: false, reason: "denied" } })
    expect(await connect(port, body, app.origin)).toMatchObject({ status: 429, body: { granted: false, reason: "cooldown" } })
    expect(c.ran()).toBe(1)
    expect((await call(port, "GET", "/v1/status", { origin: app.origin })).body).toMatchObject({ granted: { server: false } })
    expect((await app.status()).connected).toBe(false)
  })

  test("refusals from three origins lock every dialog: new subdomains do not buy new dialogs", async () => {
    const c = consent(1)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    for (const o of ["http://localhost:5171", "http://localhost:5172", "http://localhost:5173"]) {
      expect(await connect(port, { mode: "browser" }, o)).toMatchObject({ status: 403, body: { reason: "denied" } })
    }
    expect(await connect(port, { mode: "browser" }, "http://localhost:5174")).toMatchObject({ status: 429, body: { reason: "cooldown" } })
    expect(c.ran()).toBe(3)
  })

  test("a second request while a dialog is open is busy", async () => {
    const c = consent(0, 2)
    const port = await startDaemon({ AGENTD_CONSENT: c.file })
    const first = connect(port, { mode: "browser" })
    await waitFor("the dialog to open", () => c.ran() === 1)
    expect(await connect(port, { mode: "browser" }, "http://localhost:5174")).toMatchObject({ status: 409, body: { reason: "busy" } })
    expect((await first).status).toBe(200)
  })

  test("a machine that cannot show a dialog says so and grants nothing", async () => {
    const port = await startDaemon({ AGENTD_CONSENT: "/nonexistent/consent" })
    expect(await connect(port, { mode: "browser" })).toMatchObject({ status: 503, body: { granted: false, reason: "no-dialog" } })
  })

  test("a pairing code the app refuses is 502 after Allow, and nothing is saved", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file })
    expect(await connect(port, { mode: "server", code: "WRONG" }, app.origin)).toMatchObject({ status: 502, body: { granted: false, reason: "pairing" } })
    expect(existsSync(box!.configFile) ? savedApps() : []).toEqual([])
  })

  test("connecting an app that is already paired links it again at once", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [app])
    await app.waitHellos(1)
    expect((await connect(port, body, app.origin)).status).toBe(200)
    await app.waitHellos(2)
    expect(savedApps()).toEqual([app.origin])
  })

  test("DELETE disconnects the caller's own app and the daemon stays up", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [app])
    await app.waitHellos(1)
    const r = await call(port, "DELETE", "/v1/connect", { origin: app.origin })
    expect(r.status).toBe(200)
    await waitFor("the app is disconnected", async () => !(await app.status()).connected)
    expect(savedApps()).toEqual([])
    expect(daemon!.proc.exitCode).toBeNull()
  })
})

describe("one origin cannot see or touch another's app", () => {
  test("status for another origin says nothing about the app, and DELETE from it does nothing", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [app])
    await app.waitHellos(1)
    const asked = `?server=${encodeURIComponent(app.origin)}` // the old way to name another origin's app: ignored
    const st = await call(port, "GET", `/v1/status${asked}`, { origin: PAGE })
    expect(st.body).toMatchObject({ granted: { server: false, browser: false } })
    expect(st.body.agents).toBeUndefined()
    expect((await call(port, "DELETE", `/v1/connect${asked}`, { origin: PAGE })).status).toBe(200)
    await new Promise((r) => setTimeout(r, 500))
    expect(savedApps()).toEqual([app.origin])
    expect((await app.status()).connected).toBe(true)
  })

  test("an app that revoked this daemon reads as not linked once it refused the token, and is offered again", async () => {
    const a = await TestServer.start()
    const b = await TestServer.start()
    servers.push(a, b)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [a, b])
    await a.waitHellos(1)
    await b.waitHellos(1)
    await a.revoke()
    await waitFor("the refusal", () => daemon!.out.includes(`${a.origin} refused this daemon (unauthorized)`))
    expect((await call(port, "GET", "/v1/status", { origin: a.origin })).body).toMatchObject({ granted: { server: false } })
  })

  test("an app that Stop detached is offered again, and connects again", async () => {
    const a = await TestServer.start()
    const b = await TestServer.start()
    servers.push(a, b)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [a, b])
    await a.waitHellos(1)
    await b.waitHellos(1)
    await a.stopDaemon()
    await waitFor("a is disconnected", async () => !(await a.status()).connected)
    expect((await call(port, "GET", "/v1/status", { origin: a.origin })).body).toMatchObject({ granted: { server: false } })
    expect((await connect(port, { mode: "server", code: "TESTCODE" }, a.origin)).status).toBe(200)
    await waitFor("a is linked again", async () => (await a.status()).connected)
    expect((await call(port, "GET", "/v1/status", { origin: a.origin })).body).toMatchObject({ granted: { server: true } })
  })
})

describe("browser grants", () => {
  test("Allow saves the origin; the status says so; the daemon outlives its last server app", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const port = await startDaemon({ AGENTD_CONSENT: consent(0).file }, [app])
    await app.waitHellos(1)
    expect(await connect(port, { mode: "browser" })).toMatchObject({ status: 200, body: { granted: true } })
    expect((await call(port, "GET", "/v1/status", { origin: PAGE })).body).toMatchObject({ granted: { browser: true } })
    await app.stopDaemon()
    await waitFor("the app is detached", async () => !(await app.status()).connected)
    await new Promise((r) => setTimeout(r, 1000))
    expect(daemon!.proc.exitCode).toBeNull()
  })
})

describe("ports", () => {
  test("when every port of the range is taken the gateway is off and the apps still work", async () => {
    const base = 53100
    for (let p = base; p < base + 10; p++) {
      const s = createServer()
      await new Promise<void>((res, rej) => s.once("error", rej).listen(p, "127.0.0.1", res))
      blockers.push(s)
    }
    const app = await TestServer.start()
    servers.push(app)
    box = new Sandbox()
    box.writeApps([{ server: app.origin, token: TOKEN }])
    daemon = await TestDaemon.start(box, [], { AGENTD_LOCAL_PORT: String(base) })
    await waitFor("the gateway off line", () => daemon!.out.includes("local gateway off"))
    expect((await app.waitHellos(1)).connected).toBe(true)
  })
})

/** What a gateway in this process was asked to do: dialogs, revokes, upgrades. */
interface Seen {
  asked: Question[]
  revoked: string[]
  upgraded: string[]
}

/** A gateway in this test process, PAGE granted as a browser origin, every dialog answered Allow. */
async function gatewayHere(owners?: PeerOwners): Promise<{ port: number; seen: Seen }> {
  const seen: Seen = { asked: [], revoked: [], upgraded: [] }
  const deps: GatewayDeps = {
    version: "test",
    agents: () => [],
    registry: () => Promise.resolve(addBrowser(emptyRegistry(), PAGE, "2026-10-19T00:00:00.000Z")),
    ask: (q) => {
      seen.asked.push(q)
      return Promise.resolve("allow")
    },
    linked: () => false,
    connectServer: () => Promise.resolve(),
    grantBrowser: () => Promise.resolve(),
    revoke: (origin) => {
      seen.revoked.push(origin)
      return Promise.resolve()
    },
    upgrade: (origin, _req, socket) => {
      seen.upgraded.push(origin)
      socket.destroy()
    },
  }
  const g = await startGateway(deps, 0, Date.now, owners)
  if (g === undefined) throw new Error("no gateway port")
  gateways.push(g)
  return { port: g.port, seen }
}

/** Dials /v1/session as PAGE and waits until the gateway hangs up or the upgrade completes. */
function dialSession(port: number): Promise<void> {
  return new Promise((resolve) => {
    const ws = new WebSocket(`ws://127.0.0.1:${port}/v1/session`, { headers: { Origin: PAGE } })
    ws.on("error", () => resolve())
    ws.on("close", () => resolve())
  })
}

describe("only programs of the daemon's own OS user reach the gateway", () => {
  const me = process.getuid?.() ?? 0
  const routes = async (port: number) => [
    await call(port, "GET", "/v1/status", { origin: PAGE }),
    await connect(port, { mode: "browser" }, "http://localhost:5174"),
    await connect(port, { mode: "server", code: "TESTCODE" }, "http://localhost:5174"),
    await call(port, "DELETE", "/v1/connect", { origin: PAGE }),
  ]

  test("a connection from another user's process is refused on every route and upgrade, with no dialog", async () => {
    const { port, seen } = await gatewayHere(() => Promise.resolve([me + 1]))
    for (const r of await routes(port)) expect(r).toMatchObject({ status: 403, body: { granted: false, reason: "peer" } })
    await dialSession(port)
    expect(seen).toEqual({ asked: [], revoked: [], upgraded: [] })
  })

  test("an owner that cannot be found, or a lookup that fails, is refused too", async () => {
    for (const owners of [() => Promise.resolve([]), () => Promise.reject(new Error("lsof is gone"))]) {
      const { port, seen } = await gatewayHere(owners)
      expect(await call(port, "GET", "/v1/status", { origin: PAGE })).toMatchObject({ status: 403, body: { reason: "peer" } })
      await dialSession(port)
      expect(seen.upgraded).toEqual([])
    }
  })

  test("the real lookup lets this process in: routes answer and the session upgrade goes through", async () => {
    const { port, seen } = await gatewayHere()
    expect((await call(port, "GET", "/v1/status", { origin: PAGE })).status).toBe(200)
    expect((await connect(port, { mode: "browser" }, "http://localhost:5174")).status).toBe(200)
    await dialSession(port)
    expect(seen.asked.map((q) => q.origin)).toEqual(["http://localhost:5174"])
    expect(seen.upgraded).toEqual([PAGE])
  })
})
