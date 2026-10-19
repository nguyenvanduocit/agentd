import type { ChildProcess } from "node:child_process"
import { once } from "node:events"
import type { Readable } from "node:stream"
import type { FrameKind } from "../../contract/define.acp.ts"
import { LineSplitter, MAX_LINE_BYTES } from "./core/lines.ts"
import { signalGroup } from "./procgroup.ts"

const TERM_GRACE_MS = 2000
const PIPE_GRACE_MS = 3000

/** Where an agent's frames go: one ordered stream per connection. A sink that has been replaced drops frames. */
export interface FrameSink {
  send(sessionId: string, kind: FrameKind, data: string, code: number): void
  /**
   * A sink that can fall behind its reader has this: undefined while it has room, else a promise that settles once it
   * has room again. The agent's output is not read meanwhile, so the agent waits on its full pipe. A sink without it
   * takes every frame (the server bounds what it holds itself).
   */
  backlog?(): Promise<void> | undefined
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
    const { stdout, stderr } = this.child
    const splitter = new LineSplitter()
    stdout?.on("data", (chunk: Buffer) => {
      for (const line of splitter.push(chunk)) this.sink.send(this.id, "out", line, 0)
      if (splitter.tooLong) this.lineTooLong(stdout)
      else this.waitForRoom(stdout)
    })
    stderr?.setEncoding("utf8")
    stderr?.on("data", (text: string) => {
      this.sink.send(this.id, "err", text, 0)
      this.waitForRoom(stderr)
    })
  }

  /** Stops reading `stream` while the sink is full. */
  private waitForRoom(stream: Readable): void {
    const room = this.sink.backlog?.()
    if (room === undefined) return
    stream.pause()
    void room.then(() => stream.resume())
  }

  /** The agent wrote a line no reader would take: the session ends, and its stderr says why. */
  private lineTooLong(stdout: Readable): void {
    this.sink.send(this.id, "err", `agentd: the agent wrote a line longer than ${MAX_LINE_BYTES / (1024 * 1024)} MiB; the session is ended\n`, 0)
    stdout.destroy()
    this.end()
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
