import { io, type Socket } from "socket.io-client"
import { createRpcClient, type RpcClient } from "../../contract/client.generated.ts"
import { backoffDelay, type BackoffOptions } from "./core/backoff.ts"
import { namespaceUrl } from "./core/server.ts"
import type { FrameSink } from "./session.ts"

const RECONNECT: BackoffOptions = { baseMs: 500, maxMs: 15_000, jitter: 0.5 }

/**
 * What the hub answers a token it will never accept (unknown or revoked). Any other refusal, such as `unavailable`
 * (the server could not check the token now), is retried.
 */
export const REFUSED = "unauthorized"

export interface LinkOptions {
  /** The server origin (http or https). */
  server: string
  auth: Record<string, string>
  /** Give up after this many failed reconnects; the daemon retries for as long as it runs. */
  reconnectionAttempts?: number
}

/** Opens the socket to the /agentd namespace. */
export function openSocket(o: LinkOptions): Socket {
  return io(namespaceUrl(o.server), {
    transports: ["websocket"],
    auth: o.auth,
    reconnection: true,
    reconnectionDelay: RECONNECT.baseMs,
    reconnectionDelayMax: RECONNECT.maxMs,
    randomizationFactor: RECONNECT.jitter,
    reconnectionAttempts: o.reconnectionAttempts ?? Infinity,
  })
}

/** What the daemon's side of one connection reports. */
export interface LinkEvents {
  /** Every (re)connect, after the handshake was accepted. */
  onConnect(): void
  onDisconnect(reason: string): void
  /** Network trouble, or a server that could not take the handshake now; the link retries by itself. */
  onTrouble(message: string): void
  /** The server refused the token. Retrying cannot help: it is wrong or revoked. */
  onRejected(message: string): void
  /** The link stopped retrying after the socket's `reconnectionAttempts` failures. Never, when those are unlimited. */
  onGiveUp?(): void
}

/** The daemon's connection: the rpc client plus a frame sink tied to the current connection. */
export class Link {
  readonly rpc: RpcClient
  private epoch = 0
  private attempt = 0
  private retryTimer: ReturnType<typeof setTimeout> | undefined
  private closed = false

  constructor(
    readonly socket: Socket,
    private readonly events: LinkEvents,
  ) {
    this.rpc = createRpcClient(socket)
    socket.on("connect", () => {
      this.attempt = 0
      this.epoch++
      events.onConnect()
    })
    socket.on("disconnect", (reason) => {
      this.epoch++
      events.onDisconnect(reason)
      if (reason === "io server disconnect") this.retry()
    })
    socket.on("connect_error", (err) => {
      // An inactive socket is one whose handshake the server refused: socket.io does not retry those.
      if (!socket.active && err.message === REFUSED) {
        events.onRejected(err.message)
        return
      }
      events.onTrouble(err.message)
      if (!socket.active) this.retry()
    })
    socket.io.on("reconnect_failed", () => events.onGiveUp?.())
  }

  /** Retries, with the backoff socket.io uses, what socket.io does not: a socket the server ended or refused. */
  private retry(): void {
    if (this.closed) return
    if (this.attempt >= this.socket.io.reconnectionAttempts()) {
      this.events.onGiveUp?.()
      return
    }
    this.retryTimer = setTimeout(() => this.socket.connect(), backoffDelay(this.attempt++, RECONNECT))
  }

  /**
   * A sink for the connection that is current now. Frames go out only while that same connection is up: after a
   * drop the server has forgotten the sessions, so a frame sent after the reconnect would be stale.
   */
  sink(): FrameSink {
    const epoch = this.epoch
    return {
      send: (sessionId, kind, data, code) => {
        if (this.epoch === epoch && this.socket.connected) this.rpc.server.frame(sessionId, kind, data, code)
      },
    }
  }

  /** Closes the socket; resolves once the connection is closed (what was queued before has gone out), or after `graceMs`. */
  close(graceMs = 1000): Promise<void> {
    this.closed = true
    clearTimeout(this.retryTimer)
    this.rpc.dispose()
    return new Promise((resolve) => {
      if (!this.socket.connected) {
        this.socket.disconnect()
        resolve()
        return
      }
      const timer = setTimeout(resolve, graceMs)
      this.socket.io.once("close", () => {
        clearTimeout(timer)
        resolve()
      })
      this.socket.disconnect()
    })
  }
}
