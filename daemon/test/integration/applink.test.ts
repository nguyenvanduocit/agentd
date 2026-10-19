// The daemon as agentd.app runs it: the app starts it with a channel on fd 3 (AGENTD_UI_FD) and shows its state; the
// daemon starts no tray program of its own, does what the app chooses, and ends when the app goes.
import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { existsSync, readFileSync } from "node:fs"
import { join } from "node:path"
import type { Duplex } from "node:stream"
import { build, Sandbox, sh, TestDaemon, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const PAGE = "http://localhost:5173"
let daemon: TestDaemon | undefined
let box: Sandbox | undefined
afterEach(async () => {
  await daemon?.stop()
  box?.remove()
  daemon = box = undefined
})

/** The lines the daemon sends the app, parsed. */
function lines(app: Duplex): Record<string, unknown>[] {
  const got: Record<string, unknown>[] = []
  let rest = ""
  app.on("data", (d: Buffer) => {
    const parts = (rest + d.toString()).split("\n")
    rest = parts.pop() ?? ""
    for (const p of parts) if (p !== "") got.push(JSON.parse(p) as Record<string, unknown>)
  })
  return got
}

async function start() {
  box = new Sandbox()
  box.writeApps([], [{ origin: PAGE, grantedAt: "2026-10-19T00:00:00.000Z" }])
  const marker = join(box.root, "tray-started")
  const started = await TestDaemon.startWithApp(box, { AGENTD_TRAY: sh(`touch ${marker}; sleep 60`) })
  daemon = started.daemon
  return { app: started.app, got: lines(started.app), marker }
}

describe("a daemon started by agentd.app", () => {
  test("sends its state to the app on AGENTD_UI_FD, and starts no tray program", async () => {
    const { got, marker } = await start()
    const state = await waitFor("a state on fd 3", () => got.find((m) => m.t === "state"))
    expect(state.pages).toEqual([PAGE])
    expect(typeof state.version).toBe("string")
    expect(existsSync(marker)).toBe(false)
  })

  test("does what the app chooses", async () => {
    const { app, got } = await start()
    await waitFor("a state on fd 3", () => got.find((m) => m.t === "state"))
    const choice = JSON.stringify({ t: "action", id: "disconnect:" + PAGE })
    app.write(choice + "\n")
    await waitFor("the page disconnected", () => !readFileSync(box?.configFile ?? "", "utf8").includes(PAGE))
    const after = await waitFor("a state without the page", () => got.find((m) => m.t === "state" && Array.isArray(m.pages) && m.pages.length === 0))
    expect(after.pages).toEqual([])
  })

  test("ends when the app goes: its end of the channel closes, the daemon exits 0", async () => {
    const { app, got } = await start()
    await waitFor("a state on fd 3", () => got.find((m) => m.t === "state"))
    app.destroy()
    expect(await Promise.race([daemon?.exited, new Promise((r) => setTimeout(() => r("still running"), 15_000))])).toBe(0)
    expect(daemon?.out).toContain("agentd.app closed")
  })
})
