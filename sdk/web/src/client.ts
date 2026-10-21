/** Client of the agentd's local gateway (127.0.0.1): detect it, ask to connect, run an agent from a page. */

export const DEFAULT_PORTS: readonly number[] = Array.from({ length: 10 }, (_, i) => 47615 + i)
const DETECT_TIMEOUT_MS = 800

/** The versions of the daemon's page protocol (`protocol` of GET /v1/status) this SDK speaks. */
export const SUPPORTED_PROTOCOLS: readonly number[] = [1, 2]

export interface AgentInfo {
  name: string
  /** What to call the agent on screen ("Claude Code"); its name when the daemon gives none. */
  title: string
  ready: boolean
  reason: string
}

export interface Granted {
  server: boolean
  browser: boolean
}

/**
 * A daemon `detect` found. `protocol` is the version of the page protocol the daemon states; a daemon that predates the
 * statement sends none, and the key is then absent.
 */
export type FoundDaemon = { found: true; port: number; version: string; granted: Granted; agents: AgentInfo[] | undefined; protocol?: number }

/**
 * No daemon answered like one. When a program on one of the ports did refuse this page with agentd's own refusal (the
 * gateway answers `origin` to a request without an Origin header, `host`, `peer`), `refused` says which: that daemon is
 * there, and this page is not what it talks to (since 0.7.0).
 */
export type AbsentDaemon = { found: false; refused?: Reason; message?: string }

export type Detected = AbsentDaemon | FoundDaemon

/**
 * Why the daemon refused a request, or the SDK could not ask it. The words for each are `reasonText(reason)`.
 * `locked`, `no-folder` and `not-saved` are the daemon's more specific `code` for `cooldown`, `no-dialog` and
 * `pairing`; `unreachable`, `cancelled` and `incompatible` come from the SDK itself.
 */
export type KnownReason =
  | "denied"
  | "timeout"
  | "cooldown"
  | "locked"
  | "busy"
  | "no-dialog"
  | "no-folder"
  | "pairing"
  | "not-saved"
  | "origin"
  | "host"
  | "malformed"
  | "not-found"
  | "error"
  | "peer"
  | "unreachable"
  | "cancelled"
  | "incompatible"

/** A `KnownReason`, or the reason of a newer daemon, as it was sent: `reasonText` has words for it too. */
export type Reason = KnownReason | (string & {})

/** `retryAfter` is the seconds after which asking again can work, when the daemon said (`cooldown`, `locked`). */
export type ConnectResult = { granted: true } | { granted: false; reason: Reason; message?: string; retryAfter?: number }

export interface Daemon {
  port: number
}

/** The part of `fetch` this SDK calls. The global `fetch`, a wrapper that adds a header, or a fake all fit. */
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>

export interface FetchOptions {
  fetch?: FetchLike
  /** Stops the request; the answer is then the reason `cancelled` (the dialog on the user's computer may still be open). */
  signal?: AbortSignal
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

/** An agent as the daemon sends it: a 0.2 daemon sends no title. */
type WireAgent = Partial<AgentInfo> & { name: string }

function agentInfo(a: WireAgent): AgentInfo {
  return { name: a.name, title: a.title || a.name, ready: a.ready === true, reason: a.reason ?? "" }
}

/** What the gateway says when it refuses a request: `reason` is the legacy value, `code` the more specific one (since daemon 0.7). */
interface RefusalBody {
  granted?: unknown
  reason?: unknown
  code?: unknown
  message?: unknown
  retryAfter?: unknown
}

/** The reason of a refusal: its `code` when this SDK knows it, else its `reason` (which every SDK knows), else "malformed". */
function reasonOf(body: RefusalBody): Reason {
  if (typeof body.code === "string" && Object.hasOwn(TEXT, body.code)) return body.code
  return typeof body.reason === "string" ? body.reason : "malformed"
}

const messageOf = (body: RefusalBody): { message?: string } => (typeof body.message === "string" ? { message: body.message } : {})

/** The seconds after which asking again can work: the body's `retryAfter`, else the `Retry-After` header. */
function retryAfterOf(body: RefusalBody, res: Response): { retryAfter?: number } {
  if (typeof body.retryAfter === "number" && Number.isFinite(body.retryAfter) && body.retryAfter >= 0) return { retryAfter: body.retryAfter }
  const header = Number(res.headers.get("retry-after"))
  return Number.isFinite(header) && header > 0 ? { retryAfter: header } : {}
}

/** An answer that is agentd's own refusal: `{granted: false, reason}`. Anything else a program on the port says is not. */
function isRefusal(body: RefusalBody): boolean {
  return body.granted === false && typeof body.reason === "string"
}

async function probe(port: number, o: DetectOptions): Promise<Detected> {
  const ms = o.timeoutMs ?? DETECT_TIMEOUT_MS
  const ask = async (): Promise<Detected> => {
    const res = await (o.fetch ?? fetch)(statusUrl(port), { signal: AbortSignal.timeout(ms) })
    if (!res.ok) {
      const refusal = (await res.json().catch(() => ({}))) as RefusalBody
      return isRefusal(refusal) ? { found: false, refused: reasonOf(refusal), ...messageOf(refusal) } : { found: false }
    }
    const body = (await res.json()) as { name?: unknown; version?: unknown; granted?: Granted; agents?: WireAgent[]; protocol?: unknown }
    if (body.name !== "agentd" || typeof body.version !== "string") return { found: false }
    const agents = body.agents?.map(agentInfo)
    const protocol = typeof body.protocol === "number" ? { protocol: body.protocol } : {}
    return { found: true, port, version: body.version, granted: body.granted ?? { server: false, browser: false }, agents, ...protocol }
  }
  try {
    return await within(ms, ask())
  } catch {
    // Nothing listens there, the browser blocked the call, or the user said no to its prompt: all look the same.
    return { found: false }
  }
}

/**
 * Looks for a daemon on this machine. A browser that blocks the call looks the same as no daemon; a daemon that answers
 * with its own refusal (a page without an Origin header, an address it does not accept) is reported in `refused`.
 */
export async function detect(o: DetectOptions = {}): Promise<Detected> {
  const results = await Promise.all((o.ports ?? DEFAULT_PORTS).map((p) => probe(p, o)))
  return results.find((r): r is FoundDaemon => r.found) ?? results.find((r) => !r.found && r.refused !== undefined) ?? { found: false }
}

/** What a request that threw means: the caller stopped it (`cancelled`), or nothing answered (`unreachable`). */
function failure(e: unknown, o: FetchOptions): { reason: Reason; message: string } {
  const message = e instanceof Error ? e.message : String(e)
  const stopped = o.signal?.aborted === true || (e instanceof Error && e.name === "AbortError")
  return { reason: stopped ? "cancelled" : "unreachable", message }
}

async function connectRequest(d: Daemon, init: RequestInit, path: string, o: FetchOptions): Promise<ConnectResult> {
  try {
    const res = await (o.fetch ?? fetch)(`${base(d)}${path}`, o.signal === undefined ? init : { ...init, signal: o.signal })
    const body = (await res.json().catch(() => ({}))) as RefusalBody
    if (res.ok && body.granted === true) return { granted: true }
    return { granted: false, reason: reasonOf(body), ...messageOf(body), ...retryAfterOf(body, res) }
  } catch (e) {
    return { granted: false, ...failure(e, o) }
  }
}

const post = (body: object): RequestInit => ({ method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) })

/**
 * Asks the user (in a dialog on their machine) to let the daemon connect to this page's own server with this pairing
 * code. The server is the page's origin: the daemon refuses to connect to any other. `name` is what the app calls
 * itself: the dialog and the daemon's menu show it beside the origin, and the daemon refuses a request without one.
 */
export function connectServer(d: Daemon, a: { name: string; code: string }, o: FetchOptions = {}): Promise<ConnectResult> {
  return connectRequest(d, post({ mode: "server", name: a.name, code: a.code }), "/v1/connect", o)
}

/** Asks the user to let this page run agents on their machine directly. `name` is what the app calls itself, as for `connectServer`. */
export function connectBrowser(d: Daemon, a: { name: string }, o: FetchOptions = {}): Promise<ConnectResult> {
  return connectRequest(d, post({ mode: "browser", name: a.name }), "/v1/connect", o)
}

/** Whether `disconnect` took the page's grant back; if not, the reason, as for a refused connection. */
export type DisconnectResult = { disconnected: true } | { disconnected: false; reason: Reason; message?: string }

/**
 * Takes back what this page was allowed: its browser grant and the app at its own origin. Open sessions end. The
 * daemon answers 200 when it is done (its body says nothing is granted any more, which is not a refusal), so success is
 * the status, not `granted`.
 */
export async function disconnect(d: Daemon, o: FetchOptions = {}): Promise<DisconnectResult> {
  try {
    const res = await (o.fetch ?? fetch)(`${base(d)}/v1/connect`, o.signal === undefined ? { method: "DELETE" } : { method: "DELETE", signal: o.signal })
    if (res.ok) return { disconnected: true }
    const body = (await res.json().catch(() => ({}))) as RefusalBody
    return { disconnected: false, reason: reasonOf(body), ...messageOf(body) }
  } catch (e) {
    return { disconnected: false, ...failure(e, o) }
  }
}

// The sentences are those of contract/testdata/page-reasons.json, which the gateway and the Go client test against too.
const TEXT: Record<KnownReason, string> = {
  denied: "You declined. The app stays disconnected from this computer.",
  timeout: "The dialog on this computer was not answered in time. Try again and answer it.",
  cooldown: "You declined a moment ago. Wait half a minute before asking again.",
  locked: "Several connection requests were declined in a row, so agentd is not asking for a while. Try again in a few minutes.",
  busy: "Another connection request is waiting for an answer on this computer. Answer it first.",
  "no-dialog": "This computer cannot show the confirmation dialog (no graphical session), so nothing was connected.",
  "no-folder": "No folder was chosen, so nothing was connected. Choose at least one folder to allow.",
  pairing: "The app refused the pairing code. Start the connection again to get a fresh one.",
  "not-saved": "agentd could not save the connection on this computer. Look at its log, then try again.",
  origin: "agentd does not accept requests from this address. Use the address the app is served from, without a path.",
  host: "agentd did not accept the address used to reach it. Open the page from its usual address.",
  malformed: "agentd did not understand the request. Update agentd and try again.",
  "not-found": "This agentd does not know the request. Update agentd and try again.",
  error: "agentd could not carry out the request. Look at its log, then try again.",
  peer: "agentd runs as another user than this program, so it refuses the request.",
  unreachable: "agentd on this computer did not answer. Check that it is running.",
  cancelled: "You stopped waiting. The dialog may still be open: answering Allow there still connects.",
  incompatible: "This agentd cannot work with this page. Update agentd, or come back after the app updates.",
}

/**
 * A sentence for a person about why a connection did not happen. A reason this SDK does not know (a newer daemon's)
 * gets a sentence that names it, never undefined.
 */
export function reasonText(reason: Reason): string {
  return Object.hasOwn(TEXT, reason) ? TEXT[reason as KnownReason] : `agentd on this computer refused the request (${reason}).`
}

export interface Frame {
  kind: "out" | "err" | "exit"
  data: string
  code: number
}

/** The `_meta` that ACP allows on its objects. It is here so that these types fit ACP's: see `McpServer`. */
export type AcpMeta = { [key: string]: unknown } | null

/** One environment variable of a stdio MCP server, as ACP types it. */
export interface McpEnvVariable {
  name: string
  value: string
  _meta?: AcpMeta
}

/**
 * A stdio MCP server as ACP's session/new takes it in `mcpServers`. The optional `_meta` of the server and of each
 * variable is ACP's: without them the official SDK's `request("session/new", …)` does not accept a server of this type
 * as a typed argument and answers `unknown` (since 0.7.0).
 */
export interface McpServer {
  name: string
  command: string
  args: string[]
  env: McpEnvVariable[]
  _meta?: AcpMeta
}

/** The page's end of the bridge between the agent and the page's own MCP server, as an MCP transport takes it. */
export interface McpStreams {
  /**
   * The lines of the agent's MCP client, as bytes, each ending in "\n"; it ends with the session. Like `readable`, it
   * starts the first time it is taken and keeps no line from before: take it before session/new.
   */
  readonly readable: ReadableStream<Uint8Array>
  /** The lines of the page's MCP server, as bytes: each complete line goes to the agent's MCP client. */
  readonly writable: WritableStream<Uint8Array>
}

export interface AgentSession {
  pid: number
  cwd: string
  /** The page's own MCP server, as the agent reaches it once session/new lists `mcpServer(name)`. */
  readonly mcp: McpStreams
  /**
   * The MCP server, named `name`, to list in session/new's `mcpServers`: the agent then reaches the page's server through
   * `mcp`. Throws on a daemon that does not bridge a page's MCP server (agentd 0.3 and older).
   */
  mcpServer(name: string): McpServer
  /**
   * Moves the agent to `dir`, an absolute path to a directory the user allowed this page. The daemon ends the agent and
   * starts the same program in `dir` on this session's streams: `readable` and `writable` carry on with the new process,
   * and `pid` and `cwd` are its own once this resolves. It is a new agent that has not been initialized, so the page
   * speaks ACP to it from the start (`initialize`, then `session/new` or `session/load`); lines the old agent said and
   * the page has not read yet stay in `readable`. Rejects with a SessionError carrying the daemon's message when the
   * daemon refuses (a folder the user did not allow, not a directory), and the agent stays where it is, except that an
   * agent which cannot start in `dir` is gone and ends the session. An agentd that predates `chdir` refuses it as an
   * unknown message.
   */
  chdir(dir: string): Promise<void>
  /** One line to the agent's stdin. */
  write(line: string): void
  /**
   * The agent's stdout as bytes, each line ending in "\n"; it ends when the agent exits or the socket closes. The stream
   * starts the first time it is taken and keeps no line from before: take it right after the session opens (an ACP agent speaks only when asked).
   */
  readonly readable: ReadableStream<Uint8Array>
  /** Bytes to the agent's stdin: each complete line is one `write`, empty lines are dropped. */
  readonly writable: WritableStream<Uint8Array>
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
  version?: string
  line?: string
  mcp?: Omit<McpServer, "name">
}

export interface SessionOptions {
  WebSocket?: typeof WebSocket
}

/**
 * Why `openSession` could not open an agent: `reason` is `unreachable` when the socket could not be opened or was closed
 * (a page that has no grant is hung up on, and the browser says no more than that), and `error` when the daemon refused
 * the open itself, with its own words in `message` (an agent it does not know, one that is not ready). Since 0.7.0.
 */
export class SessionError extends Error {
  readonly reason: Reason

  constructor(message: string, reason: Reason) {
    super(message)
    this.name = "SessionError"
    this.reason = reason
  }
}

/** Splits text into the complete lines it ends and the start of the next one. */
function splitLines(text: string): { lines: string[]; rest: string } {
  const parts = text.split("\n")
  return { lines: parts.slice(0, -1), rest: parts.at(-1) ?? "" }
}

/** Bytes cut anywhere, sent on as lines: a line, or a character, cut between two chunks waits for its other half. */
function lineWriter(write: (line: string) => void): WritableStream<Uint8Array> {
  const decoder = new TextDecoder()
  let rest = ""
  const send = (text: string): void => {
    const split = splitLines(rest + text)
    rest = split.rest
    for (const line of split.lines) if (line !== "") write(line)
  }
  return new WritableStream({
    write: (chunk) => send(decoder.decode(chunk, { stream: true })),
    close: () => send(decoder.decode() + "\n"), // the last line has no newline left to wait for
  })
}

/** Lines (stdout, or the agent's MCP lines) as bytes from the first time the stream is taken; a session that never takes it keeps no line. */
function lineReader() {
  const encoder = new TextEncoder()
  let stream: ReadableStream<Uint8Array> | undefined
  let controller: ReadableStreamDefaultController<Uint8Array> | undefined
  let ended = false
  return {
    get stream(): ReadableStream<Uint8Array> {
      stream ??= new ReadableStream({
        start: (c) => (ended ? c.close() : void (controller = c)),
        cancel: () => void (controller = undefined),
      })
      return stream
    },
    line: (data: string): void => controller?.enqueue(encoder.encode(data + "\n")),
    end: (): void => {
      if (ended) return
      ended = true
      controller?.close()
      controller = undefined
    },
  }
}

/** The server of `opened.mcp` under `name`, a copy each time; a daemon that sent none bridges no page's MCP server. */
function mcpServerOf(wire: Incoming["mcp"], version: string, name: string): McpServer {
  if (wire === undefined) throw new Error(`agentd ${version} does not bridge a page's MCP server: update agentd on this computer`)
  return { name, command: wire.command, args: [...wire.args], env: wire.env.map((e) => ({ name: e.name, value: e.value })) }
}

/** Opens `agent` on the daemon over a socket of its own; the page is the ACP client of the lines. */
export function openSession(d: Daemon, agent: string, o: SessionOptions = {}): Promise<AgentSession> {
  const Socket = o.WebSocket ?? WebSocket
  const id = crypto.randomUUID()
  const ws = new Socket(`ws://127.0.0.1:${d.port}/v1/session`)
  const frames: ((f: Frame) => void)[] = []
  const closers: (() => void)[] = []
  const stdout = lineReader()
  const mcpIn = lineReader()
  let version = "(unknown version)"
  let moving: { resolve: (m: Incoming) => void; reject: (e: SessionError) => void } | undefined
  const send = (m: object): void => ws.send(JSON.stringify(m))
  const write = (line: string): void => send({ t: "write", id, line })
  const end = (): void => {
    stdout.end()
    mcpIn.end()
  }
  return new Promise((resolve, reject) => {
    ws.onerror = () => reject(new SessionError("cannot open a session: the daemon refused the connection (is this page allowed?)", "unreachable"))
    ws.onclose = () => {
      reject(new SessionError("the daemon closed the session", "unreachable"))
      moving?.reject(new SessionError("the daemon closed the session", "unreachable"))
      end()
      for (const c of closers) c()
    }
    ws.onmessage = (e: MessageEvent<string>) => {
      const m = JSON.parse(e.data) as Incoming
      if (m.t === "hello") {
        version = m.version ?? version
        send({ t: "open", id, agent })
      } else if (m.id !== id) return
      else if (m.t === "error") {
        reject(new SessionError(m.message ?? "the daemon refused to open the agent", "error"))
        moving?.reject(new SessionError(m.message ?? "the daemon refused to move the agent", "error"))
      } else if (m.t === "moved") moving?.resolve(m)
      else if (m.t === "mcp") mcpIn.line(m.line ?? "")
      else if (m.t === "frame") {
        const frame: Frame = { kind: m.kind ?? "out", data: m.data ?? "", code: m.code ?? 0 }
        if (frame.kind === "out") stdout.line(frame.data)
        else if (frame.kind === "exit") end()
        for (const f of frames) f(frame)
      } else if (m.t === "opened") {
        const mcpOut = lineWriter((line) => send({ t: "mcp", id, line }))
        const session: AgentSession = {
          pid: m.pid ?? -1,
          cwd: m.cwd ?? "",
          mcp: {
            get readable() {
              return mcpIn.stream
            },
            writable: mcpOut,
          },
          mcpServer: (name) => mcpServerOf(m.mcp, version, name),
          chdir: (dir) => {
            if (moving) return Promise.reject(new SessionError("the agent is already moving", "error"))
            return new Promise<void>((done, fail) => {
              const settle = (): void => {
                moving = undefined
              }
              moving = {
                resolve: (moved) => {
                  settle()
                  session.pid = moved.pid ?? -1
                  session.cwd = moved.cwd ?? ""
                  done()
                },
                reject: (e) => {
                  settle()
                  fail(e)
                },
              }
              send({ t: "chdir", id, dir })
            })
          },
          write,
          get readable() {
            return stdout.stream
          },
          writable: lineWriter(write),
          onFrame: (cb) => void frames.push(cb),
          onClose: (cb) => void closers.push(cb),
          close: () => {
            send({ t: "close", id })
            ws.close()
          },
        }
        resolve(session)
      }
    }
  })
}
