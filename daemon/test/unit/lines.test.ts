import { describe, expect, test } from "bun:test"
import { LineSplitter, MAX_LINE_BYTES } from "../../src/core/lines.ts"

const b = (s: string) => Buffer.from(s)

describe("LineSplitter", () => {
  test("one chunk, several lines", () => {
    expect(new LineSplitter().push(b("a\nbb\nccc\n"))).toEqual(["a", "bb", "ccc"])
  })

  test("a line cut in the middle is joined", () => {
    const s = new LineSplitter()
    expect(s.push(b('{"a":'))).toEqual([])
    expect(s.push(b("1}\n{"))).toEqual(['{"a":1}'])
    expect(s.push(b('"b":2}\n'))).toEqual(['{"b":2}'])
  })

  test("a chunk may end exactly on a newline, or start with one", () => {
    const s = new LineSplitter()
    expect(s.push(b("x\n"))).toEqual(["x"])
    expect(s.push(b("\ny"))).toEqual([""])
    expect(s.push(b("\n"))).toEqual(["y"])
  })

  test("a multi-byte character split across chunks stays whole", () => {
    const bytes = Buffer.from("xin chào \u{1F600}\n")
    for (let cut = 1; cut < bytes.length - 1; cut++) {
      const s = new LineSplitter()
      const lines = [...s.push(bytes.subarray(0, cut)), ...s.push(bytes.subarray(cut))]
      expect(lines).toEqual(["xin chào \u{1F600}"])
    }
  })

  test("a carriage return before the newline is dropped", () => {
    expect(new LineSplitter().push(b("a\r\nb\n"))).toEqual(["a", "b"])
  })

  test("an unterminated tail is held back, never emitted", () => {
    const s = new LineSplitter()
    expect(s.push(b("done\npartial"))).toEqual(["done"])
    expect(s.push(Buffer.alloc(0))).toEqual([])
  })

  test("a 12 MiB line in 64 KiB chunks comes out as one line", () => {
    const line = "x".repeat(12 * 1024 * 1024)
    const bytes = Buffer.from(`${line}\nnext\n`)
    const s = new LineSplitter()
    const out: string[] = []
    for (let i = 0; i < bytes.length; i += 65536) out.push(...s.push(bytes.subarray(i, i + 65536)))
    expect(out).toHaveLength(2)
    expect(out[0]?.length).toBe(line.length)
    expect(out[0] === line).toBe(true)
    expect(out[1]).toBe("next")
  })

  test("a line at the cap goes through", () => {
    const s = new LineSplitter(8)
    expect(s.push(b("1234"))).toEqual([])
    expect(s.push(b("5678\nnext\n"))).toEqual(["12345678", "next"])
    expect(s.tooLong).toBe(false)
  })

  test("a line past the cap ends the stream: the lines before it come out, nothing after, nothing is held", () => {
    const s = new LineSplitter(8)
    expect(s.push(b("ok\n123456789\nlater\n"))).toEqual(["ok"])
    expect(s.tooLong).toBe(true)
    expect(s.push(b("more\n"))).toEqual([])
    expect(s.tooLong).toBe(true)
  })

  test("an unterminated tail past the cap ends the stream before its newline arrives", () => {
    const s = new LineSplitter(8)
    expect(s.push(b("12345"))).toEqual([])
    expect(s.tooLong).toBe(false)
    expect(s.push(b("6789"))).toEqual([])
    expect(s.tooLong).toBe(true)
    expect(s.push(b("\n"))).toEqual([])
  })

  test("the default cap holds far more than a real ACP line", () => {
    expect(MAX_LINE_BYTES).toBeGreaterThanOrEqual(10 * 1024 * 1024)
    const s = new LineSplitter()
    const over = Buffer.alloc(MAX_LINE_BYTES + 1, 0x78)
    expect(s.push(over)).toEqual([])
    expect(s.tooLong).toBe(true)
  })
})
