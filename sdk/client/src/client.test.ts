import { describe, expect, test } from "bun:test"
import { connectBrowser, connectServer, detect, disconnect, openSession, reasonText, type Reason } from "./client.ts"

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>
const fetchWith = (handler: Handler): typeof fetch => ((input: RequestInfo | URL, init?: RequestInit) => Promise.resolve(handler(String(input), init))) as typeof fetch
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
const STATUS = { name: "agentd", version: "1.2.3", granted: { server: false, browser: false } }

describe("detect", () => {
  test("finds the daemon on the first port that answers like one", async () => {
    const seen: string[] = []
    const f = fetchWith((url) => {
      seen.push(url)
      if (url.startsWith("http://127.0.0.1:47616/")) return json(200, STATUS)
      throw new TypeError("Failed to fetch")
    })
    expect(await detect({ fetch: f })).toEqual({ found: true, port: 47616, version: "1.2.3", granted: { server: false, browser: false }, agents: undefined })
    expect(seen).toContain("http://127.0.0.1:47615/v1/status")
  })

  test("another program on a port is not the daemon", async () => {
    const f = fetchWith(() => json(200, { name: "something-else" }))
    expect(await detect({ fetch: f })).toEqual({ found: false })
  })

  test("a refusal, a bad body and a hang all mean not found", async () => {
    const f = fetchWith((url) => {
      if (url.includes(":47615/")) return json(403, { reason: "origin" })
      if (url.includes(":47616/")) return new Response("not json", { status: 200 })
      return new Promise<Response>(() => undefined)
    })
    const started = Date.now()
    expect(await detect({ fetch: f, timeoutMs: 100, ports: [47615, 47616, 47617] })).toEqual({ found: false })
    expect(Date.now() - started).toBeLessThan(1500)
  })
})

describe("connect", () => {
  const found = { port: 47615 }

  test("connectServer posts the code and no server, and reports Allow", async () => {
    let got: { url: string; init?: RequestInit } | undefined
    const f = fetchWith((url, init) => {
      got = { url, init }
      return json(200, { granted: true })
    })
    expect(await connectServer(found, { code: "ABCD" }, { fetch: f })).toEqual({ granted: true })
    expect(got?.url).toBe("http://127.0.0.1:47615/v1/connect")
    expect(got?.init?.method).toBe("POST")
    expect(JSON.parse(String(got?.init?.body))).toEqual({ mode: "server", code: "ABCD" })
  })

  test("connectBrowser posts mode browser", async () => {
    let body = ""
    const f = fetchWith((_u, init) => {
      body = String(init?.body)
      return json(200, { granted: true })
    })
    expect(await connectBrowser(found, { fetch: f })).toEqual({ granted: true })
    expect(JSON.parse(body)).toEqual({ mode: "browser" })
  })

  test.each<[number, string, Reason]>([
    [403, "denied", "denied"],
    [403, "timeout", "timeout"],
    [429, "cooldown", "cooldown"],
    [409, "busy", "busy"],
    [503, "no-dialog", "no-dialog"],
    [502, "pairing", "pairing"],
    [403, "origin", "origin"],
    [400, "malformed", "malformed"],
  ])("HTTP %i %s is the reason %s", async (status, reason, expected) => {
    const f = fetchWith(() => json(status, { granted: false, reason, message: "why" }))
    expect(await connectBrowser(found, { fetch: f })).toEqual({ granted: false, reason: expected, message: "why" })
  })

  test("a daemon that went away is unreachable", async () => {
    const f = fetchWith(() => {
      throw new TypeError("Failed to fetch")
    })
    expect(await connectBrowser(found, { fetch: f })).toMatchObject({ granted: false, reason: "unreachable" })
  })

  test("disconnect sends DELETE", async () => {
    let got = ""
    let method = ""
    const f = fetchWith((url, init) => {
      got = url
      method = init?.method ?? ""
      return json(200, { granted: false })
    })
    await disconnect(found, { fetch: f })
    expect(method).toBe("DELETE")
    expect(got).toBe("http://127.0.0.1:47615/v1/connect")
  })
})

describe("reasonText", () => {
  test("every reason has a sentence a person can act on", () => {
    const all: Reason[] = ["denied", "timeout", "cooldown", "busy", "no-dialog", "pairing", "origin", "malformed", "unreachable"]
    for (const r of all) expect(reasonText(r).length).toBeGreaterThan(20)
  })
})

class FakeSocket {
  static last: FakeSocket
  readonly sent: Record<string, unknown>[] = []
  onmessage: ((e: { data: string }) => void) | null = null
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) {
    FakeSocket.last = this
    queueMicrotask(() => this.onopen?.())
  }
  send(text: string): void {
    this.sent.push(JSON.parse(text) as Record<string, unknown>)
  }
  close(): void {
    this.onclose?.()
  }
  say(m: object): void {
    this.onmessage?.({ data: JSON.stringify(m) })
  }
}

describe("openSession", () => {
  test("opens one agent on its own socket and relays lines both ways", async () => {
    const pending = openSession({ port: 47615 }, "claude", { WebSocket: FakeSocket as unknown as typeof WebSocket })
    await new Promise((r) => setTimeout(r, 0))
    const sock = FakeSocket.last
    expect(sock.url).toBe("ws://127.0.0.1:47615/v1/session")
    sock.say({ t: "hello", version: "1", agents: [] })
    await new Promise((r) => setTimeout(r, 0))
    const open = sock.sent[0]
    expect(open).toMatchObject({ t: "open", agent: "claude" })
    sock.say({ t: "opened", id: open?.id, pid: 42, cwd: "/work" })
    const session = await pending
    expect(session).toMatchObject({ pid: 42, cwd: "/work" })

    const lines: string[] = []
    session.onFrame((f) => f.kind === "out" && lines.push(f.data))
    sock.say({ t: "frame", id: open?.id, kind: "out", data: "hello", code: 0 })
    sock.say({ t: "frame", id: "someone-else", kind: "out", data: "ignored", code: 0 })
    expect(lines).toEqual(["hello"])
    session.write("ping")
    expect(sock.sent[1]).toEqual({ t: "write", id: open?.id, line: "ping" })
    session.close()
    expect(sock.sent[2]).toEqual({ t: "close", id: open?.id })
  })

  test("an error for the open rejects with the daemon's message", async () => {
    const pending = openSession({ port: 47615 }, "rm -rf", { WebSocket: FakeSocket as unknown as typeof WebSocket })
    await new Promise((r) => setTimeout(r, 0))
    const sock = FakeSocket.last
    sock.say({ t: "hello", version: "1", agents: [] })
    await new Promise((r) => setTimeout(r, 0))
    sock.say({ t: "error", id: sock.sent[0]?.id, message: 'unknown agent "rm -rf"' })
    await expect(pending).rejects.toThrow('unknown agent "rm -rf"')
  })
})
