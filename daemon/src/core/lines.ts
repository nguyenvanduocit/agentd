const NL = 0x0a
const CR = 0x0d

/**
 * The longest line a splitter keeps, 32 MiB. A line goes on to the server as a JSON string inside one Socket.IO
 * message; escaping a JSON line at most doubles it (a backslash before each quote and backslash), so a line at the cap
 * still fits the server's 64 MiB message limit (hub.MaxMessageBytes). It is three times the 10 MiB line the Go ACP
 * SDK reads and hundreds of times a real one (a large tool result runs to tens of KB).
 */
export const MAX_LINE_BYTES = 32 * 1024 * 1024

/**
 * Cuts a byte stream into lines. A line may arrive in any number of chunks and a chunk may hold any number of
 * lines. Bytes are joined before they are decoded, so a multi-byte character that straddles two chunks stays whole.
 * A trailing "\r" is dropped, an unterminated tail is held back (it is never emitted).
 *
 * A line longer than `max` bytes ends the stream: the lines before it are emitted, then `tooLong` is set and the
 * splitter holds and emits nothing more. Cutting the line short would hand on a wrong message, and there is no telling
 * where the next one starts.
 */
export class LineSplitter {
  private pending: Buffer[] = []
  private pendingBytes = 0
  private over = false

  constructor(private readonly max = MAX_LINE_BYTES) {}

  /** A line passed the cap: the stream is over. */
  get tooLong(): boolean {
    return this.over
  }

  push(chunk: Buffer): string[] {
    const lines: string[] = []
    let rest = chunk
    while (!this.over) {
      const nl = rest.indexOf(NL)
      const part = nl < 0 ? rest : rest.subarray(0, nl)
      if (this.pendingBytes + part.length > this.max) this.overflow()
      else if (nl < 0) {
        this.hold(part)
        return lines
      } else {
        this.hold(part)
        lines.push(this.take())
        rest = rest.subarray(nl + 1)
      }
    }
    return lines
  }

  private hold(part: Buffer): void {
    if (part.length === 0) return
    this.pending.push(part)
    this.pendingBytes += part.length
  }

  private overflow(): void {
    this.over = true
    this.pending = []
    this.pendingBytes = 0
  }

  private take(): string {
    const joined = Buffer.concat(this.pending)
    this.pending = []
    this.pendingBytes = 0
    const end = joined.length > 0 && joined[joined.length - 1] === CR ? joined.length - 1 : joined.length
    return joined.toString("utf8", 0, end)
  }
}
