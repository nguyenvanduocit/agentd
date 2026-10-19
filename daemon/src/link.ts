import { io, type Socket } from "socket.io-client"
import { createRpcClient, type RpcClient } from "../../contract/client.generated.ts"
import { backoffDelay, type BackoffOptions } from "./core/backoff.ts"
import { namespaceUrl } from "./core/server.ts"
import type { FrameSink } from "./session.ts"

const RECONNECT: BackoffOptions = { baseMs: 500, maxMs: 15_000, jitter: 0.5 }

export interface LinkOptions {
  /** The server origin (http or https). */
  server: string
  auth: Record<string, string>
  /** Give up after this many failed reconnects; the daemon retries for as long as it runs. */
  reconnectionAttempts?: number
}

/**
 * Opens the socket to the /agentd namespace. The server ending the socket ("io server disconnect") is not retried by
 * socket.io itself, so it is retried here with the same backoff.
 */
export function openSocket(o: LinkOptions): Socket {
  const socket = io(namespaceUrl(o.server), {
    transports: ["websocket"],
    auth: o.auth,
    reconnection: true,
    reconnectionDelay: RECONNECT.baseMs,
    reconnectionDelayMax: RECONNECT.maxMs,
    randomizationFactor: RECONNECT.jitter,
    reconnectionAttempts: o.reconnectionAttempts ?? Infinity,
  })
  let attempt = 0
  socket.on("connect", () => {
    attempt = 0
  })
  socket.on("disconnect", (reason) => {
    if (reason !== "io server disconnect") return
    setTimeout(() => socket.connect(), backoffDelay(attempt++, RECONNECT))
  })
  return socket
}

/** What the daemon's side of one connection reports. */
export interface LinkEvents {
  /** Every (re)connect, after the handshake was accepted. */
  onConnect(): void
  onDisconnect(reason: string): void
  /** Network trouble; the link retries by itself. */
  onTrouble(message: string): void
  /** The server refused the handshake. Retrying cannot help: the token is wrong or revoked. */
  onRejected(message: string): void
}

/** The daemon's connection: the rpc client plus a frame sink tied to the current connection. */
export class Link {
  readonly rpc: RpcClient
  private epoch = 0

  constructor(
    readonly socket: Socket,
    events: LinkEvents,
  ) {
    this.rpc = createRpcClient(socket)
    socket.on("connect", () => {
      this.epoch++
      events.onConnect()
    })
    socket.on("disconnect", (reason) => {
      this.epoch++
      events.onDisconnect(reason)
    })
    socket.on("connect_error", (err) => {
      if (socket.active) events.onTrouble(err.message)
      else events.onRejected(err.message)
    })
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
