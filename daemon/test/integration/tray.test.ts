import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { build, Sandbox, TestDaemon, TestServer, TOKEN, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const dirs: string[] = []
const servers: TestServer[] = []
let daemon: TestDaemon | undefined
let box: Sandbox | undefined

afterEach(async () => {
  await daemon?.stop()
  for (const s of servers.splice(0)) s.stop()
  box?.remove()
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
  daemon = box = undefined
})

/** A stand-in for the tray program: it records its pid and every line it gets, and prints `reply` after the first one. */
function fakeTray(reply: string[] = [], tail = "cat >> $LINES"): { file: string; lines: () => string[]; pid: () => number } {
  const dir = mkdtempSync(join(tmpdir(), "mc-tray-"))
  dirs.push(dir)
  const lines = join(dir, "lines")
  const pid = join(dir, "pid")
  const file = join(dir, "tray")
  const echoes = reply.map((r) => `echo '${r}'`).join("\n")
  writeFileSync(file, `#!/bin/sh\nLINES=${lines}\necho $$ > ${pid}\nread first\necho "$first" >> $LINES\n${echoes}\n${tail}\n`)
  chmodSync(file, 0o755)
  return {
    file,
    lines: () => (existsSync(lines) ? readFileSync(lines, "utf8").split("\n").filter(Boolean) : []),
    pid: () => (existsSync(pid) ? Number(readFileSync(pid, "utf8")) : 0),
  }
}

async function start(tray: string, apps: TestServer[] = [], env: Record<string, string> = {}) {
  box = new Sandbox()
  box.writeApps(apps.map((s) => ({ server: s.origin, token: TOKEN })))
  daemon = await TestDaemon.start(box, [], { AGENTD_TRAY: tray, ...env })
}

const alive = (pid: number) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

describe("the tray program", () => {
  test("gets the state: version, the apps, the sessions, whether a service restarts the daemon", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const t = fakeTray()
    await start(t.file, [app], { AGENTD_MANAGED: "1" })
    await waitFor("the first state", () => t.lines()[0])
    expect(JSON.parse(t.lines()[0]!)).toMatchObject({ t: "state", version: "0.0.0-itest", apps: [app.origin], pages: [], sessions: 0, managed: true, gatewayOff: false })
  })

  test("gets a new state when something changes, and only then", async () => {
    const t = fakeTray()
    await start(t.file)
    await waitFor("the first state", () => t.lines().length >= 1)
    await new Promise((r) => setTimeout(r, 5000))
    expect(t.lines()).toHaveLength(1)
    box!.writeApps([], [{ origin: "http://localhost:5173", grantedAt: "2026-10-19T00:00:00.000Z" }])
    await waitFor("a state with the page", () => t.lines().some((l) => (JSON.parse(l) as { pages: string[] }).pages.includes("http://localhost:5173")), 10_000)
  }, 30_000)

  test("Stop in its menu stops the daemon with 0", async () => {
    const t = fakeTray(['{"t":"action","id":"stop"}'])
    await start(t.file)
    expect(await daemon!.exited).toBe(0)
    expect(daemon!.out).toContain("stop requested from the tray menu")
  })

  test("Restart in its menu is ignored by a daemon that no service restarts", async () => {
    const t = fakeTray(['{"t":"action","id":"restart"}'])
    await start(t.file)
    await waitFor("the restart to be refused", () => daemon!.out.includes("not managed: restart ignored"))
    expect(daemon!.proc.exitCode).toBeNull()
  })

  test("Disconnect in its menu removes that app and keeps the daemon up", async () => {
    const app = await TestServer.start()
    servers.push(app)
    const t = fakeTray([`{"t":"action","id":"disconnect:${app.origin}"}`])
    await start(t.file, [app])
    await app.waitHellos(1)
    await waitFor("the app to be disconnected", async () => !(await app.status()).connected)
    expect((JSON.parse(readFileSync(box!.configFile, "utf8")) as { apps: unknown[] }).apps).toEqual([])
    expect(daemon!.proc.exitCode).toBeNull()
  })

  test("an action it does not know does nothing", async () => {
    const t = fakeTray(['{"t":"action","id":"format-disk"}', "not json"])
    await start(t.file)
    await new Promise((r) => setTimeout(r, 1500))
    expect(daemon!.proc.exitCode).toBeNull()
  })

  test("ends with the daemon: its input closes", async () => {
    const t = fakeTray()
    await start(t.file)
    const pid = await waitFor("the tray pid", () => t.pid() || undefined)
    await daemon!.stop()
    await waitFor("the tray to end", () => !alive(pid), 8000)
    expect(alive(pid)).toBe(false)
  })

  test("one that cannot start (no display) is given up after a few tries, and the daemon runs on", async () => {
    const t = fakeTray([], "exit 1")
    await start(t.file)
    await waitFor("the give-up line", () => daemon!.out.includes("tray: giving up"), 20_000)
    expect(daemon!.proc.exitCode).toBeNull()
  }, 40_000)

  test("AGENTD_TRAY=off starts none", async () => {
    const t = fakeTray()
    box = new Sandbox()
    daemon = await TestDaemon.start(box, [], { AGENTD_TRAY: "off" })
    await waitFor("the daemon to serve", () => daemon!.out.includes("serving agents"))
    await new Promise((r) => setTimeout(r, 1000))
    expect(t.pid()).toBe(0)
  })
})
