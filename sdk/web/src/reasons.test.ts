import { describe, expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import { connectBrowser, connectServer, detect, disconnect, openSession, reasonText, SessionError, type FetchLike, type Reason } from "./client.ts"

// contract/testdata/page-reasons.json is the one list of why the local gateway refuses a page, and of the words for it.
// The gateway and both Go and TypeScript SDKs test against it, so the words cannot drift apart.
interface Golden {
  reasons: { reason: string; legacy?: string; text: string }[]
  clientReasons: { reason: string; text: string }[]
  fallback: string
}
const golden = JSON.parse(readFileSync(new URL("../../../contract/testdata/page-reasons.json", import.meta.url), "utf8")) as Golden

const fetchWith = (handler: (url: string, init?: RequestInit) => Response | Promise<Response>): FetchLike => (url, init) => Promise.resolve(handler(url, init))
const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", ...headers } })

describe("the reasons and their words are the golden file's", () => {
  test("every reason of the file has exactly its sentence", () => {
    for (const e of [...golden.reasons, ...golden.clientReasons]) expect(reasonText(e.reason)).toBe(e.text)
  })

  test("a reason the SDK does not know gets the fallback sentence, naming it", () => {
    expect(reasonText("from-the-future")).toBe(golden.fallback.replace("{reason}", "from-the-future"))
  })

  test("a specific reason comes with the legacy one every older SDK knows, and that one has words too", () => {
    const names = new Set(golden.reasons.map((e) => e.reason))
    for (const e of golden.reasons) {
      if (e.legacy === undefined) continue
      expect(names.has(e.legacy)).toBe(true)
      expect(golden.reasons.find((l) => l.reason === e.legacy)?.legacy).toBeUndefined()
    }
  })
})

describe("the daemon's reason and its more specific code", () => {
  const refusal = (body: object, headers: Record<string, string> = {}) => fetchWith(() => json(429, { granted: false, ...body }, headers))
  const d = { port: 47615 }

  test("the code is the reason when the SDK knows it, and the legacy reason stays for a code it does not", async () => {
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "cooldown", code: "locked", message: "m" }) })).toEqual({ granted: false, reason: "locked", message: "m" })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "no-dialog", code: "no-folder" }) })).toEqual({ granted: false, reason: "no-folder" })
    expect(await connectServer(d, { name: "Test app", code: "ABCD-1234" }, { fetch: refusal({ reason: "pairing", code: "not-saved" }) })).toEqual({ granted: false, reason: "not-saved" })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "cooldown", code: "a-code-from-the-future" }) })).toEqual({ granted: false, reason: "cooldown" })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "denied" }) })).toEqual({ granted: false, reason: "denied" })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({}) })).toEqual({ granted: false, reason: "malformed" })
  })

  test("retryAfter is the daemon's seconds, from the body or else the Retry-After header", async () => {
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "cooldown", retryAfter: 30 }, { "retry-after": "99" }) })).toEqual({ granted: false, reason: "cooldown", retryAfter: 30 })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "cooldown", code: "locked" }, { "retry-after": "600" }) })).toEqual({ granted: false, reason: "locked", retryAfter: 600 })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: refusal({ reason: "cooldown", retryAfter: "soon" }) })).toEqual({ granted: false, reason: "cooldown" })
  })

  test("a failed disconnect says why in the same words", async () => {
    const f = fetchWith(() => json(403, { granted: false, reason: "peer", message: "another user" }))
    expect(await disconnect(d, { fetch: f })).toEqual({ disconnected: false, reason: "peer", message: "another user" })
  })
})

describe("a request that is stopped", () => {
  const d = { port: 47615 }

  test("is cancelled when its AbortSignal aborts, and unreachable for any other failure", async () => {
    const stopped = new AbortController()
    const f = fetchWith((_url, init) => {
      if (init?.signal?.aborted) throw Object.assign(new Error("The operation was aborted."), { name: "AbortError" })
      throw new TypeError("Failed to fetch")
    })
    stopped.abort()
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: f, signal: stopped.signal })).toMatchObject({ granted: false, reason: "cancelled" })
    expect(await connectBrowser(d, { name: "Test app" }, { fetch: f })).toMatchObject({ granted: false, reason: "unreachable", message: "Failed to fetch" })
  })

  test("hands its signal to the fetch", async () => {
    let seen: AbortSignal | null | undefined
    const stopped = new AbortController()
    await connectBrowser(d, { name: "Test app" }, { fetch: fetchWith((_url, init) => ((seen = init?.signal), json(200, { granted: true }))), signal: stopped.signal })
    expect(seen).toBe(stopped.signal)
  })
})

describe("detect shows a refusal the gateway gave", () => {
  const status = { name: "agentd", version: "1.2.3", granted: { server: false, browser: false } }

  test("a gateway that refuses this page is not 'not installed': found is false, and why is said", async () => {
    const f = fetchWith(() => json(403, { granted: false, reason: "origin", message: "no Origin header" }))
    expect(await detect({ fetch: f, ports: [47615] })).toEqual({ found: false, refused: "origin", message: "no Origin header" })
  })

  test("a daemon that answers wins over one that refuses, and a refusal wins over silence", async () => {
    const mixed = fetchWith((url) => {
      if (url.includes(":47615/")) return json(403, { granted: false, reason: "host" })
      if (url.includes(":47616/")) return json(200, status)
      throw new TypeError("Failed to fetch")
    })
    expect(await detect({ fetch: mixed, ports: [47615, 47616, 47617] })).toMatchObject({ found: true, port: 47616 })
    const refusedAndSilent = fetchWith((url) => {
      if (url.includes(":47616/")) return json(403, { granted: false, reason: "peer" })
      throw new TypeError("Failed to fetch")
    })
    expect(await detect({ fetch: refusedAndSilent, ports: [47615, 47616] })).toEqual({ found: false, refused: "peer" })
  })

  test("a program that is not agentd and answers 403 is not taken for a refusal of it", async () => {
    expect(await detect({ fetch: fetchWith(() => json(403, { reason: "origin" })), ports: [47615] })).toEqual({ found: false })
    expect(await detect({ fetch: fetchWith(() => new Response("forbidden", { status: 403 })), ports: [47615] })).toEqual({ found: false })
    expect(await detect({ fetch: fetchWith(() => json(403, { granted: false, reason: 7 })), ports: [47615] })).toEqual({ found: false })
  })

  test("the more specific code of a refusal wins here too, when the SDK knows it", async () => {
    const f = fetchWith(() => json(403, { granted: false, reason: "origin", code: "from-the-future" }))
    expect(await detect({ fetch: f, ports: [47615] })).toEqual({ found: false, refused: "origin" })
  })
})

describe("openSession rejects with a SessionError that carries a reason", () => {
  class Sock {
    static last: Sock
    onmessage: ((e: { data: string }) => void) | null = null
    onclose: (() => void) | null = null
    onerror: (() => void) | null = null
    constructor(readonly url: string) {
      Sock.last = this
    }
    send(): void {}
    close(): void {
      this.onclose?.()
    }
  }
  const WS = Sock as unknown as typeof WebSocket
  const tick = () => new Promise((r) => setTimeout(r, 0))

  test("a socket that cannot be opened is unreachable, and says that the page may not be allowed", async () => {
    const pending = openSession({ port: 47615 }, "claude", { WebSocket: WS })
    await tick()
    Sock.last.onerror?.()
    const error = (await pending.catch((e: unknown) => e)) as SessionError
    expect(error).toBeInstanceOf(SessionError)
    expect(error).toBeInstanceOf(Error)
    expect(error.reason).toBe("unreachable" satisfies Reason)
    expect(error.message).toContain("is this page allowed")
  })

  test("the daemon's own refusal of the open keeps the daemon's words", async () => {
    const pending = openSession({ port: 47615 }, "nosuch", { WebSocket: WS })
    await tick()
    Sock.last.onmessage?.({ data: JSON.stringify({ t: "hello", version: "1" }) })
    await tick()
    // the id the SDK chose is in the open message it sent: this fake does not record it, so echo through the wire shape
    const sent: { id?: string }[] = []
    Sock.last.send = ((text: string) => void sent.push(JSON.parse(text) as { id?: string })) as never
    Sock.last.onmessage?.({ data: JSON.stringify({ t: "hello", version: "1" }) })
    await tick()
    Sock.last.onmessage?.({ data: JSON.stringify({ t: "error", id: sent[0]?.id, message: 'unknown agent "nosuch"' }) })
    const error = (await pending.catch((e: unknown) => e)) as SessionError
    expect(error).toBeInstanceOf(SessionError)
    expect(error.reason).toBe("error")
    expect(error.message).toBe('unknown agent "nosuch"')
  })

  test("a socket the daemon closed is unreachable", async () => {
    const pending = openSession({ port: 47615 }, "claude", { WebSocket: WS })
    await tick()
    Sock.last.onclose?.()
    const error = (await pending.catch((e: unknown) => e)) as SessionError
    expect(error).toBeInstanceOf(SessionError)
    expect(error.reason).toBe("unreachable")
  })
})
