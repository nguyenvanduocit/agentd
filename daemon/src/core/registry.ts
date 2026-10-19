import { serverOrigin } from "./server.ts"

/** A server app this daemon is paired with: it dials out to `server` with `token`. */
export interface App {
  server: string
  token: string
}

/** A web page origin the user allowed to run agents through the local gateway. */
export interface BrowserGrant {
  origin: string
  grantedAt: string
}

/** What daemon.json holds. */
export interface Registry {
  apps: App[]
  browsers: BrowserGrant[]
}

export function emptyRegistry(): Registry {
  return { apps: [], browsers: [] }
}

function isApp(v: unknown): v is App {
  const a = v as Partial<App> | null
  return typeof a?.server === "string" && typeof a.token === "string" && a.token !== ""
}

function isGrant(v: unknown): v is BrowserGrant {
  const g = v as Partial<BrowserGrant> | null
  return typeof g?.origin === "string" && typeof g.grantedAt === "string"
}

const NO_APP = "has no apps list. Pair again: agentd pair --app <url> --code <code>"

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    throw new Error("is not valid JSON. Pair again: agentd pair --app <url> --code <code>")
  }
}

/** Reads daemon.json: `{apps:[{server, token}], browsers:[{origin, grantedAt}]}`. */
export function parseRegistry(text: string): Registry {
  const o = parseJson(text) as { apps?: unknown; browsers?: unknown } | null
  const browsers = o?.browsers ?? []
  if (!Array.isArray(o?.apps) || !o.apps.every(isApp) || !Array.isArray(browsers) || !browsers.every(isGrant)) throw new Error(NO_APP)
  return { apps: o.apps.map(({ server, token }) => ({ server, token })), browsers: browsers.map(({ origin, grantedAt }) => ({ origin, grantedAt })) }
}

/** Adds the app, or replaces the token of the app with the same server; the order of first pairing stays. */
export function addApp(r: Registry, app: App): Registry {
  const at = r.apps.findIndex((a) => a.server === app.server)
  const apps = at < 0 ? [...r.apps, app] : r.apps.map((a, i) => (i === at ? app : a))
  return { ...r, apps }
}

export function addBrowser(r: Registry, origin: string, grantedAt: string): Registry {
  if (hasBrowser(r, origin)) return r
  return { ...r, browsers: [...r.browsers, { origin, grantedAt }] }
}

/** Drops the app whose server is `origin` and the browser grant of `origin`. */
export function removeOrigin(r: Registry, origin: string): Registry {
  return { apps: r.apps.filter((a) => a.server !== origin), browsers: r.browsers.filter((g) => g.origin !== origin) }
}

export function hasApp(r: Registry, server: string): boolean {
  return r.apps.some((a) => a.server === server)
}

export function hasBrowser(r: Registry, origin: string): boolean {
  return r.browsers.some((g) => g.origin === origin)
}

/**
 * What still runs for an origin that `r` no longer has, as `agentd apps remove` leaves it: of the `linked` app servers
 * and the origins with open `pages`, those to end.
 */
export function withdrawn(r: Registry, linked: string[], pages: string[]): { apps: string[]; pages: string[] } {
  return { apps: linked.filter((s) => !hasApp(r, s)), pages: pages.filter((o) => !hasBrowser(r, o)) }
}

/** Every app, or only the one with the origin `server`. */
export function selectApps(r: Registry, server: string | undefined): App[] {
  if (server === undefined) return r.apps
  const origin = serverOrigin(server)
  const found = r.apps.filter((a) => a.server === origin)
  if (found.length === 0) throw new Error(`not paired with ${origin}. Pair first: agentd pair --app ${origin} --code <code>`)
  return found
}

/** The one app a process (the MCP bridge) talks to: the named one, or the only one. */
export function selectApp(r: Registry, server: string | undefined): App {
  const [app] = selectApps(r, server)
  if (app === undefined) throw new Error("not paired. Run: agentd pair --app <url> --code <code>")
  if (server === undefined && r.apps.length > 1) throw new Error("several apps are paired: pass --app <url> to say which one")
  return app
}
