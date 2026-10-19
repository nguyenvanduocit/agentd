/** Client of the agentd's local gateway (127.0.0.1): detect it, ask to connect, run an agent from a page. */

export const DEFAULT_PORTS: readonly number[] = Array.from({ length: 10 }, (_, i) => 47615 + i)
const DETECT_TIMEOUT_MS = 800

export interface AgentInfo {
  name: string
  ready: boolean
  reason: string
}

export interface Granted {
  server: boolean
  browser: boolean
}

export type Detected = { found: false } | { found: true; port: number; version: string; granted: Granted; agents: AgentInfo[] | undefined }

export type Reason = "denied" | "timeout" | "cooldown" | "busy" | "no-dialog" | "pairing" | "origin" | "malformed" | "unreachable"

export type ConnectResult = { granted: true } | { granted: false; reason: Reason; message?: string }

export interface Daemon {
  port: number
}

export interface FetchOptions {
  fetch?: typeof fetch
}

export interface DetectOptions extends FetchOptions {
  ports?: readonly number[]
  timeoutMs?: number
}

const base = (d: Daemon): string => `http://127.0.0.1:${d.port}`

const statusUrl = (port: number): string => `http://127.0.0.1:${port}/v1/status`

/** Rejects after `ms` whatever the fetch does: a fetch that ignores its AbortSignal must not hold detect() up. */
function within<T>(ms: number, work: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error("no answer")), ms)
  })
  return Promise.race([work, late]).finally(() => clearTimeout(timer))
}

async function probe(port: number, o: DetectOptions): Promise<Detected> {
  const ms = o.timeoutMs ?? DETECT_TIMEOUT_MS
  try {
    const res = await within(ms, (o.fetch ?? fetch)(statusUrl(port), { signal: AbortSignal.timeout(ms) }))
    if (!res.ok) return { found: false }
    const body = (await res.json()) as { name?: unknown; version?: unknown; granted?: Granted; agents?: AgentInfo[] }
    if (body.name !== "agentd" || typeof body.version !== "string") return { found: false }
    return { found: true, port, version: body.version, granted: body.granted ?? { server: false, browser: false }, agents: body.agents }
  } catch {
    // Nothing listens there, the browser blocked the call, or the user said no to its prompt: all look the same.
    return { found: false }
  }
}

/** Looks for a daemon on this machine. A browser that blocks the call looks the same as no daemon. */
export async function detect(o: DetectOptions = {}): Promise<Detected> {
  const results = await Promise.all((o.ports ?? DEFAULT_PORTS).map((p) => probe(p, o)))
  return results.find((r) => r.found) ?? { found: false }
}

async function connectRequest(d: Daemon, init: RequestInit, path: string, o: FetchOptions): Promise<ConnectResult> {
  try {
    const res = await (o.fetch ?? fetch)(`${base(d)}${path}`, init)
    const body = (await res.json().catch(() => ({}))) as { granted?: boolean; reason?: Reason; message?: string }
    if (res.ok && body.granted === true) return { granted: true }
    return { granted: false, reason: body.reason ?? "malformed", ...(body.message === undefined ? {} : { message: body.message }) }
  } catch (e) {
    return { granted: false, reason: "unreachable", message: e instanceof Error ? e.message : String(e) }
  }
}

const post = (body: object): RequestInit => ({ method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) })

/**
 * Asks the user (in a dialog on their machine) to let the daemon connect to this page's own server with this pairing
 * code. The server is the page's origin: the daemon refuses to connect to any other.
 */
export function connectServer(d: Daemon, a: { code: string }, o: FetchOptions = {}): Promise<ConnectResult> {
  return connectRequest(d, post({ mode: "server", code: a.code }), "/v1/connect", o)
}

/** Asks the user to let this page run agents on their machine directly. */
export function connectBrowser(d: Daemon, o: FetchOptions = {}): Promise<ConnectResult> {
  return connectRequest(d, post({ mode: "browser" }), "/v1/connect", o)
}

/** Takes back what this page was allowed: its browser grant and the app at its own origin. Open sessions end. */
export async function disconnect(d: Daemon, o: FetchOptions = {}): Promise<void> {
  await connectRequest(d, { method: "DELETE" }, "/v1/connect", o)
}

const TEXT: Record<Reason, string> = {
  denied: "You declined. The app stays disconnected from this computer.",
  timeout: "The dialog on this computer was not answered in time. Try again and answer it.",
  cooldown: "You declined a moment ago. Wait half a minute before asking again.",
  busy: "Another connection request is waiting for an answer on this computer. Answer it first.",
  "no-dialog": "This computer cannot show the confirmation dialog (no graphical session), so nothing was connected.",
  pairing: "The app refused the pairing code. Start the connection again to get a fresh one.",
  origin: "The daemon does not accept requests from this page's address.",
  malformed: "The daemon did not understand the request. Update the daemon and try again.",
  unreachable: "The daemon on this computer did not answer. Check that it is running.",
}

/** A sentence for a person about why a connection did not happen. */
export function reasonText(reason: Reason): string {
  return TEXT[reason]
}

export interface Frame {
  kind: "out" | "err" | "exit"
  data: string
  code: number
}

export interface AgentSession {
  pid: number
  cwd: string
  /** One line to the agent's stdin. */
  write(line: string): void
  /** The agent's stdout lines, stderr text and exit. */
  onFrame(cb: (frame: Frame) => void): void
  /** The socket ended: the agent is gone with it. */
  onClose(cb: () => void): void
  close(): void
}

interface Incoming {
  t?: string
  id?: string
  pid?: number
  cwd?: string
  message?: string
  kind?: Frame["kind"]
  data?: string
  code?: number
}

export interface SessionOptions {
  WebSocket?: typeof WebSocket
}

/** Opens `agent` on the daemon over a socket of its own; the page is the ACP client of the lines. */
export function openSession(d: Daemon, agent: string, o: SessionOptions = {}): Promise<AgentSession> {
  const Socket = o.WebSocket ?? WebSocket
  const id = crypto.randomUUID()
  const ws = new Socket(`ws://127.0.0.1:${d.port}/v1/session`)
  const frames: ((f: Frame) => void)[] = []
  const closers: (() => void)[] = []
  const send = (m: object): void => ws.send(JSON.stringify(m))
  return new Promise((resolve, reject) => {
    ws.onerror = () => reject(new Error("cannot open a session: the daemon refused the connection (is this page allowed?)"))
    ws.onclose = () => {
      reject(new Error("the daemon closed the session"))
      for (const c of closers) c()
    }
    ws.onmessage = (e: MessageEvent<string>) => {
      const m = JSON.parse(e.data) as Incoming
      if (m.t === "hello") send({ t: "open", id, agent })
      else if (m.t === "error" && m.id === id) reject(new Error(m.message))
      else if (m.t === "frame" && m.id === id) for (const f of frames) f({ kind: m.kind ?? "out", data: m.data ?? "", code: m.code ?? 0 })
      else if (m.t === "opened" && m.id === id) {
        resolve({
          pid: m.pid ?? -1,
          cwd: m.cwd ?? "",
          write: (line) => send({ t: "write", id, line }),
          onFrame: (cb) => void frames.push(cb),
          onClose: (cb) => void closers.push(cb),
          close: () => {
            send({ t: "close", id })
            ws.close()
          },
        })
      }
    }
  })
}
