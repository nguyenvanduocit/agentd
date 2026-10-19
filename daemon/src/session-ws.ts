import type { IncomingMessage } from "node:http"
import type { Duplex } from "node:stream"
import { WebSocket, WebSocketServer } from "ws"
import type { Hello } from "../../contract/define.acp.ts"
import { parseToDaemon, type FromDaemon } from "./core/localproto.ts"
import { Fleet, type FleetOptions } from "./fleet.ts"
import { say } from "./log.ts"
import type { FrameSink } from "./session.ts"

/** Sessions one page may keep: a page chooses what runs on the machine, so the number is bounded. */
export const MAX_SESSIONS_PER_SOCKET = 5

export interface BrowserSessionOptions {
  fleet: Omit<FleetOptions, "sink">
  hello: () => Hello
}

/** The sockets of pages that were allowed to run agents; each socket has its own fleet, ended when it closes. */
export class BrowserSessions {
  private readonly open = new Set<{ ws: WebSocket; fleet: Fleet; origin: string }>()
  private readonly wss = new WebSocketServer({ noServer: true })

  constructor(private readonly o: BrowserSessionOptions) {}

  /** Agents running for pages. */
  get size(): number {
    let total = 0
    for (const s of this.open) total += s.fleet.size
    return total
  }

  /** Completes the WebSocket handshake of a request the gateway already checked. */
  upgrade(origin: string, req: IncomingMessage, socket: Duplex, head: Buffer): void {
    this.wss.handleUpgrade(req, socket, head, (ws) => {
      say(`${origin} opened a browser session`)
      this.serve(ws, origin)
    })
  }

  /** Closes every socket of `origin`, which ends the agents on them: the page was disconnected. */
  endOrigin(origin: string): void {
    for (const s of this.open) if (s.origin === origin) s.ws.terminate()
  }

  /** Closes every socket (an upgraded socket would keep the gateway's server open) and ends the agents. */
  async shutdown(graceMs: number): Promise<void> {
    const all = [...this.open]
    for (const s of all) s.ws.terminate()
    await Promise.all(all.map((s) => s.fleet.shutdown(graceMs)))
    this.wss.close()
  }

  private serve(ws: WebSocket, origin: string): void {
    const send = (m: FromDaemon): void => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m))
    }
    const sink: FrameSink = { send: (id, kind, data, code) => send({ t: "frame", id, kind, data, code }) }
    const fleet = new Fleet({ ...this.o.fleet, sink: () => sink })
    const entry = { ws, fleet, origin }
    this.open.add(entry)
    const end = (): void => {
      fleet.endAll()
      this.open.delete(entry)
    }
    ws.on("close", end)
    ws.on("error", end)
    ws.on("message", (data: Buffer) => this.handle(fleet, send, data.toString()))
    const hello = this.o.hello()
    send({ t: "hello", version: hello.version, agents: hello.agents })
  }

  private handle(fleet: Fleet, send: (m: FromDaemon) => void, text: string): void {
    const m = parseToDaemon(text)
    if (m === undefined) {
      send({ t: "error", id: "", message: "expected a JSON message: open, write or close" })
      return
    }
    if (m.t === "close") fleet.close(m.id)
    else if (m.t === "write") void fleet.write(m.id, m.line).then((ok) => ok || send({ t: "error", id: m.id, message: `session ${m.id} is not running` }))
    else if (fleet.size >= MAX_SESSIONS_PER_SOCKET) send({ t: "error", id: m.id, message: `a page may keep at most ${MAX_SESSIONS_PER_SOCKET} sessions` })
    else fleet.open(m.id, m.agent).then((o) => send({ t: "opened", id: m.id, pid: o.pid, cwd: o.cwd }), (e: unknown) => send({ t: "error", id: m.id, message: e instanceof Error ? e.message : String(e) }))
  }
}
