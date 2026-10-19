import { spawn, type ChildProcessByStdio } from "node:child_process"
import type { Readable, Writable } from "node:stream"
import { LineSplitter } from "./core/lines.ts"
import { parseTrayLine, type TrayAction, type TrayState } from "./core/tray.ts"
import { say, warn } from "./log.ts"

/** A tray that ends sooner than this after it started did not get to run (no display, no notification area). */
const QUICK_MS = 10_000
const MAX_QUICK_EXITS = 3
const RESTART_DELAY_MS = 2000
const END_GRACE_MS = 1000

type TrayProcess = ChildProcessByStdio<Writable, Readable, Readable>

/**
 * The daemon's icon in the menu bar or notification area: a program of its own (tray/), started with the daemon. It reads
 * state lines on stdin and prints the entry the user chose on stdout; it ends when its stdin does, so a daemon that is
 * killed takes it along. One that keeps ending at once is given up: the daemon runs without an icon.
 */
export class Tray {
  private child: TrayProcess | undefined
  private last = ""
  private quickExits = 0
  private stopped = false
  private timer: NodeJS.Timeout | undefined

  constructor(
    private readonly file: string,
    private readonly onAction: (action: TrayAction) => void,
  ) {}

  start(): void {
    const began = Date.now()
    const child = spawn(this.file, [], { stdio: ["pipe", "pipe", "pipe"], windowsHide: true })
    this.child = child
    child.stdin.on("error", () => undefined) // EPIPE: it ended; the exit handler says so
    child.on("error", (e) => warn(`tray: cannot start ${this.file}: ${e.message}`))
    const lines = new LineSplitter()
    child.stdout.on("data", (chunk: Buffer) => {
      for (const line of lines.push(chunk)) {
        const action = parseTrayLine(line)
        if (action !== undefined) this.onAction(action)
      }
    })
    child.stderr.setEncoding("utf8")
    child.stderr.on("data", (text: string) => say(`tray: ${text.trim()}`))
    child.once("close", () => this.ended(child, began))
    this.send()
  }

  /** The state to show now; sent again to a tray that was started anew. Nothing is sent when it did not change. */
  update(state: TrayState): void {
    const text = JSON.stringify(state)
    if (text === this.last) return
    this.last = text
    this.send()
  }

  /** Ends the tray: its stdin closes, and it is killed if it does not follow. */
  stop(): void {
    this.stopped = true
    clearTimeout(this.timer)
    const child = this.child
    if (child === undefined) return
    child.stdin.end()
    setTimeout(() => child.kill("SIGTERM"), END_GRACE_MS).unref()
  }

  private send(): void {
    const stdin = this.child?.stdin
    if (this.last !== "" && stdin?.writable === true) stdin.write(`${this.last}\n`)
  }

  private ended(child: TrayProcess, began: number): void {
    if (this.stopped || this.child !== child) return
    this.quickExits = Date.now() - began < QUICK_MS ? this.quickExits + 1 : 0
    if (this.quickExits >= MAX_QUICK_EXITS) {
      warn(`tray: giving up after ${this.quickExits} quick exits (no display or notification area?); the daemon runs without an icon`)
      return
    }
    this.timer = setTimeout(() => this.start(), RESTART_DELAY_MS)
  }
}
