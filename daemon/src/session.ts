import type { ChildProcess } from "node:child_process"
import { once } from "node:events"
import type { FrameKind } from "../../contract/define.acp.ts"
import { LineSplitter } from "./core/lines.ts"
import { signalGroup } from "./procgroup.ts"

const TERM_GRACE_MS = 2000
const PIPE_GRACE_MS = 3000

/** Where an agent's frames go: one ordered stream per connection. A sink that has been replaced drops frames. */
export interface FrameSink {
  send(sessionId: string, kind: FrameKind, data: string, code: number): void
}

/** The pid once `child` has started; rejects when the command could not be run (events.once rejects on "error"). */
function spawned(child: ChildProcess): Promise<number> {
  return once(child, "spawn").then(() => child.pid ?? -1)
}

/** One agent process: its stdout becomes `out` frames, its stderr `err` frames, its end one `exit` frame. */
export class AgentSession {
  readonly pgid: number
  private spawned = false
  private killTimer: NodeJS.Timeout | undefined
  private pipeTimer: NodeJS.Timeout | undefined

  constructor(
    private readonly id: string,
    private readonly child: ChildProcess,
    private readonly sink: FrameSink,
    onEnd: () => void,
  ) {
    this.pgid = child.pid ?? -1
    child.on("error", () => undefined) // reported through started(), or by the exit frame
    child.once("spawn", () => {
      this.spawned = true
    })
    this.pipeOutput()
    child.stdin?.on("error", () => undefined) // EPIPE after the agent died: the exit frame says so
    // The leader is gone: end whatever it left in the group. A process that left the group may still hold a pipe,
    // so the pipes get a grace period before they are cut and `close` can fire.
    child.once("exit", () => {
      signalGroup(this.pgid, true)
      this.pipeTimer = setTimeout(() => {
        child.stdout?.destroy()
        child.stderr?.destroy()
      }, PIPE_GRACE_MS)
    })
    child.once("close", (code, signal) => {
      clearTimeout(this.killTimer)
      clearTimeout(this.pipeTimer)
      onEnd()
      if (this.spawned) sink.send(id, "exit", "", code ?? (signal ? 128 : -1))
    })
  }

  /** Settles when the process has started (the pid) or could not (the error). Call it right after construction. */
  started(): Promise<number> {
    return spawned(this.child)
  }

  private pipeOutput(): void {
    const splitter = new LineSplitter()
    this.child.stdout?.on("data", (chunk: Buffer) => {
      for (const line of splitter.push(chunk)) this.sink.send(this.id, "out", line, 0)
    })
    this.child.stderr?.setEncoding("utf8")
    this.child.stderr?.on("data", (text: string) => this.sink.send(this.id, "err", text, 0))
  }

  /** Writes one line to the agent's stdin; false when the agent can no longer read it. */
  write(line: string): Promise<boolean> {
    const stdin = this.child.stdin
    if (!stdin?.writable) return Promise.resolve(false)
    return new Promise((resolve) => stdin.write(`${line}\n`, (err) => resolve(!err)))
  }

  /** SIGTERM the group, SIGKILL it after a grace period: children may outlive a leader that ignores SIGTERM. */
  end(): void {
    signalGroup(this.pgid, false)
    this.killTimer ??= setTimeout(() => signalGroup(this.pgid, true), TERM_GRACE_MS)
  }
}
