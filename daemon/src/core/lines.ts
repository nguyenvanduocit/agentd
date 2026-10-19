const NL = 0x0a
const CR = 0x0d

/**
 * Cuts a byte stream into lines. A line may arrive in any number of chunks and a chunk may hold any number of
 * lines. Bytes are joined before they are decoded, so a multi-byte character that straddles two chunks stays whole.
 * A trailing "\r" is dropped, an unterminated tail is held back (it is never emitted).
 */
export class LineSplitter {
  private pending: Buffer[] = []

  push(chunk: Buffer): string[] {
    const lines: string[] = []
    let rest = chunk
    for (;;) {
      const nl = rest.indexOf(NL)
      if (nl < 0) {
        if (rest.length > 0) this.pending.push(rest)
        return lines
      }
      this.pending.push(rest.subarray(0, nl))
      lines.push(this.take())
      rest = rest.subarray(nl + 1)
    }
  }

  private take(): string {
    const joined = Buffer.concat(this.pending)
    this.pending = []
    const end = joined.length > 0 && joined[joined.length - 1] === CR ? joined.length - 1 : joined.length
    return joined.toString("utf8", 0, end)
  }
}
