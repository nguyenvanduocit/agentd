import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http"
import type { AddressInfo } from "node:net"
import type { Duplex } from "node:stream"
import type { AgentInfo } from "../../contract/define.acp.ts"
import type { Answer, Question } from "./core/consent.ts"
import { Cooldown, corsHeaders, hostAllowed, Lockout, originOf, parseConnect, PORT_RANGE } from "./core/local.ts"
import { hasApp, hasBrowser, type Registry } from "./core/registry.ts"

const COOLDOWN_MS = 30_000
const LOCKOUT_REFUSALS = 3
const LOCKOUT_WINDOW_MS = 10 * 60_000
const MAX_BODY = 4096

/** What the gateway needs from the daemon. */
export interface GatewayDeps {
  version: string
  agents: () => AgentInfo[]
  registry: () => Promise<Registry>
  ask: (q: Question) => Promise<Answer>
  /** Whether the daemon is linked to the app at `server` now (an app that Stop or a refused token detached is saved, not linked). */
  linked: (server: string) => boolean
  /** Pairs with the server at the page's origin using the code, saves it and links to it now; throws when the app refuses. */
  connectServer: (server: string, code: string) => Promise<void>
  grantBrowser: (origin: string) => Promise<void>
  /** Forgets the browser grant and the app of `origin`, and ends their sessions. */
  revoke: (origin: string) => Promise<void>
  /** A WebSocket upgrade of a browser-granted origin on /v1/session. */
  upgrade?: (origin: string, req: IncomingMessage, socket: Duplex, head: Buffer) => void
}

export interface Gateway {
  port: number
  close: () => Promise<void>
}

interface Out {
  status: number
  body: Record<string, unknown>
}

const refuse = (status: number, reason: string, extra: Record<string, unknown> = {}): Out => ({ status, body: { granted: false, reason, ...extra } })

function readJson(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve) => {
    let text = ""
    req.on("data", (d: Buffer) => {
      text += d.toString()
      if (text.length > MAX_BODY) req.destroy()
    })
    req.on("end", () => {
      try {
        resolve(JSON.parse(text))
      } catch {
        resolve(undefined)
      }
    })
    req.on("error", () => resolve(undefined))
  })
}

/** The requests of /v1, one origin at a time asking the user. A page sees and changes only what belongs to its own origin. */
class Routes {
  private busy = false
  private readonly cooldown: Cooldown
  private readonly lockout: Lockout

  constructor(
    private readonly deps: GatewayDeps,
    now: () => number,
  ) {
    this.cooldown = new Cooldown(COOLDOWN_MS, now)
    this.lockout = new Lockout(LOCKOUT_REFUSALS, LOCKOUT_WINDOW_MS, now)
  }

  async status(origin: string): Promise<Out> {
    const reg = await this.deps.registry()
    const granted = { server: hasApp(reg, origin) && this.deps.linked(origin), browser: hasBrowser(reg, origin) }
    const body: Record<string, unknown> = { name: "agentd", version: this.deps.version, granted }
    if (granted.server || granted.browser) body.agents = this.deps.agents()
    return { status: 200, body }
  }

  async connect(origin: string, req: IncomingMessage): Promise<Out> {
    const want = parseConnect(await readJson(req))
    if (want === undefined) return refuse(400, "malformed")
    if (want.mode === "browser" && hasBrowser(await this.deps.registry(), origin)) return { status: 200, body: { granted: true } }
    if (this.cooldown.blocked(origin) || this.lockout.locked()) return refuse(429, "cooldown")
    if (this.busy) return refuse(409, "busy")
    this.busy = true
    try {
      return await this.decide(origin, want.mode === "server" ? want.code : undefined)
    } finally {
      this.busy = false
    }
  }

  private async decide(origin: string, code: string | undefined): Promise<Out> {
    const agents = this.deps.agents().filter((a) => a.ready).map((a) => a.name)
    const answer = await this.deps.ask({ origin, agents })
    if (answer === "unavailable") return refuse(503, "no-dialog")
    if (answer !== "allow") {
      this.cooldown.block(origin)
      this.lockout.strike()
      return refuse(403, answer === "timeout" ? "timeout" : "denied")
    }
    this.lockout.reset()
    try {
      if (code !== undefined) await this.deps.connectServer(origin, code)
      else await this.deps.grantBrowser(origin)
    } catch (e) {
      return refuse(502, "pairing", { message: e instanceof Error ? e.message : String(e) })
    }
    return { status: 200, body: { granted: true } }
  }

  async disconnect(origin: string): Promise<Out> {
    await this.deps.revoke(origin)
    return { status: 200, body: { granted: false } }
  }
}

function send(res: ServerResponse, out: Out, headers: Record<string, string>): void {
  res.writeHead(out.status, { "Content-Type": "application/json", "Cache-Control": "no-store", ...headers })
  res.end(JSON.stringify(out.body))
}

const LOCAL_BASE = "https://gateway.invalid"

function route(routes: Routes, req: IncomingMessage, origin: string): Promise<Out> | undefined {
  const path = new URL(req.url ?? "/", LOCAL_BASE).pathname
  if (path === "/v1/status" && req.method === "GET") return routes.status(origin)
  if (path === "/v1/connect" && req.method === "POST") return routes.connect(origin, req)
  if (path === "/v1/connect" && req.method === "DELETE") return routes.disconnect(origin)
  return undefined
}

function failure(e: unknown): Out {
  return refuse(500, "error", { message: e instanceof Error ? e.message : String(e) })
}

function handle(routes: Routes, port: number, req: IncomingMessage, res: ServerResponse): void {
  if (!hostAllowed(req.headers.host, port)) {
    send(res, refuse(421, "host"), {})
    return
  }
  const origin = originOf(req.headers.origin)
  if (origin === undefined) {
    send(res, refuse(403, "origin"), {})
    return
  }
  const cors = corsHeaders(origin, req.headers as Record<string, string | undefined>)
  if (req.method === "OPTIONS") {
    res.writeHead(204, cors)
    res.end()
    return
  }
  const out = route(routes, req, origin)
  if (out === undefined) send(res, refuse(404, "not-found"), cors)
  else out.then((o) => send(res, o, cors), (e: unknown) => send(res, failure(e), cors))
}

function upgrade(deps: GatewayDeps, port: number, req: IncomingMessage, socket: Duplex, head: Buffer): void {
  const origin = originOf(req.headers.origin)
  const path = new URL(req.url ?? "/", LOCAL_BASE).pathname
  if (!hostAllowed(req.headers.host, port) || origin === undefined || path !== "/v1/session" || deps.upgrade === undefined) {
    socket.destroy()
    return
  }
  deps.registry().then(
    (reg) => {
      if (hasBrowser(reg, origin)) deps.upgrade?.(origin, req, socket, head)
      else socket.destroy()
    },
    () => socket.destroy(),
  )
}

function serve(deps: GatewayDeps, now: () => number): Server {
  const routes = new Routes(deps, now)
  const portOf = () => (server.address() as AddressInfo).port
  const server = createServer((req, res) => handle(routes, portOf(), req, res))
  server.on("upgrade", (req, socket, head) => upgrade(deps, portOf(), req, socket, head))
  return server
}

function listen(server: Server, port: number): Promise<number | "taken"> {
  return new Promise((resolve, reject) => {
    server.once("error", (e: NodeJS.ErrnoException) => (e.code === "EADDRINUSE" ? resolve("taken") : reject(e)))
    server.listen(port, "127.0.0.1", () => resolve((server.address() as AddressInfo).port))
  })
}

/**
 * Starts the gateway on 127.0.0.1: the first free port of `port` … `port + 9` (any free port when `port` is 0).
 * Resolves to undefined when all are taken.
 */
export async function startGateway(deps: GatewayDeps, port: number, now: () => number = Date.now): Promise<Gateway | undefined> {
  const last = port === 0 ? 0 : port + PORT_RANGE - 1
  for (let p = port; p <= last; p++) {
    const server = serve(deps, now)
    const bound = await listen(server, p)
    if (bound === "taken") continue
    return {
      port: bound,
      close: () =>
        new Promise((resolve) => {
          server.closeAllConnections()
          server.close(() => resolve())
        }),
    }
  }
  return undefined
}
