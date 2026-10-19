import { serverOrigin } from "./server.ts"

/** The Host header of a request to the gateway: a loopback name and the gateway's own port. Anything else is DNS rebinding. */
export function hostAllowed(host: string | undefined, port: number): boolean {
  if (host === undefined) return false
  const h = host.toLowerCase()
  return h === `127.0.0.1:${port}` || h === `localhost:${port}`
}

/**
 * The page's origin from its Origin header, or undefined when it is missing, `null`, has a path, or may not be
 * trusted (plain http is accepted for a loopback host only, as for server addresses).
 */
export function originOf(header: string | undefined): string | undefined {
  if (header === undefined || header === "" || header === "null") return undefined
  try {
    if (new URL(header).origin !== header) return undefined
    return serverOrigin(header)
  } catch {
    return undefined
  }
}

/** CORS for one caller. The origin is reflected, never "*": the answer differs per origin. */
export function corsHeaders(origin: string, requestHeaders: Readonly<Record<string, string | undefined>>): Record<string, string> {
  const headers: Record<string, string> = {
    "Access-Control-Allow-Origin": origin,
    Vary: "Origin",
    "Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
    "Access-Control-Allow-Headers": "content-type",
  }
  if (requestHeaders["access-control-request-private-network"] === "true") headers["Access-Control-Allow-Private-Network"] = "true"
  return headers
}

export type ConnectRequest = { mode: "server"; code: string } | { mode: "browser" }

/**
 * The body of POST /v1/connect, or undefined when it is malformed. The app a page connects is the one at its own
 * origin: a body that names a server is refused, so a page cannot point the daemon at someone else's server.
 */
export function parseConnect(body: unknown): ConnectRequest | undefined {
  const b = body as { mode?: unknown; code?: unknown; server?: unknown } | null
  if (b?.mode === "browser") return { mode: "browser" }
  if (b?.mode !== "server" || typeof b.code !== "string" || b.code === "" || b.server !== undefined) return undefined
  return { mode: "server", code: b.code }
}

/** Origins that said no (or did not answer) wait before the dialog can open for them again. */
export class Cooldown {
  private readonly until = new Map<string, number>()

  constructor(
    private readonly ms: number,
    private readonly now: () => number,
  ) {}

  block(origin: string): void {
    this.until.set(origin, this.now() + this.ms)
  }

  blocked(origin: string): boolean {
    return (this.until.get(origin) ?? 0) > this.now()
  }
}

/**
 * Refusals count across every origin: a page that asks from a new subdomain after each Deny would otherwise open a
 * dialog each time. `strikes` refusals within `windowMs` lock every dialog until the oldest of them is out of the window.
 */
export class Lockout {
  private strikes: number[] = []

  constructor(
    private readonly max: number,
    private readonly windowMs: number,
    private readonly now: () => number,
  ) {}

  strike(): void {
    this.strikes = [...this.recent(), this.now()]
  }

  locked(): boolean {
    return this.recent().length >= this.max
  }

  /** An Allow: the user is paying attention, what came before no longer counts. */
  reset(): void {
    this.strikes = []
  }

  private recent(): number[] {
    return this.strikes.filter((t) => t + this.windowMs > this.now())
  }
}

export const DEFAULT_PORT = 47615
/** The gateway tries this many ports from the first one. */
export const PORT_RANGE = 10

/** The first port the gateway tries: $AGENTD_LOCAL_PORT (0 = any free port, for tests), else 47615. */
export function gatewayPort(env: Readonly<Record<string, string | undefined>>): number {
  const raw = env.AGENTD_LOCAL_PORT
  if (raw === undefined || raw === "") return DEFAULT_PORT
  const port = Number(raw)
  if (!Number.isInteger(port) || port < 0 || port + PORT_RANGE - 1 > 65535) {
    throw new Error(`AGENTD_LOCAL_PORT "${raw}" is not a port from 0 to ${65535 - PORT_RANGE + 1}`)
  }
  return port
}
