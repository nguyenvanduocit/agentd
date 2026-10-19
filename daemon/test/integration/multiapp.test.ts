import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { build, groupAlive, Sandbox, TestDaemon, TestServer, TOKEN, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

const AGENT = "sh -c 'sleep 60'"
let servers: TestServer[] = []
let daemon: TestDaemon | undefined
let box: Sandbox | undefined
const pgids: number[] = []

afterEach(async () => {
  await daemon?.stop()
  for (const g of pgids.splice(0)) {
    try {
      process.kill(-g, "SIGKILL")
    } catch {
      /* gone */
    }
  }
  for (const s of servers) s.stop()
  box?.remove()
  servers = []
  daemon = undefined
  box = undefined
})

/** Starts `n` server doubles and one daemon paired with all of them. */
async function start(n: number, saved?: (origins: string[]) => { server: string; token: string }[]) {
  servers = await Promise.all(Array.from({ length: n }, () => TestServer.start()))
  box = new Sandbox()
  const origins = servers.map((s) => s.origin)
  box.writeApps(saved ? saved(origins) : origins.map((server) => ({ server, token: TOKEN })))
  daemon = await TestDaemon.start(box, [], { AGENTD_AGENT_CLAUDE: AGENT })
  return servers
}

describe("several apps", () => {
  test("every app gets hello, and a session id means one agent per app", async () => {
    const [a, b] = await start(2)
    await a!.waitHellos(1)
    await b!.waitHellos(1)
    const pa = await a!.openOk("s1")
    const pb = await b!.openOk("s1")
    pgids.push(pa.pid, pb.pid)
    expect(pa.pid).not.toBe(pb.pid)
    expect(groupAlive(pa.pid) && groupAlive(pb.pid)).toBe(true)
    await a!.write("s1", '{"a":1}')
    await waitFor("frames of a only", async () => (await a!.status()).frames >= 0)
    expect((await b!.frames("s1")).every((f) => f.data !== '{"a":1}')).toBe(true)
  })

  test("Stop from one app detaches it and the other keeps serving", async () => {
    const [a, b] = await start(2)
    await a!.waitHellos(1)
    await b!.waitHellos(1)
    await a!.stopDaemon()
    await waitFor("a is disconnected", async () => !(await a!.status()).connected)
    expect(daemon!.proc.exitCode).toBeNull()
    expect((await b!.status()).connected).toBe(true)
    const pb = await b!.openOk("s2")
    pgids.push(pb.pid)
    expect(groupAlive(pb.pid)).toBe(true)
  })

  test("Stop from the last app exits 0", async () => {
    const [a] = await start(1)
    await a!.waitHellos(1)
    await a!.stopDaemon()
    expect(await daemon!.exited).toBe(0)
  })

  test("a refused token on one app leaves the other serving", async () => {
    const [a, b] = await start(2, (o) => [{ server: o[0]!, token: "wrong" }, { server: o[1]!, token: TOKEN }])
    await b!.waitHellos(1)
    await waitFor("a rejected the token", async () => (await a!.status()).rejects >= 1)
    expect(daemon!.proc.exitCode).toBeNull()
    expect((await b!.status()).connected).toBe(true)
  })

  test("a daemon with nothing paired stays up and says where it serves", async () => {
    box = new Sandbox()
    daemon = await TestDaemon.start(box, [])
    await waitFor("the serving line", () => daemon!.out.includes("serving agents"))
    await new Promise((r) => setTimeout(r, 1500))
    expect(daemon.proc.exitCode).toBeNull()
  })
})
