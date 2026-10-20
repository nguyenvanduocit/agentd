/** Client of the agentd's local gateway (127.0.0.1): detect it, ask to connect, run an agent from a page. */

export const DEFAULT_PORTS: readonly number[] = Array.from({ length: 10 }, (_, i) => 47615 + i)
const DETECT_TIMEOUT_MS = 800

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

export type Detected = { found: false } | { found: true; port: number; version: string; granted: Granted; agents: AgentInfo[] | undefined }

export type Reason =
  | "denied"
  | "timeout"
  | "cooldown"
  | "busy"
  | "no-dialog"
  | "pairing"
  | "origin"
  | "malformed"
  | "unreachable"
  | "peer"
  | "host"
  | "not-found"
  | "error"

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

/** An agent as the daemon sends it: a 0.2 daemon sends no title. */
type WireAgent = Partial<AgentInfo> & { name: string }

function agentInfo(a: WireAgent): AgentInfo {
  return { name: a.name, title: a.title || a.name, ready: a.ready === true, reason: a.reason ?? "" }
}

async function probe(port: number, o: DetectOptions): Promise<Detected> {
  const ms = o.timeoutMs ?? DETECT_TIMEOUT_MS
  try {
    const res = await within(ms, (o.fetch ?? fetch)(statusUrl(port), { signal: AbortSignal.timeout(ms) }))
    if (!res.ok) return { found: false }
    const body = (await res.json()) as { name?: unknown; version?: unknown; granted?: Granted; agents?: WireAgent[] }
    if (body.name !== "agentd" || typeof body.version !== "string") return { found: false }
    const agents = body.agents?.map(agentInfo)
    return { found: true, port, version: body.version, granted: body.granted ?? { server: false, browser: false }, agents }
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
  peer: "The daemon on this computer runs as another user than this browser, so it refuses the request.",
  host: "The daemon did not accept the address this page used to reach it. Open the page from its usual address.",
  "not-found": "The daemon does not know this request. Update the daemon and try again.",
  error: "The daemon could not carry out the request. Look at its log, then try again.",
}

/**
 * A sentence for a person about why a connection did not happen. A reason this SDK does not know (a newer daemon's)
 * gets a sentence that names it, never undefined.
 */
export function reasonText(reason: Reason): string {
  return (TEXT as Record<string, string | undefined>)[reason] ?? `The daemon on this computer refused the request (${reason}).`
}

export interface Frame {
  kind: "out" | "err" | "exit"
  data: string
  code: number
}

/** A stdio MCP server as ACP's session/new takes it in `mcpServers`. */
export interface McpServer {
  name: string
  command: string
  args: string[]
  env: { name: string; value: string }[]
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
  const send = (m: object): void => ws.send(JSON.stringify(m))
  const write = (line: string): void => send({ t: "write", id, line })
  const end = (): void => {
    stdout.end()
    mcpIn.end()
  }
  return new Promise((resolve, reject) => {
    ws.onerror = () => reject(new Error("cannot open a session: the daemon refused the connection (is this page allowed?)"))
    ws.onclose = () => {
      reject(new Error("the daemon closed the session"))
      end()
      for (const c of closers) c()
    }
    ws.onmessage = (e: MessageEvent<string>) => {
      const m = JSON.parse(e.data) as Incoming
      if (m.t === "hello") {
        version = m.version ?? version
        send({ t: "open", id, agent })
      } else if (m.id !== id) return
      else if (m.t === "error") reject(new Error(m.message))
      else if (m.t === "mcp") mcpIn.line(m.line ?? "")
      else if (m.t === "frame") {
        const frame: Frame = { kind: m.kind ?? "out", data: m.data ?? "", code: m.code ?? 0 }
        if (frame.kind === "out") stdout.line(frame.data)
        else if (frame.kind === "exit") end()
        for (const f of frames) f(frame)
      } else if (m.t === "opened") {
        const mcpOut = lineWriter((line) => send({ t: "mcp", id, line }))
        resolve({
          pid: m.pid ?? -1,
          cwd: m.cwd ?? "",
          mcp: {
            get readable() {
              return mcpIn.stream
            },
            writable: mcpOut,
          },
          mcpServer: (name) => mcpServerOf(m.mcp, version, name),
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
        })
      }
    }
  })
}
