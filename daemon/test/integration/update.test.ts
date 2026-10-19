// Auto-update, rollback, restart: real daemon bundles under the real launcher, against the Go app-server double and a release
// server stub that the test steers.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test"
import { execFileSync, spawn } from "node:child_process"
import { existsSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { build, ReleaseServer, sh, sleep, TestServer, waitFor } from "./harness.ts"
import { buildVersion, cleanVersions, Machine, offerOf, ReleaseStub, writeRelease, type Version } from "./versions.ts"

let server: TestServer | undefined
let machine: Machine | undefined
let releases: ReleaseStub | undefined
const dirs: string[] = []

beforeAll(async () => {
  await build()
}, 300_000)

afterEach(async () => {
  await machine?.stop()
  server?.stop()
  releases?.stop()
  machine = server = releases = undefined
})
afterAll(() => {
  cleanVersions()
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
})

/** Builds `version` and puts its tarball on the release server (it is offered only when a test says so). */
async function publish(version: string, o: Parameters<typeof buildVersion>[1] = {}): Promise<Version> {
  releases ??= new ReleaseStub()
  const built = await buildVersion(version, o)
  releases.add(built)
  return built
}

/** An installed daemon at `running`, paired with an app, taking updates from the release server stub every 200 ms. */
async function setup(running: Version, env: Record<string, string> = {}): Promise<{ s: TestServer; m: Machine; r: ReleaseStub }> {
  const r = (releases ??= new ReleaseStub())
  const s = (server = await TestServer.start("test-token"))
  const m = (machine = new Machine())
  await m.install(running)
  m.pair(s.origin)
  m.startLoop({ AGENTD_RELEASE_URL: r.origin, AGENTD_RELEASE_CHECK_MS: "200", ...env })
  await s.waitHellos(1)
  return { s, m, r }
}

const versions = async (s: TestServer) => ((await s.status()).hellos ?? []).map((h) => h.version)

describe("update", () => {
  test("a newer version is downloaded, checked, unpacked, made current, and the daemon restarts on it and says hello with it", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const { s, m, r } = await setup(one)
    expect(await versions(s)).toEqual(["1.0.0"])

    r.offer(offerOf(two))
    await waitFor("hello of 2.0.0", async () => (await versions(s)).includes("2.0.0"), 60_000)

    expect(m.current()).toBe("2.0.0")
    expect(m.read("previous")).toBe("1.0.0")
    expect(m.versions()).toEqual(["1.0.0", "2.0.0"])
    expect(m.leftovers()).toEqual([])
    expect(r.downloads["agentd-2.0.0.tgz"]).toBe(1)
    expect(m.out).toContain("updated to 2.0.0")
    expect(m.out).toContain("starting the daemon again at once (2.0.0)")
    // the new daemon does not download again, however often it asks
    const asked = r.latestHits
    await waitFor("the daemon to ask again", () => r.latestHits >= asked + 3, 10_000)
    expect(r.downloads["agentd-2.0.0.tgz"]).toBe(1)
  })

  test("a daemon takes an update from the real release server (cmd/agentd-server): its latest.json is what the daemon reads", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const dist = mkdtempSync(join(tmpdir(), "mc-dist-"))
    dirs.push(dist)
    const real = await ReleaseServer.start(dist)
    try {
      server = await TestServer.start("test-token")
      const m = (machine = new Machine())
      await m.install(one)
      m.pair(server.origin)
      m.startLoop({ AGENTD_RELEASE_URL: real.origin, AGENTD_RELEASE_CHECK_MS: "200" })
      await server.waitHellos(1)
      expect(m.out).toContain("cannot read the release offer") // nothing published yet: the server says 503, the daemon waits
      writeRelease(dist, two)
      await waitFor("hello of 2.0.0", async () => (await versions(server!)).includes("2.0.0"), 60_000)
      expect(m.current()).toBe("2.0.0")
      expect(real.downloads()["agentd-2.0.0.tgz"]).toBe(1)
    } finally {
      real.stop()
    }
  })

  test("the update comes from the release server, whichever apps the daemon serves", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const r = (releases ??= new ReleaseStub())
    const first = (server = await TestServer.start("test-token"))
    const second = await TestServer.start("test-token")
    try {
      const m = (machine = new Machine())
      await m.install(one)
      m.box.writeApps([
        { server: first.origin, token: "test-token" },
        { server: second.origin, token: "test-token" },
      ])
      m.startLoop({ AGENTD_RELEASE_URL: r.origin, AGENTD_RELEASE_CHECK_MS: "200" })
      await first.waitHellos(1)
      await second.waitHellos(1)
      await first.stopDaemon() // the first app leaves: updates go on
      await waitFor("the first app is detached", async () => !(await first.status()).connected)
      r.offer(offerOf(two))
      await waitFor("hello of 2.0.0", async () => (await versions(second)).includes("2.0.0"), 60_000)
      expect(m.current()).toBe("2.0.0")
      expect(r.downloads["agentd-2.0.0.tgz"]).toBe(1)
    } finally {
      second.stop()
    }
  })

  test("a wrong sha256 changes nothing; the right one, offered next, updates", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const { s, m, r } = await setup(one)

    r.offer(offerOf(two, { sha256: "0".repeat(64) }))
    await waitFor("the refusal", () => m.out.includes("cannot update to 2.0.0: sha256 is"), 30_000)
    await sleep(500)
    expect(m.current()).toBe("1.0.0")
    expect(m.versions()).toEqual(["1.0.0"])
    expect(m.leftovers()).toEqual([])
    expect(m.read("previous")).toBeUndefined()
    expect(await versions(s)).toEqual(["1.0.0"])

    r.offer(offerOf(two))
    await waitFor("hello of 2.0.0", async () => (await versions(s)).includes("2.0.0"), 60_000)
    expect(m.current()).toBe("2.0.0")
  })

  test("an offer the release key did not sign is not downloaded; the signed one updates", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const { s, m, r } = await setup(one)

    r.offer(offerOf(two, { signature: offerOf(one).signature }))
    await waitFor("the refusal", () => m.out.includes("not updating to 2.0.0: the signature does not match the release key"), 30_000)
    await sleep(500)
    expect(m.current()).toBe("1.0.0")
    expect(m.versions()).toEqual(["1.0.0"])
    expect(r.downloads["agentd-2.0.0.tgz"]).toBeUndefined()

    r.offer(offerOf(two))
    await waitFor("hello of 2.0.0", async () => (await versions(s)).includes("2.0.0"), 60_000)
    expect(m.current()).toBe("2.0.0")
  })

  test("a release server that is down changes nothing, and is asked again", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const { s, m, r } = await setup(one)
    r.offer(undefined) // 503: nothing published
    await waitFor("a few answers", () => r.latestHits >= 4, 10_000)
    expect(m.current()).toBe("1.0.0")
    expect(m.out).toContain("cannot read the release offer")
    r.offer(offerOf(two))
    await waitFor("hello of 2.0.0", async () => (await versions(s)).includes("2.0.0"), 60_000)
  })

  test("an update waits while a session runs, and happens when the last one ends", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const { s, m, r } = await setup(one, { AGENTD_AGENT_CLAUDE: sh("sleep 600") })
    r.delayMs = 3000
    r.offer(offerOf(two))
    await waitFor("the daemon to connect", async () => (await s.status()).connected)
    const opened = await s.openOk("busy") // the download is still on its way: the daemon is busy when the update is in place
    await waitFor("the staged update", () => m.current() === "2.0.0", 30_000)

    await sleep(8000) // more than one idle check
    expect((await versions(s)).includes("2.0.0")).toBe(false)
    expect(m.out).not.toContain("restarting on 2.0.0")
    expect(opened.pid).toBeGreaterThan(0)

    await s.close("busy")
    await waitFor("hello of 2.0.0", async () => (await versions(s)).includes("2.0.0"), 60_000)
    expect(m.out).toContain("restarting on 2.0.0")
  })

  test("a daemon that is not an installed one never updates", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0")
    const r = (releases ??= new ReleaseStub())
    r.offer(offerOf(two))
    server = await TestServer.start("test-token")
    const m = (machine = new Machine())
    // run the bundle from the tarball's unpacked directory, outside <home>/daemon
    const dir = mkdtempSync(join(tmpdir(), "mc-notinstalled-"))
    dirs.push(dir)
    execFileSync("tar", ["-xzf", one.tgz, "-C", dir, "--strip-components=1"])
    m.pair(server.origin)
    const p = spawn("node", [join(dir, "daemon.js")], { env: m.env({ AGENTD_RELEASE_URL: r.origin, AGENTD_RELEASE_CHECK_MS: "200" }), cwd: m.box.work, stdio: ["ignore", "pipe", "pipe"] })
    let out = ""
    p.stdout.on("data", (d: Buffer) => (out += d.toString()))
    try {
      await server.waitHellos(1)
      await sleep(1500)
      expect(out).toContain("updates are off")
      expect(r.downloads).toEqual({})
      expect(r.latestHits).toBe(0)
    } finally {
      p.kill("SIGKILL")
    }
  })
})

describe("rollback", () => {
  test("a new version that crashes at start twice is rolled back and not tried again; another version is", async () => {
    const one = await publish("1.0.0")
    const two = await publish("2.0.0", { head: `throw new Error("broken release");\n` })
    const three = await publish("3.0.0")
    const { s, m, r } = await setup(one)

    r.offer(offerOf(two))
    await waitFor("the rollback", () => m.read("rejected") === "2.0.0", 90_000)
    expect(m.current()).toBe("1.0.0")
    expect(m.read("previous")).toBeUndefined()
    await waitFor("the rollback in the log", () => m.out.includes("version 2.0.0 crashed at start twice: back on 1.0.0"), 10_000)
    expect(m.out).toContain("broken release")
    await waitFor("v1 back online", async () => ((await versions(s)).filter((x) => x === "1.0.0").length >= 2), 60_000)

    // still offered, still refused: no second download, however often the daemon asks
    const asked = r.latestHits
    await waitFor("the daemon to ask again", () => r.latestHits >= asked + 4, 15_000)
    expect(m.current()).toBe("1.0.0")
    expect(r.downloads["agentd-2.0.0.tgz"]).toBe(1)
    expect(m.out).toContain("not updating to 2.0.0")

    // the release server offers something else: that one is installed
    r.offer(offerOf(three))
    await waitFor("hello of 3.0.0", async () => (await versions(s)).includes("3.0.0"), 60_000)
    expect(m.current()).toBe("3.0.0")
    expect(m.read("rejected")).toBeUndefined()
  })
})

describe("restart", () => {
  test("kill -9 of the daemon: the launcher starts it again and it reconnects", async () => {
    const one = await publish("1.0.0")
    const { s, m } = await setup(one)
    const pid = await waitFor("the daemon pid", () => m.daemonPid())
    process.kill(pid, "SIGKILL")
    await s.waitHellos(2)
    expect(m.daemonPid()).not.toBe(pid)
    expect(m.out).toContain("daemon 1.0.0 exited with 137")
  })

  test.each([
    ["an uncaught exception", "throw", "fatal: uncaught exception: Error: thrown from test"],
    ["an unhandled rejection", "reject", "fatal: unhandled rejection: Error: rejected from test"],
  ])("%s exits non-zero and the launcher starts a new process", async (_, kind, logged) => {
    const one = await publish("1.0.0", { failOnce: true })
    const marker = join(mkdtempSync(join(tmpdir(), "mc-marker-")), "failed")
    dirs.push(resolve(marker, ".."))
    const { s, m } = await setup(one, { MC_TEST_FAIL_FILE: marker, MC_TEST_FAIL: kind })
    await waitFor("the failure", () => existsSync(marker) && m.out.includes(logged), 30_000)
    await waitFor("the exit", () => m.out.includes("daemon 1.0.0 exited with 1"), 15_000)
    await s.waitHellos(2)
    expect(m.out).toContain("starting it again in 1 s")
  })
})
