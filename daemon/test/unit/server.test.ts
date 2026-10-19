import { describe, expect, test } from "bun:test"
import { backoffDelay } from "../../src/core/backoff.ts"
import { namespaceUrl, pairUrl, serverOrigin } from "../../src/core/server.ts"

describe("serverOrigin", () => {
  test.each([
    ["https://mc.example.com", "https://mc.example.com"],
    ["https://mc.example.com/", "https://mc.example.com"],
    ["http://127.0.0.1:8080/app?x=1#y", "http://127.0.0.1:8080"],
    ["wss://mc.example.com/socket.io", "https://mc.example.com"],
    ["ws://localhost:3000", "http://localhost:3000"],
  ])("%s", (raw, want) => {
    expect(serverOrigin(raw)).toBe(want)
  })

  test("rejects what is not a server address", () => {
    expect(() => serverOrigin("mc.example.com")).toThrow("is not a URL")
    expect(() => serverOrigin("ftp://x")).toThrow("must start with https://")
  })

  test("plaintext is for loopback only, so the token never crosses a network unencrypted", () => {
    for (const raw of ["http://mc.example.com", "ws://10.0.0.5:8080", "http://127.0.0.1.evil.com", "http://localhost.evil.com"]) {
      expect(() => serverOrigin(raw)).toThrow("must use https://")
    }
    expect(serverOrigin("http://[::1]:9000/x")).toBe("http://[::1]:9000")
    expect(serverOrigin("http://127.1.2.3:80")).toBe("http://127.1.2.3")
  })

  test("fixed paths on the origin", () => {
    expect(namespaceUrl("https://s")).toBe("https://s/agentd")
    expect(pairUrl("https://s")).toBe("https://s/agentd/pair")
  })
})

describe("backoffDelay", () => {
  const o = { baseMs: 500, maxMs: 15_000, jitter: 0.5 }

  test("doubles up to the cap", () => {
    const d = [0, 1, 2, 3, 4, 5, 6, 40].map((n) => backoffDelay(n, { ...o, jitter: 0 }))
    expect(d).toEqual([500, 1000, 2000, 4000, 8000, 15_000, 15_000, 15_000])
  })

  test("jitter only shortens the delay, by at most its share", () => {
    expect(backoffDelay(2, o, () => 0)).toBe(2000)
    expect(backoffDelay(2, o, () => 1)).toBe(1000)
    expect(backoffDelay(2, o, () => 0.5)).toBe(1500)
  })
})
