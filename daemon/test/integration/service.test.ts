// The service files, checked by the tools that read them, and (opt in) one real launchd run on this machine.
import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { execFileSync, spawnSync } from "node:child_process"
import { randomBytes } from "node:crypto"
import { existsSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { plist, systemdUnit } from "../../src/core/service.ts"
import { build, sh, sleep, TestServer, waitFor } from "./harness.ts"
import { buildVersion, cleanVersions, Machine } from "./versions.ts"

const darwin = process.platform === "darwin"

describe("generated service files", () => {
  test.skipIf(!darwin)("the plist passes plutil -lint, with and without environment, and with characters XML escapes", () => {
    const dir = mkdtempSync(join(tmpdir(), "mc-plist-"))
    try {
      const envs: Record<string, string>[] = [{}, { PATH: "/a&b:/usr/bin", AGENTD_X: "<\"'>" }]
      for (const env of envs) {
        const file = join(dir, "x.plist")
        writeFileSync(file, plist({ label: "com.agentd.daemon", launcher: "/h/.agentd/bin/agentd", workDir: "/Users/a & b", logFile: "/h/.agentd/logs/daemon.log", env }))
        expect(execFileSync("plutil", ["-lint", file], { encoding: "utf8" })).toContain("OK")
      }
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  test.skipIf(Bun.which("systemd-analyze") === null)("the unit passes systemd-analyze verify", () => {
    const dir = mkdtempSync(join(tmpdir(), "mc-unit-"))
    try {
      const file = join(dir, "agentd.service")
      writeFileSync(file, systemdUnit({ launcher: "/bin/true", workDir: undefined, logFile: "/tmp/daemon.log", env: { PATH: "/usr/bin" } }))
      const r = spawnSync("systemd-analyze", ["--user", "verify", file], { encoding: "utf8" })
      expect(r.stderr).toBe("")
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

// One real launchd run: a service with a unique label, its plist in a temporary directory, removed at the end.
// It registers a job in the user's launchd domain for a minute, so it runs only when asked: AGENTD_TEST_LAUNCHD=1.
describe.skipIf(!darwin || process.env.AGENTD_TEST_LAUNCHD !== "1")("launchd", () => {
  let server: TestServer | undefined
  let machine: Machine | undefined
  const uid = process.getuid?.() ?? 0
  const label = `com.agentd.daemon.test.${randomBytes(4).toString("hex")}`
  let plistDir = ""
  const plistFile = () => join(plistDir, `${label}.plist`)

  beforeAll(async () => {
    await build()
  }, 300_000)

  afterEach(async () => {
    // Whatever happened, the job leaves launchd and the files leave the disk.
    spawnSync("launchctl", ["bootout", `gui/${uid}/${label}`])
    rmSync(plistFile(), { force: true })
    await machine?.stop()
    server?.stop()
    cleanVersions()
    if (plistDir !== "") rmSync(plistDir, { recursive: true, force: true })
  })

  const listedFor = (l: string) => spawnSync("sh", ["-c", `launchctl list | grep ${l} || true`], { encoding: "utf8" }).stdout.trim()
  const listed = () => spawnSync("sh", ["-c", "launchctl list | grep agentd || true"], { encoding: "utf8" }).stdout.trim()

  test("the service runs the daemon, kill -9 brings it back, and nothing is left", async () => {
    plistDir = mkdtempSync(join(tmpdir(), "mc-launchd-"))
    const before = listed()
    const one = await buildVersion("0.0.0-launchd")
    server = await TestServer.start()
    const m = (machine = new Machine())
    await m.install(one)
    m.pair(server.origin)
    const env = {
      ...m.env({ AGENTD_SERVICE_LABEL: label, AGENTD_SERVICE_DIR: plistDir, AGENTD_AGENT_CLAUDE: sh("sleep 600") }),
    }
    const launcher = join(m.home, "bin", "agentd")
    const install = spawnSync(launcher, ["service", "install"], { env, encoding: "utf8" })
    expect(install.status).toBe(0)
    expect(install.stdout).toContain(`launchd, ${label}`)
    expect(existsSync(plistFile())).toBe(true)

    // 1. launchd runs the daemon: it connects to the server, in the user's home and not in `/`
    await server.waitHellos(1)
    const logFile = join(m.home, "logs", "daemon.log")
    expect(readFileSync(logFile, "utf8")).toContain(`serving agents in ${realpathSync(m.box.root)}`)
    expect(readFileSync(plistFile(), "utf8")).toContain(`<key>WorkingDirectory</key>\n  <string>${m.box.root}</string>`)
    const daemonPid = () => {
      const all = [...readFileSync(logFile, "utf8").matchAll(/\(pid (\d+)\) serving agents/g)]
      return Number(all[all.length - 1]?.[1])
    }
    const status = () => spawnSync(launcher, ["service", "status"], { env, encoding: "utf8" })
    expect(status().stdout).toContain("state:   running")
    const jobPid = Number(/pid (\d+)\)/.exec(status().stdout)?.[1])
    const firstDaemon = daemonPid()
    expect(firstDaemon).toBeGreaterThan(1)
    expect(jobPid).toBeGreaterThan(1)

    // 2. kill -9 the daemon: the launcher exits, launchd starts it again within the throttle interval (5 s)
    const killedAt = Date.now()
    process.kill(firstDaemon, "SIGKILL")
    await waitFor("a new daemon", () => daemonPid() !== firstDaemon, 40_000)
    await server.waitHellos(2)
    const back = Date.now() - killedAt
    expect(back).toBeLessThan(30_000)
    process.stderr.write(`daemon killed with -9; launchd had the new daemon connected after ${back} ms\n`)
    const secondDaemon = daemonPid()

    // 3. kill -9 the job's own process (the launcher): launchd ends its group and starts it again
    const job2 = Number(/pid (\d+)\)/.exec(status().stdout)?.[1])
    process.kill(job2, "SIGKILL")
    await waitFor("another daemon", () => daemonPid() !== secondDaemon, 40_000)
    await sleep(500)
    expect(() => process.kill(secondDaemon, 0)).toThrow()

    // 4. Stop from the server: the daemon exits 0, launchd leaves the job stopped (KeepAlive only after a failure)
    const hellos = async () => ((await server?.status())?.hellos ?? []).length
    const helloCount = await hellos()
    expect(await server.stopDaemon()).toEqual({ ok: true })
    await waitFor("the daemon to disconnect", async () => !(await server?.status())?.connected, 20_000)
    await sleep(8000) // longer than the throttle interval
    expect(await hellos()).toBe(helloCount)
    expect((await server.status()).connected).toBe(false)
    const stopped = status()
    expect(stopped.stdout).toContain("stopped (it exited 0")
    expect(stopped.status).toBe(1)
    expect(listedFor(label)).toMatch(/^-\s+0\s/)
    process.stderr.write(`after Stop: ${listedFor(label)}\n${stopped.stdout}`)

    // 5. service start brings it back; service restart gives a new process
    expect(spawnSync(launcher, ["service", "start"], { env, encoding: "utf8" }).status).toBe(0)
    await waitFor("hello after start", async () => (await hellos()) > helloCount, 40_000)
    const afterStart = daemonPid()
    expect(spawnSync(launcher, ["service", "restart"], { env, encoding: "utf8" }).status).toBe(0)
    await waitFor("a new daemon after restart", () => daemonPid() !== afterStart, 40_000)
    await waitFor("connected", async () => (await server?.status())?.connected === true, 20_000)

    // 6. a revoked token: the daemon exits 0 and launchd leaves it stopped
    await server.revoke()
    await sleep(10_000)
    expect(listedFor(label)).toMatch(/^-\s+0\s/)
    expect((await server.status()).rejects).toBe(1)

    // 7. uninstall removes the job and the file
    const down = spawnSync(launcher, ["service", "uninstall"], { env, encoding: "utf8" })
    expect(down.status).toBe(0)
    expect(existsSync(plistFile())).toBe(false)
    expect(spawnSync("launchctl", ["print", `gui/${uid}/${label}`]).status).not.toBe(0)
    await sleep(500)
    expect(listed()).toBe(before)
  }, 400_000)
})
