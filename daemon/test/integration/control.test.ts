// Restart and Stop from the server, and a revoked token: the exit codes that tell the service what to do
// (0 stay stopped, 75 start again now), with real processes: a daemon directly, and under the launcher's restart loop.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test"
import { mkdtempSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { build, groupAlive, Rig, sh, sleep, TestServer, waitFor } from "./harness.ts"
import { buildVersion, cleanVersions, Machine, type Version } from "./versions.ts"

let rig: Rig | undefined
let machine: Machine | undefined
let server: TestServer | undefined
let one: Version
const dirs: string[] = []

beforeAll(async () => {
  await build()
  const dir = mkdtempSync(join(tmpdir(), "mc-control-"))
  dirs.push(dir)
  one = await buildVersion("1.0.0", {}, dir)
}, 300_000)

afterEach(async () => {
  await rig?.stop()
  await machine?.stop()
  server?.stop()
  rig = machine = server = undefined
})

afterAll(() => {
  cleanVersions()
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
})

/** An agent with a child, so a lingering process shows. */
const TREE = sh("sleep 600 & echo up; wait")

async function managedMachine(): Promise<{ s: TestServer; m: Machine }> {
  const s = (server = await TestServer.start())
  const m = (machine = new Machine())
  await m.install(one)
  m.pair(s.origin)
  m.startLoop({ AGENTD_AGENT_CLAUDE: TREE })
  await s.waitHellos(1)
  return { s, m }
}

describe("a daemon that no service restarts (not managed)", () => {
  test("hello says managed:false; Restart is ignored and the daemon keeps running; Stop exits 0", async () => {
    rig = await Rig.start({ claude: TREE })
    const s = rig.server
    expect((await s.status()).hellos?.[0]?.managed).toBe(false)
    const pid = rig.daemon.pid
    expect(await s.restart()).toEqual({ ok: true })
    await waitFor("the log line", () => rig?.daemon.out.includes("not managed: restart ignored"))
    await sleep(1500)
    expect(rig.daemon.proc.exitCode).toBeNull()
    expect(rig.daemon.pid).toBe(pid)
    expect((await s.status()).connected).toBe(true)

    await s.stopDaemon()
    expect(await rig.daemon.exited).toBe(0)
    expect(rig.daemon.out).toContain("stop requested by the server")
  })

  test("Stop in the middle of a turn ends the agent's whole process tree, and the exit frame still arrives", async () => {
    rig = await Rig.start({ claude: TREE })
    const { pid } = await rig.open("busy")
    await rig.frame("busy", "the agent's first line", (f) => f.kind === "out")
    expect(groupAlive(pid)).toBe(true)
    await rig.server.stopDaemon()
    expect(await rig.daemon.exited).toBe(0)
    expect(groupAlive(pid)).toBe(false)
    expect((await rig.server.frames("busy")).some((f) => f.kind === "exit")).toBe(true)
  })
})

describe("a daemon under the launcher's restart loop (managed)", () => {
  test("hello says managed:true", async () => {
    const { s } = await managedMachine()
    expect((await s.status()).hellos?.[0]?.managed).toBe(true)
  })

  test("Restart mid-turn: the tree is gone, a new process (new pid) connects with a new hello", async () => {
    const { s, m } = await managedMachine()
    const { pid } = await s.openOk("busy")
    await waitFor("the agent's first line", async () => (await s.frames("busy")).some((f) => f.kind === "out"))
    const daemon = await waitFor("the daemon pid", () => m.daemonPid())
    expect(await s.restart()).toEqual({ ok: true })
    await s.waitHellos(2)
    expect(m.daemonPid()).not.toBe(daemon)
    expect(groupAlive(pid)).toBe(false)
    expect((await s.frames("busy")).some((f) => f.kind === "exit")).toBe(true)
    expect(m.out).toContain("restart requested by the server")
    expect(m.out).toContain("starting the daemon again at once")
    expect(m.out).not.toContain("starting it again in")
  })

  test("Stop mid-turn: the tree is gone and nothing starts the daemon again", async () => {
    const { s, m } = await managedMachine()
    const { pid } = await s.openOk("busy")
    await waitFor("the agent's first line", async () => (await s.frames("busy")).some((f) => f.kind === "out"))
    expect(await s.stopDaemon()).toEqual({ ok: true })
    expect(await m.launcherExit).toBe(0)
    expect(groupAlive(pid)).toBe(false)
    await sleep(3000)
    expect((await s.status()).hellos?.length).toBe(1)
    expect((await s.status()).connected).toBe(false)
    expect(readFileSync(join(m.home, "run", "last-exit"), "utf8").trim()).toBe("0")
    expect(m.out).toContain("it stays stopped")
  })

  test("a revoked token: the daemon exits 0 and does not retry", async () => {
    const { s, m } = await managedMachine()
    await s.revoke()
    expect(await m.launcherExit).toBe(0)
    const rejects = (await s.status()).rejects
    expect(rejects).toBe(1)
    await sleep(3000)
    expect((await s.status()).rejects).toBe(rejects)
    expect(m.out).toContain("stays stopped")
  })
})
