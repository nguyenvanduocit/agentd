import { Socket } from "node:net"
import { LineSplitter } from "./core/lines.ts"
import { parseTrayLine, type TrayAction, type TrayState } from "./core/tray.ts"
import { warn } from "./log.ts"

/**
 * The app that started the daemon (agentd.app), on the descriptor it handed over: the tray's protocol in both directions,
 * JSON lines, states from the daemon and the user's choices from the app. The app owns the daemon: when its end closes
 * (the app quit, or died), `onGone` ends the daemon.
 */
export class AppLink {
  private socket: Socket | undefined
  private last = ""
  private stopped = false

  constructor(
    private readonly fd: number,
    private readonly onAction: (action: TrayAction) => void,
    private readonly onGone: () => void,
  ) {}

  start(): void {
    const socket = new Socket({ fd: this.fd, readable: true, writable: true })
    this.socket = socket
    const lines = new LineSplitter()
    socket.on("data", (chunk: Buffer) => {
      for (const line of lines.push(chunk)) {
        const action = parseTrayLine(line)
        if (action !== undefined) this.onAction(action)
      }
    })
    socket.on("error", (e) => warn(`app: ${e.message}`))
    socket.once("close", () => {
      if (!this.stopped) this.onGone()
    })
    this.send()
  }

  /** The state to show now; nothing is sent when it did not change. */
  update(state: TrayState): void {
    const text = JSON.stringify(state)
    if (text === this.last) return
    this.last = text
    this.send()
  }

  stop(): void {
    this.stopped = true
    this.socket?.end()
  }

  private send(): void {
    if (this.last !== "" && this.socket?.writable === true) this.socket.write(`${this.last}\n`)
  }
}
