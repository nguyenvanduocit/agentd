// install.sh as the real release server (cmd/agentd-server) renders it, paired with an app (the Go server double), run for
// real with `curl | bash` in a temporary HOME. No service of this machine is touched: --foreground and
// --no-service skip it, and the one default-path test uses the launcher's own restart loop (AGENTD_SERVICE_MANAGER=loop).
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test"
import { execFileSync, spawn, type ChildProcess } from "node:child_process"
import { existsSync, mkdtempSync, symlinkSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { build, ReleaseServer, sleep, TestServer, waitFor } from "./harness.ts"
import { buildVersion, cleanVersions, writeRelease, type Version } from "./versions.ts"

const VERSION = "0.0.0-install"

let one: Version
const dirs: string[] = []
const procs: ChildProcess[] = []
let rig: Rig | undefined

beforeAll(async () => {
  await build()
  one = await buildVersion(VERSION)
}, 300_000)

afterEach(() => {
  for (const p of procs.splice(0)) {
    try {
      if (p.pid !== undefined) process.kill(-p.pid, "SIGKILL")
    } catch {
      /* gone */
    }
  }
  rig?.stop()
  rig = undefined
})

afterAll(() => {
  cleanVersions()
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
})

/** An app (the server double, where the daemon pairs) and a release server (where install.sh and the tarball come from). */
class Rig {
  private constructor(
    readonly app: TestServer,
    readonly releases: ReleaseServer,
  ) {}

  static async start(dist = tmp("mc-dist-")): Promise<Rig> {
    writeRelease(dist, one)
    return Rig.over(dist)
  }

  /** A rig whose release server serves `dist` as it is. */
  static async over(dist: string): Promise<Rig> {
    return new Rig(await TestServer.start("test-token"), await ReleaseServer.start(dist))
  }

  /** The app's origin: what a daemon pairs with. */
  get origin(): string {
    return this.app.origin
  }

  status = () => this.app.status()
  waitHellos = (n = 1) => this.app.waitHellos(n)
  downloads = () => this.releases.downloads()

  stop(): void {
    this.app.stop()
    this.releases.stop()
  }
}

const startServer = async (): Promise<Rig> => (rig = await Rig.start())

function tmp(prefix: string): string {
  const d = mkdtempSync(join(tmpdir(), prefix))
  dirs.push(d)
  return d
}

interface Run {
  out: string
  exited: Promise<number>
  proc: ChildProcess
}

interface RunOptions {
  home?: string
  args?: string[]
  env?: Record<string, string>
  /** A directory of fake tools put first on PATH. */
  fakeBin?: string
}

function runInstall(s: Rig, o: RunOptions = {}): Run & { home: string } {
  const home = o.home ?? tmp("mc-install-home-")
  const script = `set -o pipefail; curl -fsSL "$0/install.sh" | bash -s -- "$@"`
  // A command with a code names the app that gave it, as the app's web UI writes it.
  const args = o.args ?? []
  const withApp = args.includes("--code") && !args.includes("--app") ? ["--app", s.origin, ...args] : args
  const proc = spawn("bash", ["-c", script, s.releases.origin, ...withApp], {
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      PATH: (o.fakeBin === undefined ? "" : o.fakeBin + ":") + (process.env.PATH ?? ""),
      HOME: home,
      XDG_CONFIG_HOME: join(home, "xdg-config"),
      XDG_STATE_HOME: join(home, "xdg-state"),
      TMPDIR: tmp("mc-install-tmp-"),
      // The agents are fakes: no test installs an adapter from the network unless it asks to.
      AGENTD_AGENT_CLAUDE: "true",
      AGENTD_AGENT_CODEX: "true",
      ...o.env,
    },
  })
  procs.push(proc)
  const r = { out: "", proc, home, exited: Promise.resolve(0) }
  proc.stdout.on("data", (d: Buffer) => (r.out += d.toString()))
  proc.stderr.on("data", (d: Buffer) => (r.out += d.toString()))
  r.exited = new Promise((res) => proc.once("exit", (code, sig) => res(code ?? (sig ? 128 : -1))))
  return r
}

/** Ends the run the way Ctrl-C does and waits for it. */
async function stop(r: Run): Promise<void> {
  if (r.proc.pid !== undefined) process.kill(-r.proc.pid, "SIGTERM")
  await Promise.race([r.exited, sleep(15_000)])
}

const mcFiles = (home: string): string[] => (existsSync(join(home, ".agentd")) ? readdirSync(join(home, ".agentd")).sort((a, b) => a.localeCompare(b)) : [])

/** A PATH of symlinks to what install.sh and the daemon need and nothing else: no node, no npx. */
function narrowPath(): string {
  const bin = tmp("mc-narrow-path-")
  const tools = "bash sh curl tar gzip mktemp cut awk rm mv mkdir chmod cat env dirname sed grep head tail tr ps kill sleep hostname id ls shasum sha256sum perl ldd uname readlink cp date basename ln wc".split(" ")
  for (const t of tools) {
    const found = Bun.which(t)
    if (found !== null) symlinkSync(found, join(bin, t))
  }
  return bin
}


describe("install.sh --foreground", () => {
  test("installs, pairs and serves; Node is not downloaded when the machine has one; a second run downloads nothing", async () => {
    const s = await startServer()
    const first = runInstall(s, { args: ["--foreground", "--code", "TESTCODE"], env: { AGENTD_NODE_DIST: "http://127.0.0.1:1/never" } })
    await s.waitHellos(1)
    const home = first.home
    expect(first.out).toContain("using Node.js")
    expect(first.out).not.toContain("installing Node.js")
    expect(first.out).toContain(`downloading daemon ${VERSION}`)
    expect(first.out).toContain("pairing this machine")
    expect(existsSync(join(home, ".agentd", "node"))).toBe(false)
    expect(existsSync(join(home, ".agentd", "daemon", VERSION, "daemon.js"))).toBe(true)
    expect(readFileSync(join(home, ".agentd", "bin", "agentd"), "utf8")).toStartWith("#!/bin/sh")
    expect(readFileSync(join(home, "xdg-config", "agentd", "daemon.json"), "utf8")).toContain(s.origin)
    expect(mcFiles(home).filter((n) => n.startsWith(".install."))).toEqual([])
    expect(s.downloads()[`agentd-${VERSION}.tgz`]).toBe(1)
    expect((await s.status()).hellos?.[0]?.version).toBe(VERSION)
    await stop(first)

    const second = runInstall(s, { home, args: ["--foreground"] })
    await s.waitHellos(2)
    expect(second.out).toContain(`daemon ${VERSION} is already installed`)
    expect(s.downloads()[`agentd-${VERSION}.tgz`]).toBe(1)
    await stop(second)
  })

  test("a wrong code ends with a message and a non-zero exit code, and leaves no half install", async () => {
    const s = await startServer()
    const r = runInstall(s, { args: ["--foreground", "--code", "WRONG-CODE"] })
    expect(await r.exited).not.toBe(0)
    expect(r.out).toContain("refused the pairing code")
    expect(r.out).toContain("pairing failed")
    expect(mcFiles(r.home).filter((n) => n.startsWith(".install."))).toEqual([])
    expect((await s.status()).connected).toBe(false)
  })

  test("a code of the wrong shape is refused before anything is downloaded or written", async () => {
    const s = await startServer()
    const r = runInstall(s, { args: ["--code", "x; touch pwned"] })
    expect(await r.exited).not.toBe(0)
    expect(r.out).toContain("pairing code looks wrong")
    expect(s.downloads()).toEqual({})
    expect(mcFiles(r.home)).toEqual([])
  })

  test("no code and no saved pairing: a message, nothing written", async () => {
    const s = await startServer()
    const r = runInstall(s, { args: ["--foreground"] })
    expect(await r.exited).not.toBe(0)
    expect(r.out).toContain("not paired yet")
    expect(mcFiles(r.home)).toEqual([])
  })

  test("a tarball whose sha256 is not the baked-in one is refused and leaves nothing behind", async () => {
    const bad = await buildVersion(VERSION, { head: "/* changed */\n" })
    // latest.json (and so the script) carries bad.sha256; the tarball served under that name is the one of `one`
    const dist = tmp("mc-bad-")
    writeRelease(dist, bad)
    writeFileSync(join(dist, "releases", `agentd-${VERSION}.tgz`), readFileSync(one.tgz))
    const s = (rig = await Rig.over(dist))
    const r = runInstall(s, { args: ["--foreground", "--code", "TESTCODE"] })
    expect(await r.exited).not.toBe(0)
    expect(r.out).toContain("corrupt or was changed")
    expect(r.out).toContain("Nothing was installed")
    expect(mcFiles(r.home)).toEqual([])
    expect((await s.status()).connected).toBe(false)
  })
})

describe("install.sh refuses what it cannot install", () => {
  test("Windows and musl", async () => {
    const s = await startServer()
    for (const [uname, ldd, want] of [
      [`echo MINGW64_NT-10.0`, "", "use WSL"],
      [`case "$1" in -m) echo x86_64 ;; *) echo Linux ;; esac`, `echo "musl libc (x86_64)"`, "musl"],
    ] as const) {
      const bin = tmp("mc-fakebin-")
      writeFileSync(join(bin, "uname"), `#!/bin/sh\n${uname}\n`, { mode: 0o755 })
      if (ldd !== "") writeFileSync(join(bin, "ldd"), `#!/bin/sh\n${ldd}\n`, { mode: 0o755 })
      // musl is only asked when Node is missing: hide the machine's node and npx
      writeFileSync(join(bin, "node"), "#!/bin/sh\necho v18.0.0\n", { mode: 0o755 })
      const r = runInstall(s, { args: ["--foreground", "--code", "TESTCODE"], fakeBin: bin })
      expect(await r.exited).not.toBe(0)
      expect(r.out).toContain(want)
      expect(mcFiles(r.home).filter((n) => n.startsWith(".install."))).toEqual([])
    }
  })
})

describe("install.sh and the Node.js download", () => {
  test("an archive that does not match SHASUMS256.txt is refused, and nothing is installed", async () => {
    const s = await startServer()
    const mirror = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch(req) {
        const path = new URL(req.url).pathname
        if (path.endsWith("SHASUMS256.txt")) return new Response(`${"0".repeat(64)}  node-v24.21.0-${process.platform}-${process.arch}.tar.gz\n`)
        return new Response("not the archive that was summed")
      },
    })
    try {
      const run = runInstall(s, { args: ["--foreground", "--code", "TESTCODE"], env: { PATH: narrowPath(), AGENTD_NODE_DIST: `http://127.0.0.1:${mirror.port}` } })
      expect(await run.exited).not.toBe(0)
      expect(run.out).toContain("Node.js download is corrupt")
      expect(run.out).toContain("Nothing was installed")
      expect(mcFiles(run.home)).toEqual([])
      expect(s.downloads()).toEqual({})
    } finally {
      await mirror.stop(true)
    }
  })
})

describe("install.sh with a service", () => {
  test("--no-service installs and pairs, and starts nothing", async () => {
    const s = await startServer()
    const r = runInstall(s, { args: ["--no-service", "--code", "TESTCODE"] })
    expect(await r.exited).toBe(0)
    expect(r.out).toContain("no service was installed")
    expect(existsSync(join(r.home, ".agentd", "bin", "agentd"))).toBe(true)
    expect(existsSync(join(r.home, "xdg-config", "agentd", "daemon.json"))).toBe(true)
    await sleep(1000)
    expect((await s.status()).connected).toBe(false)
  })

  test("by default it installs the service; a second install restarts it; uninstall stops it (restart loop standing in for launchd and systemd)", async () => {
    const s = await startServer()
    const env = { AGENTD_SERVICE_MANAGER: "loop" }
    const home = tmp("mc-install-home-")
    const launcher = join(home, ".agentd", "bin", "agentd")
    const first = runInstall(s, { home, args: ["--code", "TESTCODE"], env })
    expect(await first.exited).toBe(0)
    await s.waitHellos(1)
    expect(first.out).toContain("restart loop (no service manager)")
    expect(first.out).toContain("state:   running")
    const pid1 = Number(readFileSync(join(home, ".agentd", "run", "loop.pid"), "utf8"))

    const second = runInstall(s, { home, args: [], env })
    expect(await second.exited).toBe(0)
    await s.waitHellos(2)
    const pid2 = Number(readFileSync(join(home, ".agentd", "run", "loop.pid"), "utf8"))
    expect(pid2).not.toBe(pid1)
    expect(() => process.kill(pid1, 0)).toThrow()

    const down = spawn(launcher, ["service", "uninstall"], { env: { ...process.env, HOME: home, XDG_CONFIG_HOME: join(home, "xdg-config"), ...env } })
    await new Promise((r) => down.once("exit", r))
    await waitFor("the loop to end", () => !existsSync(join(home, ".agentd", "run", "loop.pid")), 15_000)
    expect(() => process.kill(pid2, 0)).toThrow()
    await waitFor("the daemon to disconnect", async () => !(await s.status()).connected, 15_000)
  })
})

// The one run against the real nodejs.org (about 50 MB): AGENTD_TEST_NETWORK=1.
describe.skipIf(process.env.AGENTD_TEST_NETWORK !== "1")("install.sh downloads Node.js", () => {
  test("a machine without node or npx gets the pinned Node.js, checked against SHASUMS256.txt, and the daemon runs on it", async () => {
    const s = await startServer()
    const path = narrowPath()
    expect(Bun.which("node", { PATH: path })).toBeNull()
    expect(Bun.which("npx", { PATH: path })).toBeNull()
    const started = Date.now()
    // Claude is not faked here: its real adapter is installed by the private npm, in the background of the daemon.
    const run = runInstall(s, { args: ["--foreground", "--code", "TESTCODE"], env: { PATH: path, AGENTD_AGENT_CLAUDE: "" } })
    // runInstall puts the machine's PATH after fakeBin; here the narrow path replaces it
    await waitFor("the daemon to connect", async () => (await s.status()).connected, 300_000)
    const home = run.home
    const node = join(home, ".agentd", "node", "bin")
    const version = execFileSync(join(node, "node"), ["--version"], { encoding: "utf8" }).trim()
    const npx = execFileSync(join(node, "npx"), ["--version"], { encoding: "utf8", env: { PATH: `${node}:${path}` } }).trim()
    const size = execFileSync("du", ["-sh", join(home, ".agentd", "node")], { encoding: "utf8" }).split("\t")[0]
    process.stderr.write(`node ${version}, npx ${npx}, ${size} on disk, installed and connected in ${Math.round((Date.now() - started) / 1000)} s\n`)
    expect(version).toBe("v24.21.0")
    expect(run.out).toContain("installing Node.js v24.21.0")
    const claude = () => (async () => {
      const lists = (await s.status()).agentLists ?? []
      return lists[lists.length - 1]?.find((a) => a.name === "claude")
    })()
    expect((await s.status()).hellos?.[0]?.agents.find((a) => a.name === "claude")?.ready).toBe(false)
    await waitFor("the real Claude adapter to install", async () => (await claude())?.ready === true, 240_000)
    process.stderr.write(`claude adapter ready ${Math.round((Date.now() - started) / 1000)} s after the start; log:\n${run.out}\n`)
    expect(mcFiles(home).filter((n) => n.startsWith(".install."))).toEqual([])
    await stop(run)
  }, 400_000)
})
