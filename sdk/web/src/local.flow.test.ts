import { describe, expect, test } from "bun:test"
import { SessionError, reasonText, type ConnectResult, type Detected, type FetchLike } from "./client.ts"
import { createLocalDaemon, type LocalClient, type LocalState } from "./local.ts"

const NOT_FOUND: Detected = { found: false }
const FOUND: Detected = { found: true, port: 47615, version: "1.0.0", granted: { server: false, browser: false }, agents: undefined }
const BROWSER: Detected = { ...FOUND, granted: { server: false, browser: true } }

const flush = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0))
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })

/** A browser-mode daemon over a scripted client; `timer.tick()` is the next tick of watch(). */
function browser(o: { detected?: (Detected | Error)[]; connect?: ConnectResult | Error; disconnect?: unknown } = {}) {
  const detected = [...(o.detected ?? [FOUND])]
  const timer = { tick: (): void => undefined, stopped: false }
  const client: LocalClient = {
    detect: () => {
      const next = detected.length > 1 ? detected.shift() : detected[0]
      return next instanceof Error ? Promise.reject(next) : Promise.resolve(next ?? NOT_FOUND)
    },
    connectServer: () => Promise.reject(new Error("unused")),
    connectBrowser: () => (o.connect instanceof Error ? Promise.reject(o.connect) : Promise.resolve(o.connect ?? { granted: true })),
    disconnect: () => Promise.resolve(o.disconnect as undefined),
  }
  const local = createLocalDaemon({
    mode: "browser",
    client,
    interval: (tick) => {
      timer.tick = tick
      return () => void (timer.stopped = true)
    },
  })
  const phases: string[] = []
  local.subscribe((s) => phases.push(s.phase))
  return { local, phases, timer }
}

describe("connecting", () => {
  test("goes from connecting to connected without showing 'available' in between", async () => {
    const t = browser({ detected: [FOUND, BROWSER] })
    await t.local.detect()
    t.phases.length = 0
    expect(await t.local.connect()).toBe(true)
    expect(t.phases).toEqual(["connecting", "connected"])
  })

  test("a client that throws is unreachable, with the sentence for it and the exception kept as the message", async () => {
    const t = browser({ connect: new TypeError("Failed to fetch") })
    await t.local.detect()
    expect(await t.local.connect()).toBe(false)
    expect(t.local.state).toMatchObject({ phase: "refused", reason: "unreachable", message: "Failed to fetch", error: reasonText("unreachable") })
  })

  test("a look that throws is a daemon that is not there", async () => {
    const t = browser({ detected: [new TypeError("Failed to fetch")] })
    await t.local.detect()
    expect(t.local.state).toMatchObject({ phase: "absent", error: "" })
  })

  test("a disconnect that throws is a refusal of its own, unreachable", async () => {
    const throwing: LocalClient = {
      detect: () => Promise.resolve(BROWSER),
      connectServer: () => Promise.reject(new Error("unused")),
      connectBrowser: () => Promise.resolve({ granted: true }),
      disconnect: () => Promise.reject(new TypeError("Failed to fetch")),
    }
    const local = createLocalDaemon({ mode: "browser", client: throwing })
    await local.detect()
    await local.disconnect()
    expect(local.state).toMatchObject({ phase: "refused", reason: "unreachable", message: "Failed to fetch" })
  })

  test("a refusal that is the app's own (the server gave no pairing code) is a generic error with its own words", async () => {
    const client: LocalClient = {
      detect: () => Promise.resolve(FOUND),
      connectServer: () => Promise.reject(new Error("unused")),
      connectBrowser: () => Promise.reject(new Error("unused")),
      disconnect: () => Promise.resolve(),
    }
    const local = createLocalDaemon({ client, startPairing: () => Promise.resolve(""), cancelPairing: () => Promise.resolve() })
    await local.detect()
    await local.connect()
    expect(local.state).toMatchObject({ phase: "refused", reason: "error", error: "The app could not start a connection. Try again." })
  })

  test("a refusal with a specific code reads as that reason, and carries the seconds to wait", async () => {
    const t = browser({ connect: { granted: false, reason: "locked", retryAfter: 600 } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state).toMatchObject({ phase: "refused", reason: "locked", error: reasonText("locked"), retryAfter: 600 })
  })
})

describe("watch keeps looking while connected", () => {
  test("a daemon that left is noticed on the second miss, not the first (one slow answer is not a departure)", async () => {
    const t = browser({ detected: [BROWSER, NOT_FOUND, NOT_FOUND, NOT_FOUND] })
    const stop = t.local.watch()
    await flush()
    expect(t.local.state.phase).toBe("connected")
    t.timer.tick()
    await flush()
    expect(t.local.state.phase).toBe("connected")
    t.timer.tick()
    await flush()
    expect(t.local.state).toMatchObject({ phase: "absent", error: "" })
    stop()
  })

  test("one miss between two answers changes nothing, and tells nobody", async () => {
    const t = browser({ detected: [BROWSER, NOT_FOUND, BROWSER] })
    const stop = t.local.watch()
    await flush()
    t.phases.length = 0
    t.timer.tick()
    await flush()
    t.timer.tick()
    await flush()
    expect(t.local.state.phase).toBe("connected")
    expect(t.phases).toEqual([])
    stop()
  })

  test("a grant that lapsed is noticed at once: the daemon is there and this page is not allowed any more", async () => {
    const t = browser({ detected: [BROWSER, FOUND] })
    const stop = t.local.watch()
    await flush()
    expect(t.local.state.phase).toBe("connected")
    t.timer.tick()
    await flush()
    expect(t.local.state).toMatchObject({ phase: "available", canConnect: true, error: "" })
    stop()
  })
})

describe("subscribe", () => {
  test("tells the listener nothing until a change, unless asked to start with the state now", async () => {
    const t = browser()
    const plain: string[] = []
    const eager: string[] = []
    t.local.subscribe((s) => plain.push(s.phase))
    t.local.subscribe((s) => eager.push(s.phase), { immediate: true })
    expect(plain).toEqual([])
    expect(eager).toEqual(["detecting"])
    await t.local.detect()
    expect(plain).toEqual(["available"])
    expect(eager).toEqual(["detecting", "available"])
  })
})

describe("a daemon that refuses this page is not 'not installed'", () => {
  test("shows the refusal as the reason of an absent daemon, and clears it when the next look finds nothing at all", async () => {
    const t = browser({ detected: [{ found: false, refused: "origin", message: "no Origin header" }, NOT_FOUND] })
    await t.local.detect()
    expect(t.local.state).toMatchObject({ phase: "absent", canConnect: false, reason: "origin", message: "no Origin header", error: reasonText("origin") })
    await t.local.detect()
    expect(t.local.state.phase).toBe("absent")
    expect(t.local.state.error).toBe("")
    expect("reason" in t.local.state).toBe(false)
  })
})

describe("createLocalDaemon talks to the daemon itself, so a test needs no hand-written client", () => {
  /** A fetch that answers as the gateway does for a page whose Origin it was given. */
  function gateway(seen: { url: string; method: string }[]): FetchLike {
    return async (url, init) => {
      seen.push({ url, method: init?.method ?? "GET" })
      if (url.endsWith("/v1/status")) return json(200, { name: "agentd", version: "9.9.9", protocol: 1, granted: { server: false, browser: true } })
      if (url.endsWith("/v1/connect")) return init?.method === "DELETE" ? json(200, { granted: false }) : json(200, { granted: true })
      return json(404, { granted: false, reason: "not-found" })
    }
  }

  test("takes the ports, the fetch and the timeout, and passes them to the calls it makes", async () => {
    const seen: { url: string; method: string }[] = []
    const local = createLocalDaemon({ name: "Test app", mode: "browser", ports: [47699], fetch: gateway(seen), timeoutMs: 500 })
    await local.detect()
    expect(local.state).toMatchObject({ phase: "connected", found: { found: true, port: 47699, version: "9.9.9" } })
    await local.disconnect()
    expect(seen.map((s) => `${s.method} ${s.url}`)).toEqual([
      "GET http://127.0.0.1:47699/v1/status",
      "DELETE http://127.0.0.1:47699/v1/connect",
      "GET http://127.0.0.1:47699/v1/status",
    ])
  })

  test("asks to connect through the same fetch", async () => {
    const seen: { url: string; method: string }[] = []
    const fetch: FetchLike = async (url, init) => {
      seen.push({ url, method: init?.method ?? "GET" })
      if (url.endsWith("/v1/status")) return json(200, { name: "agentd", version: "9.9.9", granted: { server: false, browser: false } })
      return json(200, { granted: true })
    }
    const local = createLocalDaemon({ name: "Test app", mode: "browser", ports: [47698], fetch })
    await local.detect()
    expect(local.state.phase).toBe("available")
    await local.connect()
    expect(seen.map((s) => `${s.method} ${s.url}`)).toContain("POST http://127.0.0.1:47698/v1/connect")
  })

  test("tells the daemon what the app calls itself, and refuses to start without a name", async () => {
    const bodies: unknown[] = []
    const fetch: FetchLike = async (url, init) => {
      if (url.endsWith("/v1/status")) return json(200, { name: "agentd", version: "9.9.9", protocol: 2, granted: { server: false, browser: false } })
      bodies.push(JSON.parse(String(init?.body)))
      return json(200, { granted: true })
    }
    const local = createLocalDaemon({ name: "AIOcean", mode: "browser", ports: [47695], fetch })
    await local.detect()
    await local.connect()
    expect(bodies).toEqual([{ mode: "browser", name: "AIOcean" }])
    expect(() => createLocalDaemon({ mode: "browser", ports: [47695], fetch })).toThrow("name is required")
    expect(() => createLocalDaemon({ name: "  ", mode: "browser", fetch })).toThrow("name is required")
  })
})

describe("openSession on the local daemon", () => {
  class Sock {
    static last: Sock
    onmessage: ((e: { data: string }) => void) | null = null
    onclose: (() => void) | null = null
    onerror: (() => void) | null = null
    constructor(readonly url: string) {
      Sock.last = this
    }
    send(): void {}
    close(): void {}
  }

  test("opens the agent on the port that was found, with the WebSocket it was given", async () => {
    const local = createLocalDaemon({
      name: "Test app",
      mode: "browser",
      ports: [47697],
      WebSocket: Sock as unknown as typeof WebSocket,
      fetch: async () => json(200, { name: "agentd", version: "1", granted: { server: false, browser: true } }),
    })
    await local.detect()
    const pending = local.openSession("claude")
    await flush()
    expect(Sock.last.url).toBe("ws://127.0.0.1:47697/v1/session")
    Sock.last.onclose?.()
    expect(await pending.catch((e: unknown) => e)).toBeInstanceOf(SessionError)
  })

  test("says so, with a reason, when no daemon has been found yet", async () => {
    const local = createLocalDaemon({ name: "Test app", mode: "browser", ports: [47696], fetch: async () => json(200, { name: "someone-else" }) })
    await local.detect()
    const error = (await local.openSession("claude").catch((e: unknown) => e)) as SessionError
    expect(error).toBeInstanceOf(SessionError)
    expect(error.reason).toBe("unreachable")
    expect(error.message).toContain("detect()")
  })
})

test("the state of a daemon is a new object after every change", async () => {
  const t = browser({ detected: [FOUND, BROWSER] })
  const seen: LocalState[] = []
  t.local.subscribe((s) => seen.push(s))
  await t.local.detect()
  await t.local.connect()
  expect(new Set(seen).size).toBe(seen.length)
})
