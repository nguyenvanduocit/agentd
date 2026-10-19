import { describe, expect, test } from "bun:test"
import { readCapped } from "../../src/http.ts"

/** A response of `chunks` chunks of `size` bytes; `pulled.n` counts the chunks the reader asked for. */
function response(chunks: number, size: number, pulled = { n: 0 }, headers: Record<string, string> = {}): Response {
  const body = new ReadableStream<Uint8Array>({
    pull(c) {
      if (pulled.n >= chunks) {
        c.close()
        return
      }
      pulled.n++
      c.enqueue(new Uint8Array(size).fill(pulled.n))
    },
  })
  return new Response(body, { headers })
}

describe("readCapped", () => {
  test("reads a body of up to the limit, whole", async () => {
    const body = await readCapped(response(3, 10), 30)
    expect(body.length).toBe(30)
    expect([body[0], body[10], body[29]]).toEqual([1, 2, 3])
  })

  test("stops reading as soon as the body passes the limit", async () => {
    const pulled = { n: 0 }
    await expect(readCapped(response(1_000_000, 1024, pulled), 4096)).rejects.toThrow("larger than 4096 bytes")
    expect(pulled.n).toBeLessThan(10)
  })

  test("a Content-Length over the limit is refused before a byte is read", async () => {
    const pulled = { n: 0 }
    await expect(readCapped(response(1, 10, pulled, { "content-length": "999999" }), 4096)).rejects.toThrow("larger than 4096 bytes")
    expect(pulled.n).toBe(0)
  })
})
