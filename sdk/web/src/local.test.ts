import { describe, expect, test } from "bun:test"
import type { ConnectResult, Daemon, Detected } from "./client.ts"
import { createLocalDaemon, type LocalClient, type LocalDaemonDeps, type LocalState } from "./local.ts"

const NOT_FOUND: Detected = { found: false }
const FOUND: Detected = { found: true, port: 47615, version: "1.0.0", granted: { server: false, browser: false }, agents: undefined }
const CONNECTED: Detected = { ...FOUND, granted: { server: true, browser: false }, agents: [{ name: "claude", title: "Claude Code", ready: true, reason: "" }] }
const BROWSER: Detected = { ...FOUND, granted: { server: false, browser: true } }

function setup(o: { detected?: Detected[]; connect?: ConnectResult; code?: string; mode?: "server" | "browser" } = {}) {
  const calls: string[] = []
  const detected = [...(o.detected ?? [FOUND])]
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
      return Promise.resolve()
    },
  }
  if (o.mode === "browser") return { local: createLocalDaemon({ mode: "browser", client }), calls }
  const local = createLocalDaemon({
    client,
    startPairing: () => {
      calls.push("startPairing")
      return Promise.resolve(o.code ?? "CODE")
    },
    cancelPairing: () => {
      calls.push("cancelPairing")
      return Promise.resolve()
    },
  })
  return { local, calls }
}

describe("createLocalDaemon", () => {
  test("starts with nothing found", () => {
    expect(setup().local.state).toEqual({ found: NOT_FOUND, connecting: false, error: "", canConnect: false })
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
