import { describe, expect, test } from "bun:test"
import type { ConnectResult, Daemon, Detected, DisconnectResult, Reason } from "./client.ts"
import { createLocalDaemon, type LocalClient, type LocalDaemonDeps, type LocalState } from "./local.ts"

const NOT_FOUND: Detected = { found: false }
const FOUND: Detected = { found: true, port: 47615, version: "1.0.0", granted: { server: false, browser: false }, agents: undefined }
const CONNECTED: Detected = { ...FOUND, granted: { server: true, browser: false }, agents: [{ name: "claude", title: "Claude Code", ready: true, reason: "" }] }
const BROWSER: Detected = { ...FOUND, granted: { server: false, browser: true } }

const flush = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0))

function setup(o: { detected?: Detected[]; connect?: ConnectResult; code?: string; mode?: "server" | "browser"; disconnected?: unknown; realTimer?: boolean } = {}) {
  const calls: string[] = []
  const detected = [...(o.detected ?? [FOUND])]
  // The timer of watch(), by hand: the test says when a tick comes.
  const timer = { tick: (): void => undefined, everyMs: 0, stopped: false }
  const interval = o.realTimer
    ? undefined
    : (tick: () => void, everyMs: number): (() => void) => {
        timer.tick = tick
        timer.everyMs = everyMs
        return () => void (timer.stopped = true)
      }
  const client: LocalClient = {
    detect: () => {
      calls.push("detect")
      return Promise.resolve((detected.length > 1 ? detected.shift() : detected[0]) ?? NOT_FOUND)
    },
    connectServer: (d: Daemon, a: { code: string }) => {
      calls.push(`connectServer ${d.port} ${a.code}`)
      return Promise.resolve(o.connect ?? { granted: true })
    },
    connectBrowser: (d: Daemon) => {
      calls.push(`connectBrowser ${d.port}`)
      return Promise.resolve(o.connect ?? { granted: true })
    },
    disconnect: (d: Daemon) => {
      calls.push(`disconnect ${d.port}`)
      return Promise.resolve(o.disconnected as DisconnectResult | undefined)
    },
  }
  if (o.mode === "browser") return { local: createLocalDaemon({ mode: "browser", client, ...(interval ? { interval } : {}) }), calls, timer }
  const local = createLocalDaemon({
    client,
    ...(interval ? { interval } : {}),
    startPairing: () => {
      calls.push("startPairing")
      return Promise.resolve(o.code ?? "CODE")
    },
    cancelPairing: () => {
      calls.push("cancelPairing")
      return Promise.resolve()
    },
  })
  return { local, calls, timer }
}

describe("createLocalDaemon", () => {
  test("starts with nothing found, and the first look not done", () => {
    expect(setup().local.state).toEqual({ found: NOT_FOUND, connecting: false, error: "", canConnect: false, phase: "detecting" })
  })

  test("detect keeps what the client found", async () => {
    const t = setup({ detected: [FOUND] })
    await t.local.detect()
    expect(t.local.state.found).toEqual(FOUND)
  })

  test("canConnect only for a daemon that is here and not yet connected to this app", async () => {
    const t = setup({ detected: [FOUND] })
    expect(t.local.state.canConnect).toBe(false)
    await t.local.detect()
    expect(t.local.state.canConnect).toBe(true)
    const done = setup({ detected: [CONNECTED] })
    await done.local.detect()
    expect(done.local.state.canConnect).toBe(false)
  })

  test("connect asks the server for a code, hands it to the daemon, and looks again", async () => {
    const t = setup({ detected: [FOUND, CONNECTED] })
    await t.local.detect()
    expect(await t.local.connect()).toBe(true)
    expect(t.calls).toEqual(["detect", "startPairing", "connectServer 47615 CODE", "detect"])
    expect(t.local.state.found).toEqual(CONNECTED)
    expect(t.local.state.error).toBe("")
  })

  test("a refusal on the machine cancels the pairing and says why in plain words", async () => {
    const t = setup({ connect: { granted: false, reason: "denied" } })
    await t.local.detect()
    expect(await t.local.connect()).toBe(false)
    expect(t.calls).toContain("cancelPairing")
    expect(t.local.state.error).toContain("declined")
    expect(t.local.state.connecting).toBe(false)
  })

  test("looking again clears the last refusal: it described an earlier attempt", async () => {
    const t = setup({ connect: { granted: false, reason: "denied" } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state.error).not.toBe("")
    await t.local.detect()
    expect(t.local.state.error).toBe("")
    expect(t.local.state.found.found).toBe(true)
  })

  test("a look while a connection waits for the dialog changes nothing (the dialog returns the focus that triggers it)", async () => {
    const t = setup()
    await t.local.detect()
    const pending = t.local.connect()
    await t.local.detect()
    await pending
    expect(t.calls.filter((c) => c === "detect")).toHaveLength(2) // the first look, and the one after the connection
  })

  test("a look that began before the connection and ends during it does not end the connecting state", async () => {
    let answerLook: (d: Detected) => void = () => undefined
    let release: (r: ConnectResult) => void = () => undefined
    const client: LocalClient = {
      detect: () => new Promise<Detected>((resolve) => (answerLook = resolve)),
      connectServer: () => new Promise<ConnectResult>((resolve) => (release = resolve)),
      connectBrowser: () => Promise.reject(new Error("server mode")),
      disconnect: () => Promise.resolve(),
    }
    const local = createLocalDaemon({ client, startPairing: () => Promise.resolve("CODE"), cancelPairing: () => Promise.resolve() })
    const firstLook = local.detect() // the focus event: a detect is in flight, the daemon not yet known
    answerLook(FOUND)
    await firstLook
    const secondLook = local.detect() // another focus: in flight again while the user presses Connect
    const connecting = local.connect()
    expect(local.state.connecting).toBe(true)
    answerLook(FOUND) // the older look answers while the dialog is still open
    await secondLook
    expect(local.state.connecting).toBe(true)
    release({ granted: true })
    await new Promise((resolve) => setTimeout(resolve, 0)) // connect has now asked for its own look
    answerLook(CONNECTED)
    expect(await connecting).toBe(true)
    expect(local.state.connecting).toBe(false)
  })

  test("a server that gives no code never reaches the daemon", async () => {
    const t = setup({ code: "" })
    await t.local.detect()
    expect(await t.local.connect()).toBe(false)
    expect(t.calls.some((c) => c.startsWith("connectServer"))).toBe(false)
    expect(t.local.state.error).not.toBe("")
  })

  test("nothing happens without a daemon, and a second press while one is running is ignored", async () => {
    const none = setup({ detected: [NOT_FOUND] })
    await none.local.detect()
    expect(await none.local.connect()).toBe(false)
    expect(none.calls).toEqual(["detect"])

    const t = setup()
    await t.local.detect()
    const first = t.local.connect()
    expect(t.local.state.connecting).toBe(true)
    expect(await t.local.connect()).toBe(false)
    await first
    expect(t.calls.filter((c) => c === "startPairing")).toHaveLength(1)
  })
})

describe("createLocalDaemon in browser mode", () => {
  test("canConnect while this page has no browser grant, whatever the server grant", async () => {
    const t = setup({ mode: "browser", detected: [CONNECTED] }) // CONNECTED has the server grant only
    await t.local.detect()
    expect(t.local.state.canConnect).toBe(true)
    const done = setup({ mode: "browser", detected: [BROWSER] })
    await done.local.detect()
    expect(done.local.state.canConnect).toBe(false)
  })

  test("connect asks the daemon for this page's browser grant, without a pairing, and looks again", async () => {
    const t = setup({ mode: "browser", detected: [FOUND, BROWSER] })
    await t.local.detect()
    expect(await t.local.connect()).toBe(true)
    expect(t.calls).toEqual(["detect", "connectBrowser 47615", "detect"])
    expect(t.local.state).toMatchObject({ found: BROWSER, connecting: false, error: "", canConnect: false })
  })

  test("a refusal on the machine says why in plain words", async () => {
    const t = setup({ mode: "browser", connect: { granted: false, reason: "denied" } })
    await t.local.detect()
    expect(await t.local.connect()).toBe(false)
    expect(t.local.state.error).toContain("declined")
    expect(t.local.state.connecting).toBe(false)
    expect(t.calls.some((c) => c.startsWith("connectServer"))).toBe(false)
  })
})

describe("disconnect", () => {
  test.each<["server" | "browser", Detected]>([
    ["server", CONNECTED],
    ["browser", BROWSER],
  ])("in %s mode takes back what this page was allowed and looks again", async (mode, granted) => {
    const t = setup({ mode, detected: [granted, FOUND] })
    await t.local.detect()
    expect(t.local.state.canConnect).toBe(false)
    await t.local.disconnect()
    expect(t.calls).toEqual(["detect", "disconnect 47615", "detect"])
    expect(t.local.state.canConnect).toBe(true)
  })

  test("without a daemon only looks", async () => {
    const t = setup({ detected: [NOT_FOUND] })
    await t.local.disconnect()
    expect(t.calls).toEqual(["detect"])
  })
})

describe("phase", () => {
  test("detecting until the first look ends, then absent, available or connected", async () => {
    const none = setup({ detected: [NOT_FOUND] })
    expect(none.local.state.phase).toBe("detecting")
    await none.local.detect()
    expect(none.local.state.phase).toBe("absent")

    const here = setup({ detected: [FOUND] })
    await here.local.detect()
    expect(here.local.state.phase).toBe("available")

    const done = setup({ detected: [CONNECTED] })
    await done.local.detect()
    expect(done.local.state.phase).toBe("connected")
  })

  test("a daemon that went away after the first look is absent, not detecting again", async () => {
    const t = setup({ detected: [FOUND, NOT_FOUND] })
    await t.local.detect()
    await t.local.detect()
    expect(t.local.state.phase).toBe("absent")
  })

  test("in browser mode only this page's own grant is connected", async () => {
    const server = setup({ mode: "browser", detected: [CONNECTED] }) // the server grant only
    await server.local.detect()
    expect(server.local.state.phase).toBe("available")
    const page = setup({ mode: "browser", detected: [BROWSER] })
    await page.local.detect()
    expect(page.local.state.phase).toBe("connected")
  })

  test("connecting while a connection waits for the dialog", async () => {
    const t = setup({ detected: [FOUND, CONNECTED] })
    await t.local.detect()
    const pending = t.local.connect()
    expect(t.local.state.phase).toBe("connecting")
    await pending
    expect(t.local.state.phase).toBe("connected")
  })

  test("a refusal is refused, with the reason and the daemon's own words", async () => {
    const t = setup({ connect: { granted: false, reason: "busy", message: "another dialog is open" } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state).toMatchObject({ phase: "refused", reason: "busy", message: "another dialog is open", connecting: false, canConnect: true })
    expect(t.local.state.error).toContain("Another connection request")
  })

  test("a refusal without a message has no message key; a look clears the whole refusal", async () => {
    const t = setup({ mode: "browser", connect: { granted: false, reason: "denied" } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state).toMatchObject({ phase: "refused", reason: "denied" })
    expect("message" in t.local.state).toBe(false)
    await t.local.detect()
    expect(t.local.state.phase).toBe("available")
    expect("reason" in t.local.state).toBe(false)
    expect(t.local.state.error).toBe("")
  })

  test("a reason this SDK does not know still gets words, and is kept as the daemon sent it", async () => {
    const t = setup({ connect: { granted: false, reason: "brand-new" as Reason } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state.reason).toBe("brand-new" as Reason)
    expect(t.local.state.error).toContain("brand-new")
  })

  test("a refusal that is not the daemon's (the server gave no code) is refused with words and the generic reason 'error'", async () => {
    const t = setup({ code: "" })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state.phase).toBe("refused")
    expect(t.local.state.error).not.toBe("")
    expect(t.local.state.reason).toBe("error")
  })

  test("a failure of the app's own pairing call is refused with the error's words", async () => {
    const client: LocalClient = { detect: () => Promise.resolve(FOUND), connectServer: () => Promise.reject(new Error("unused")), connectBrowser: () => Promise.reject(new Error("unused")), disconnect: () => Promise.resolve() }
    const local = createLocalDaemon({ client, startPairing: () => Promise.reject(new Error("the server is down")), cancelPairing: () => Promise.resolve() })
    await local.detect()
    expect(await local.connect()).toBe(false)
    expect(local.state).toMatchObject({ phase: "refused", error: "the server is down" })
  })
})

describe("a daemon whose protocol this SDK does not speak", () => {
  const WITH = (protocol: number): Detected => ({ ...FOUND, protocol })

  test("is refused with a sentence, and cannot be connected", async () => {
    const t = setup({ detected: [WITH(99)] })
    await t.local.detect()
    expect(t.local.state).toMatchObject({ phase: "refused", canConnect: false, connecting: false })
    expect(t.local.state.incompatible).toContain("newer")
    expect(t.local.state.error).toBe(t.local.state.incompatible ?? "?")
    expect(await t.local.connect()).toBe(false)
    expect(t.calls).toEqual(["detect"])
  })

  test("says too old for a protocol below the ones it speaks", async () => {
    const t = setup({ detected: [WITH(0)] })
    await t.local.detect()
    expect(t.local.state.incompatible).toContain("too old")
  })

  test("a protocol it speaks, or none stated, is not incompatible", async () => {
    for (const found of [WITH(1), FOUND]) {
      const t = setup({ detected: [found] })
      await t.local.detect()
      expect(t.local.state.phase).toBe("available")
      expect("incompatible" in t.local.state).toBe(false)
    }
  })

  test("goes away when the next look finds a daemon that fits", async () => {
    const t = setup({ detected: [WITH(99), FOUND] })
    await t.local.detect()
    await t.local.detect()
    expect(t.local.state).toMatchObject({ phase: "available", canConnect: true, error: "" })
  })
})

describe("watch", () => {
  test("looks at once, then on every tick, every 3 s unless told otherwise", async () => {
    const t = setup({ detected: [NOT_FOUND, FOUND] })
    t.local.watch()
    await flush()
    expect(t.calls).toEqual(["detect"])
    expect(t.local.state.phase).toBe("absent")
    expect(t.timer.everyMs).toBe(3000)
    t.timer.tick()
    await flush()
    expect(t.local.state.phase).toBe("available") // installed meanwhile: found without a reload
    expect(setup().timer.everyMs).toBe(0)
    const other = setup()
    other.local.watch(500)
    expect(other.timer.everyMs).toBe(500)
  })

  test("goes on looking while the phase is detecting, absent or available", async () => {
    const t = setup({ detected: [FOUND] })
    t.local.watch()
    await flush()
    t.timer.tick()
    t.timer.tick()
    await flush()
    expect(t.calls.filter((c) => c === "detect").length).toBeGreaterThanOrEqual(2)
    expect(t.local.state.phase).toBe("available")
  })

  test("keeps looking while connected, and says nothing while nothing changes", async () => {
    const t = setup({ detected: [CONNECTED] })
    const heard: string[] = []
    t.local.watch()
    await flush()
    expect(t.local.state.phase).toBe("connected")
    t.local.subscribe((s) => heard.push(s.phase))
    t.timer.tick()
    await flush()
    t.timer.tick()
    await flush()
    expect(t.calls).toEqual(["detect", "detect", "detect"])
    expect(heard).toEqual([])
  })

  test("looks no more while a connection waits for the dialog", async () => {
    let release: (r: ConnectResult) => void = () => undefined
    let looks = 0
    const client: LocalClient = {
      detect: () => ((looks += 1), Promise.resolve(FOUND)),
      connectServer: () => new Promise<ConnectResult>((resolve) => (release = resolve)), // the dialog stays open
      connectBrowser: () => Promise.reject(new Error("server mode")),
      disconnect: () => Promise.resolve(),
    }
    let tick: () => void = () => undefined
    const local = createLocalDaemon({ client, startPairing: () => Promise.resolve("CODE"), cancelPairing: () => Promise.resolve(), interval: (fn) => ((tick = fn), () => undefined) })
    await local.detect()
    const pending = local.connect()
    await flush() // connect has asked the daemon and waits for the answer
    expect(local.state.phase).toBe("connecting")
    local.watch()
    tick()
    tick()
    await flush()
    expect(looks).toBe(1)
    release({ granted: true })
    await pending
  })

  test("leaves a refusal on the screen: a tick would clear it before the person read it", async () => {
    const t = setup({ connect: { granted: false, reason: "denied" } })
    await t.local.detect()
    await t.local.connect()
    expect(t.local.state.phase).toBe("refused")
    t.local.watch()
    t.timer.tick()
    await flush()
    expect(t.local.state.phase).toBe("refused")
    expect(t.calls.filter((c) => c === "detect")).toHaveLength(1)
  })

  test("goes on looking at a daemon that is too old or new: the person may update it", async () => {
    const t = setup({ detected: [{ ...FOUND, protocol: 99 }, FOUND] })
    t.local.watch()
    await flush()
    expect(t.local.state.phase).toBe("refused")
    t.timer.tick()
    await flush()
    expect(t.local.state.phase).toBe("available")
  })

  test("tells a listener about a change and about nothing else", async () => {
    const t = setup({ detected: [FOUND] })
    const seen: LocalState[] = []
    t.local.subscribe((s) => seen.push(s))
    t.local.watch()
    await flush()
    const count = seen.length
    expect(count).toBeGreaterThan(0)
    t.timer.tick()
    t.timer.tick()
    await flush()
    expect(seen).toHaveLength(count)
  })

  test("a tick that comes while a look is still out starts no second look", async () => {
    let answer: (d: Detected) => void = () => undefined
    let looks = 0
    const client: LocalClient = {
      detect: () => new Promise<Detected>((resolve) => ((looks += 1), (answer = resolve))),
      connectServer: () => Promise.reject(new Error("unused")),
      connectBrowser: () => Promise.reject(new Error("unused")),
      disconnect: () => Promise.resolve(),
    }
    let tick: () => void = () => undefined
    const local = createLocalDaemon({ client, startPairing: () => Promise.resolve("C"), cancelPairing: () => Promise.resolve(), interval: (fn) => ((tick = fn), () => undefined) })
    local.watch()
    tick()
    tick()
    expect(looks).toBe(1)
    answer(NOT_FOUND)
    await flush()
    tick()
    expect(looks).toBe(2)
  })

  test("the function it returns ends the watching", () => {
    const t = setup()
    const stop = t.local.watch()
    expect(t.timer.stopped).toBe(false)
    stop()
    expect(t.timer.stopped).toBe(true)
    stop() // twice is as once
  })

  test("without an interval of its own it uses setInterval and clearInterval", () => {
    const real = { set: globalThis.setInterval, clear: globalThis.clearInterval }
    const made: number[] = []
    const cleared: unknown[] = []
    globalThis.setInterval = ((_fn: () => void, ms: number) => (made.push(ms), 42)) as unknown as typeof setInterval
    globalThis.clearInterval = ((id: unknown) => void cleared.push(id)) as typeof clearInterval
    try {
      const t = setup({ realTimer: true })
      const stop = t.local.watch(1234)
      expect(made).toEqual([1234])
      stop()
      expect(cleared).toEqual([42])
    } finally {
      globalThis.setInterval = real.set
      globalThis.clearInterval = real.clear
    }
  })
})

describe("disconnect that fails", () => {
  test("shows why, after the look that follows it", async () => {
    const t = setup({ detected: [CONNECTED], disconnected: { disconnected: false, reason: "unreachable", message: "Failed to fetch" } })
    await t.local.detect()
    await t.local.disconnect()
    expect(t.local.state).toMatchObject({ phase: "refused", reason: "unreachable", message: "Failed to fetch" })
    expect(t.local.state.error).toContain("did not answer")
  })

  test("a client that returns nothing, or a success, shows nothing", async () => {
    for (const disconnected of [undefined, { disconnected: true }]) {
      const t = setup({ detected: [CONNECTED, FOUND], disconnected })
      await t.local.detect()
      await t.local.disconnect()
      expect(t.local.state).toMatchObject({ phase: "available", error: "" })
    }
  })
})

describe("LocalDaemonDeps", () => {
  test("the server-mode shape an app passes today needs no mode and no client (MindChains' wrapper)", () => {
    // frontend/src/composables/createLocalDaemon.ts in MindChains: deps typed as LocalDaemonDeps, given only the pairing calls.
    const wrap = (deps: LocalDaemonDeps) => createLocalDaemon(deps)
    const local = wrap({ startPairing: () => Promise.resolve("CODE"), cancelPairing: () => Promise.resolve() })
    expect(local.state.canConnect).toBe(false)
    // @ts-expect-error browser mode has no pairing: a server's pairing calls there are a mistake the types catch
    wrap({ mode: "browser", startPairing: () => Promise.resolve("CODE"), cancelPairing: () => Promise.resolve() })
    // @ts-expect-error server mode cannot connect without a pairing
    wrap({ mode: "server" })
  })
})

describe("subscribe", () => {
  test("tells every change with the state as it is, and stops after the unsubscribe", async () => {
    const t = setup({ detected: [FOUND, CONNECTED] })
    const seen: LocalState[] = []
    const off = t.local.subscribe((s) => seen.push(s))
    await t.local.detect()
    expect(seen.at(-1)?.found).toEqual(FOUND)
    const count = seen.length
    off()
    await t.local.connect()
    expect(seen).toHaveLength(count)
  })

  test("each state is a new object, so a framework can tell a change by identity", async () => {
    const t = setup()
    const before = t.local.state
    await t.local.detect()
    expect(t.local.state).not.toBe(before)
    expect(before.found).toEqual(NOT_FOUND)
  })

  test("a listener that throws does not stop the others", async () => {
    const t = setup()
    const seen: number[] = []
    t.local.subscribe(() => {
      throw new Error("a bad listener")
    })
    t.local.subscribe(() => seen.push(1))
    await t.local.detect()
    expect(seen.length).toBeGreaterThan(0)
  })
})
