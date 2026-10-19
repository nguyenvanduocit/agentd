import { randomBytes } from "node:crypto"
import { createServer, type Server, type Socket } from "node:net"
import { LineSplitter } from "./core/lines.ts"
import { bridgeAnswer, parseBridgeHello } from "./core/pagemcp.ts"

/** 256 random bits: the token is all that tells one page session's bridge from another's. */
const TOKEN_BYTES = 32

/** The page end of one session's bridge: where the agent's MCP lines go. */
export interface McpPage {
  send(line: string): void
  /** As FrameSink.backlog: a promise while the page has too much unsent, and the bridge is not read meanwhile. */
  backlog(): Promise<void> | undefined
}

/** One page session's claim on the relay; it lasts until `end`. */
export interface McpBinding {
  readonly token: string
  /** One line of the page's MCP server to the bridge; false when no bridge is connected. */
  toBridge(line: string): boolean
  /** The session or its page is gone: closes its bridge and refuses its token from now on. */
  end(): void
}

interface Bound {
  page: McpPage
  bridge: Socket | undefined
}

/**
 * The daemon's Unix socket for `agentd-mcp --page`: a bridge sends its session's token, then each line it reads goes
 * to that session's page and each line of the page goes back to it. The socket lives in the daemon's own temporary
 * directory (mode 0700), so only this OS user reaches it; the token picks the page session. It opens no port.
 */
export class McpRelay {
  private readonly bound = new Map<string, Bound>()

  private constructor(
    readonly path: string,
    private readonly server: Server,
  ) {
    server.on("connection", (sock) => this.accept(sock))
  }

  /** Listens on `path`; rejects when it cannot (a path too long for a socket, a directory that is gone). */
  static listen(path: string): Promise<McpRelay> {
    const server = createServer()
    return new Promise((resolve, reject) => {
      server.once("error", reject)
      server.listen(path, () => {
        server.off("error", reject)
        resolve(new McpRelay(path, server))
      })
    })
  }

  bind(page: McpPage): McpBinding {
    const token = randomBytes(TOKEN_BYTES).toString("base64url")
    const b: Bound = { page, bridge: undefined }
    this.bound.set(token, b)
    return {
      token,
      toBridge: (line) => {
        if (b.bridge === undefined) return false
        b.bridge.write(`${line}\n`)
        return true
      },
      end: () => {
        this.bound.delete(token)
        b.bridge?.destroy()
        b.bridge = undefined
      },
    }
  }

  /** Ends every bridge and stops listening. */
  async close(): Promise<void> {
    for (const b of this.bound.values()) b.bridge?.destroy()
    this.bound.clear()
    await new Promise<void>((resolve) => this.server.close(() => resolve()))
  }

  private accept(sock: Socket): void {
    const splitter = new LineSplitter()
    let b: Bound | undefined
    let refused = false
    sock.on("error", () => undefined) // the bridge went away: "close" follows
    sock.on("data", (chunk: Buffer) => {
      if (refused) return
      for (const line of splitter.push(chunk)) {
        if (b !== undefined) {
          if (line.trim() !== "") b.page.send(line)
          continue
        }
        b = this.claim(sock, line)
        refused = b === undefined
        if (refused) return
      }
      // The bridge cuts its own lines at the cap: a longer one is not from a bridge.
      if (splitter.tooLong) sock.destroy()
      else if (b !== undefined) this.waitForRoom(sock, b.page)
    })
  }

  /** The session of the hello `line`, which `sock` now bridges (a bridge before it is closed); undefined when it is refused. */
  private claim(sock: Socket, line: string): Bound | undefined {
    const token = parseBridgeHello(line)
    const b = token === undefined ? undefined : this.bound.get(token)
    if (b === undefined) {
      sock.end(`${bridgeAnswer("no open session has this token: its page or session closed, or it was never one")}\n`)
      return undefined
    }
    b.bridge?.destroy()
    b.bridge = sock
    sock.once("close", () => {
      if (b.bridge === sock) b.bridge = undefined
    })
    sock.write(`${bridgeAnswer(undefined)}\n`)
    return b
  }

  /** Stops reading the bridge while its page is behind. */
  private waitForRoom(sock: Socket, page: McpPage): void {
    const room = page.backlog()
    if (room === undefined) return
    sock.pause()
    void room.then(() => sock.resume())
  }
}
