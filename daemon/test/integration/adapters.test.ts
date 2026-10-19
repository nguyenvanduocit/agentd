// The adapter store: a fake adapter packed as a local tarball stands in for @agentclientprotocol/*-acp, installed by the
// real npm into ~/.agentd/adapters in the background of a real daemon.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test"
import { execFileSync } from "node:child_process"
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { AdapterStore } from "../../src/adapters.ts"
import { adapterDir, adapterKey, ADAPTERS, MARKER } from "../../src/core/adapters.ts"
import { layoutOf } from "../../src/core/layout.ts"
import { build, Rig, sleep, TestServer, waitFor } from "./harness.ts"
import { buildVersion, cleanVersions, Machine } from "./versions.ts"

const dirs: string[] = []
let rig: Rig | undefined
let machine: Machine | undefined

beforeAll(async () => {
  await build()
}, 300_000)

afterEach(async () => {
  await rig?.stop()
  await machine?.stop()
  rig = machine = undefined
})

afterAll(() => {
  cleanVersions()
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
})

function tmp(prefix: string): string {
  const d = mkdtempSync(join(tmpdir(), prefix))
  dirs.push(d)
  return d
}

/** A package named like the real adapter (npm installs it into node_modules/<name>), packed with `tar`. */
function fakeAdapter(name: string, o: { installSeconds?: number; pidFile?: boolean } = {}): string {
  const root = tmp("mc-fake-adapter-")
  const pkg = join(root, "package")
  mkdirSync(join(pkg, "dist"), { recursive: true })
  const pid = o.pidFile === true ? `require('fs').writeFileSync(process.env.MC_TEST_PIDFILE, String(process.pid));` : ""
  const scripts = o.installSeconds === undefined ? {} : { scripts: { postinstall: `node -e "${pid} setTimeout(() => {}, ${o.installSeconds * 1000})"` } }
  writeFileSync(join(pkg, "package.json"), JSON.stringify({ name, version: "9.9.9", bin: { adapter: "dist/index.js" }, ...scripts }))
  writeFileSync(join(pkg, "dist", "index.js"), `console.log("fake adapter up"); process.stdin.on("data", (d) => process.stdout.write(d)); setInterval(() => {}, 1000)\n`)
  const tgz = join(root, "fake.tgz")
  execFileSync("tar", ["-czf", tgz, "-C", root, "package"])
  return `file:${tgz}`
}

const claude = ADAPTERS.claude

const agentsOf = async (s: TestServer) => ((await s.status()).hellos?.[0]?.agents ?? []).find((a) => a.name === "claude")
const lastList = async (s: TestServer) => {
  const lists = (await s.status()).agentLists ?? []
  return lists[lists.length - 1]?.find((a) => a.name === "claude")
}

function adapterEnv(spec: string): Record<string, string> {
  return { AGENTD_ADAPTER_SPEC_CLAUDE: spec, AGENTD_AGENT_CLAUDE: "" }
}

describe("a daemon with its adapter installed in the background", () => {
  test("hello says installing, two opens meanwhile are refused without spawning, then agents says ready and open runs the installed adapter", async () => {
    const spec = fakeAdapter(claude.pkg, { installSeconds: 3 })
    rig = await Rig.start({ env: adapterEnv(spec) })
    const s = rig.server
    expect(await agentsOf(s)).toEqual({ name: "claude", ready: false, reason: "installing the Claude adapter (first run only, this can take several minutes)" })

    const [a, b] = await Promise.all([s.open("one", "claude"), s.open("two", "claude")])
    expect(a).toEqual({ error: expect.stringContaining("the claude adapter is still installing on this machine") })
    expect(b).toEqual({ error: expect.stringContaining("the claude adapter is still installing on this machine") })
    expect(await s.frames()).toEqual([])

    await waitFor("agents: claude ready", async () => (await lastList(s))?.ready === true, 60_000)
    expect(await lastList(s)).toEqual({ name: "claude", ready: true, reason: "" })

    const opened = await rig.open("three", "claude")
    expect(opened.pid).toBeGreaterThan(0)
    const first = await rig.frame("three", "the adapter's first line", (f) => f.kind === "out")
    expect(first.data).toBe("fake adapter up")

    // on disk: one complete install, a marker, nothing partial
    const home = join(rig.box.root, "mc")
    const key = adapterKey(claude, adapterEnv(spec))
    const dir = adapterDir(layoutOf(home), "claude", key)
    expect(existsSync(join(dir, MARKER))).toBe(true)
    expect(readdirSync(join(home, "adapters", "claude"))).toEqual([key])
  })

  test("an install that was cut short leaves nothing that looks installed; the next start installs clean", async () => {
    const spec = fakeAdapter(claude.pkg, { installSeconds: 4 })
    rig = await Rig.start({ env: adapterEnv(spec) })
    const home = join(rig.box.root, "mc")
    const agentDir = join(home, "adapters", "claude")
    const key = adapterKey(claude, adapterEnv(spec))
    await waitFor("npm to be working in a partial directory", () => existsSync(agentDir) && readdirSync(agentDir).some((n) => n.startsWith(".partial")))
    rig.daemon.kill("SIGKILL")
    await rig.daemon.exited
    expect(existsSync(join(agentDir, key))).toBe(false)

    await rig.startDaemon({ env: adapterEnv(spec) })
    await rig.server.waitHellos(2)
    await waitFor("agents: claude ready", async () => (await lastList(rig!.server))?.ready === true, 60_000)
    expect(existsSync(join(agentDir, key, MARKER))).toBe(true)
    const opened = await rig.open("again", "claude")
    expect(opened.pid).toBeGreaterThan(0)
    expect((await rig.frame("again", "first line", (f) => f.kind === "out")).data).toBe("fake adapter up")
  })

  test("a directory with the final name and no marker (the old npx failure) is removed and installed again", async () => {
    const spec = fakeAdapter(claude.pkg)
    rig = await Rig.start({ env: adapterEnv(spec) })
    await rig.daemon.stop()
    const home = join(rig.box.root, "mc")
    const key = adapterKey(claude, adapterEnv(spec))
    const half = join(home, "adapters", "claude", key)
    mkdirSync(join(half, "node_modules", "junk"), { recursive: true })
    await rig.startDaemon({ env: adapterEnv(spec) })
    await waitFor("agents: claude ready", async () => (await lastList(rig!.server))?.ready === true, 60_000)
    expect(existsSync(join(half, "node_modules", "junk"))).toBe(false)
    expect(existsSync(join(half, MARKER))).toBe(true)
  })

  test("an install that fails says why in reason, the daemon stays up, open says why", async () => {
    rig = await Rig.start({ env: adapterEnv("file:/definitely/not/here.tgz") })
    const s = rig.server
    await waitFor("agents: a reason", async () => (await lastList(s))?.reason.startsWith("cannot install the Claude adapter:") === true, 60_000)
    const info = await lastList(s)
    expect(info?.ready).toBe(false)
    expect(info?.reason).toMatch(/retrying in \d+ s$/)
    expect(rig.daemon.proc.exitCode).toBeNull()
    const refused = await s.open("x", "claude")
    expect(refused).toEqual({ error: expect.stringContaining("agent claude is not ready: cannot install the Claude adapter:") })
    expect(await s.frames()).toEqual([])
  })

  test("a seam command makes the agent ready at once and installs nothing", async () => {
    rig = await Rig.start({ claude: "sh -c 'sleep 60'" })
    expect(await agentsOf(rig.server)).toEqual({ name: "claude", ready: true, reason: "" })
    await sleep(500)
    expect(existsSync(join(rig.box.root, "mc", "adapters"))).toBe(false)
  })
})

/** Whether a process is alive. */
function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

describe("an install goes down with the daemon", () => {
  /** A daemon whose adapter's postinstall (a child of npm) runs for 60 s and says its pid. */
  async function installing() {
    const spec = fakeAdapter(claude.pkg, { installSeconds: 60, pidFile: true })
    const pidFile = join(tmp("mc-pid-"), "pid")
    const env = { ...adapterEnv(spec), MC_TEST_PIDFILE: pidFile }
    const pid = async () => Number(await waitFor("the install script to run", () => existsSync(pidFile) && readFileSync(pidFile, "utf8")))
    return { spec, env, pid }
  }

  test("SIGTERM (the service's stop): npm and its children are gone within 3 s, the partial directory is not an install", async () => {
    const { spec, env, pid } = await installing()
    rig = await Rig.start({ env })
    const script = await pid()
    expect(alive(script)).toBe(true)
    await rig.daemon.stop()
    await waitFor("npm's child to be gone", () => !alive(script), 3000)
    const agentDir = join(rig.box.root, "mc", "adapters", "claude")
    expect(existsSync(join(agentDir, adapterKey(claude, adapterEnv(spec))))).toBe(false)
    expect(existsSync(agentDir) ? readdirSync(agentDir).filter((n) => n.startsWith(".partial")) : []).toEqual([])
  })

  test("Stop and Restart from the server end it too", async () => {
    const { env, pid } = await installing()
    rig = await Rig.start({ env })
    const script = await pid()
    await rig.server.stopDaemon()
    expect(await rig.daemon.exited).toBe(0)
    await waitFor("npm's child to be gone", () => !alive(script), 3000)
    expect(alive(script)).toBe(false)
  })

  test("a fatal error ends it as well", async () => {
    const { env, pid } = await installing()
    const failFile = join(tmp("mc-fail-"), "failed")
    const v = await buildVersion("1.0.0", { failOnce: true }, tmp("mc-fatal-version-"))
    const m = (machine = new Machine())
    await m.install(v)
    const server = await TestServer.start()
    m.pair(server.origin)
    m.startLoop({ ...env, MC_TEST_FAIL_FILE: failFile, MC_TEST_FAIL: "throw", MC_TEST_FAIL_WHEN: env.MC_TEST_PIDFILE })
    try {
      const script = await pid()
      await waitFor("the fatal error", () => m.out.includes("fatal: uncaught exception"), 30_000)
      await waitFor("npm's child to be gone", () => !alive(script), 3000)
      expect(alive(script)).toBe(false)
    } finally {
      server.stop()
    }
  })
})

describe("AGENTD_ADAPTERS=off", () => {
  test("an agent without a seam says the install is disabled; nothing is installed, npm does not run", async () => {
    rig = await Rig.start({ env: { AGENTD_ADAPTERS: "off", AGENTD_AGENT_CLAUDE: "", AGENTD_ADAPTER_SPEC_CLAUDE: fakeAdapter(claude.pkg, { installSeconds: 30 }) } })
    expect(await agentsOf(rig.server)).toEqual({ name: "claude", ready: false, reason: "adapter install is disabled" })
    expect(await rig.server.open("x", "claude")).toEqual({ error: expect.stringContaining("agent claude is not ready: adapter install is disabled") })
    await sleep(500)
    expect(existsSync(join(rig.box.root, "mc", "adapters"))).toBe(false)
  })

  test("a seam still wins", async () => {
    rig = await Rig.start({ claude: "sh -c 'sleep 60'", env: { AGENTD_ADAPTERS: "off" } })
    expect(await agentsOf(rig.server)).toEqual({ name: "claude", ready: true, reason: "" })
  })
})

describe("AdapterStore retries", () => {
  test("a failed install is tried again with backoff and succeeds when the package appears", async () => {
    const home = tmp("mc-store-")
    const layout = layoutOf(home)
    const missing = join(tmp("mc-store-pkg-"), "late.tgz")
    const env = { PATH: process.env.PATH ?? "", AGENTD_ADAPTER_SPEC_CLAUDE: `file:${missing}`, AGENTD_AGENT_CODEX: "true" }
    const changes: string[] = []
    const store = new AdapterStore({ layout, env, retry: { baseMs: 400, maxMs: 400, jitter: 0 } })
    store.onChange(() => changes.push(store.state("claude")?.kind ?? "none"))
    store.start()
    try {
      expect(store.state("claude")).toEqual({ kind: "installing" })
      expect(store.state("codex")).toBeUndefined()
      await waitFor("a failure", () => store.state("claude")?.kind === "failed", 30_000)
      const packed = fakeAdapter(claude.pkg).slice("file:".length)
      execFileSync("cp", [packed, missing])
      await waitFor("the retry to succeed", () => store.state("claude")?.kind === "ready", 60_000)
      expect(changes[0]).toBe("failed")
      expect(changes[changes.length - 1]).toBe("ready")
    } finally {
      store.stop()
    }
  })

  test("a complete install on disk is ready at start, without npm", () => {
    const home = tmp("mc-store-done-")
    const layout = layoutOf(home)
    const env = { PATH: "/nonexistent", AGENTD_AGENT_CODEX: "true" }
    const dir = adapterDir(layout, "claude", adapterKey(claude, env))
    mkdirSync(join(dir, "node_modules", "x"), { recursive: true })
    writeFileSync(join(dir, "node_modules", "x", "run.js"), "")
    writeFileSync(join(dir, MARKER), JSON.stringify({ spec: "s", bin: "node_modules/x/run.js" }))
    const store = new AdapterStore({ layout, env })
    store.start()
    expect(store.state("claude")).toEqual({ kind: "ready", bin: join(dir, "node_modules", "x", "run.js") })
    store.stop()
  })
})
