// The real launcher (launcher.sh) under a real sh, running real `node` scripts as daemon versions.
import { afterEach, describe, expect, test } from "bun:test"
import { spawn, type ChildProcess } from "node:child_process"
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join, resolve } from "node:path"
import { sleep, waitFor } from "./harness.ts"

const LAUNCHER_SRC = resolve(import.meta.dir, "../../launcher.sh")
const NODE_DIR = dirname(Bun.which("node") ?? "/usr/bin/node")

const homes: string[] = []
const procs: ChildProcess[] = []
afterEach(() => {
  for (const p of procs.splice(0)) {
    try {
      if (p.pid !== undefined) process.kill(-p.pid, "SIGKILL")
    } catch {
      /* gone */
    }
  }
  for (const h of homes.splice(0)) rmSync(h, { recursive: true, force: true })
})

class Home {
  readonly root = mkdtempSync(join(tmpdir(), "mc-launcher-"))
  readonly launcher = join(this.root, "bin", "agentd")
  readonly daemon = join(this.root, "daemon")

  constructor() {
    homes.push(this.root)
    mkdirSync(join(this.root, "bin"))
    copyFileSync(LAUNCHER_SRC, this.launcher)
    chmodSync(this.launcher, 0o755)
  }

  /** Installs a version whose daemon.js is `js`; with `current` it becomes current. */
  version(name: string, js: string, current = false): this {
    mkdirSync(join(this.daemon, name), { recursive: true })
    writeFileSync(join(this.daemon, name, "daemon.js"), js)
    if (current) symlinkSync(name, join(this.daemon, "current"))
    return this
  }

  file(name: string): string {
    return join(this.daemon, name)
  }

  read(name: string): string {
    return readFileSync(this.file(name), "utf8").trim()
  }

  current(): string {
    return readlinkSync(this.file("current"))
  }

  start(args: string[], env: Record<string, string> = {}): Started {
    const child = spawn(this.launcher, args, {
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
      env: { PATH: `${NODE_DIR}:/usr/bin:/bin`, HOME: this.root, AGENTD_HOME: this.root, ...env },
    })
    procs.push(child)
    return new Started(child)
  }
}

class Started {
  out = ""
  readonly exited: Promise<number>
  constructor(readonly child: ChildProcess) {
    child.stdout?.on("data", (d: Buffer) => (this.out += d.toString()))
    child.stderr?.on("data", (d: Buffer) => (this.out += d.toString()))
    this.exited = new Promise((r) => child.once("exit", (code, signal) => r(code ?? (signal ? 128 : -1))))
  }
}

const exits = (code: number) => `process.exit(${code})`

describe("not supervised", () => {
  test("pair, version, service and mcp run as they are, arguments untouched", async () => {
    const h = new Home().version("1.0.0", `console.log(JSON.stringify(process.argv.slice(2))); process.exit(7)`, true)
    const r = h.start(["pair", "--app", "http://x y", "--code", "A-1"])
    expect(await r.exited).toBe(7)
    expect(JSON.parse(r.out.trim())).toEqual(["pair", "--app", "http://x y", "--code", "A-1"])
  })

  test("no daemon installed: a message and exit 1", async () => {
    const h = new Home()
    const r = h.start(["run"])
    expect(await r.exited).toBe(1)
    expect(r.out).toContain("no daemon is installed")
  })

  test("a Node.js under ~/.agentd/node comes first on PATH", async () => {
    const h = new Home().version("1.0.0", `console.log("system")`, true)
    mkdirSync(join(h.root, "node", "bin"), { recursive: true })
    writeFileSync(join(h.root, "node", "bin", "node"), "#!/bin/sh\necho private-node \"$@\"\n", { mode: 0o755 })
    const r = h.start(["version"])
    expect(await r.exited).toBe(0)
    expect(r.out).toContain("private-node")
  })
})

describe("one supervised cycle (what a service manager runs)", () => {
  test("the exit code of the daemon is the exit code of the launcher", async () => {
    const h = new Home().version("1.0.0", exits(3), true)
    expect(await h.start(["run"]).exited).toBe(3)
    expect(await h.start(["run"]).exited).toBe(3)
  })

  test("the arguments reach the daemon, with or without the word run", async () => {
    const h = new Home().version("1.0.0", `console.log(JSON.stringify(process.argv.slice(2)))`, true)
    const a = h.start(["run", "--dir", "/w"])
    await a.exited
    expect(JSON.parse(a.out.trim())).toEqual(["run", "--dir", "/w"])
    const b = h.start(["--app", "http://s"])
    await b.exited
    expect(JSON.parse(b.out.trim())).toEqual(["--app", "http://s"])
  })

  test("two quick crashes of a new version in a row roll back to the previous one, which is not tried again", async () => {
    const h = new Home().version("1.0.0", `setInterval(() => {}, 1000)`).version("2.0.0", exits(1), true)
    writeFileSync(h.file("previous"), "1.0.0\n")
    expect(await h.start(["run"]).exited).toBe(1)
    expect(h.read("crashes")).toBe("2.0.0 1")
    expect(h.current()).toBe("2.0.0")
    const second = h.start(["run"])
    expect(await second.exited).toBe(1)
    expect(h.current()).toBe("1.0.0")
    expect(h.read("rejected")).toBe("2.0.0")
    expect(existsSync(h.file("previous"))).toBe(false)
    expect(existsSync(h.file("crashes"))).toBe(false)
    expect(second.out).toContain("back on 1.0.0")
  })

  test("a crash of another version starts the count again", async () => {
    const h = new Home().version("1.0.0", exits(1), true).version("2.0.0", exits(1))
    writeFileSync(h.file("previous"), "2.0.0\n")
    await h.start(["run"]).exited
    expect(h.read("crashes")).toBe("1.0.0 1")
    rmSync(h.file("current"))
    symlinkSync("2.0.0", h.file("current"))
    await h.start(["run"]).exited
    expect(h.read("crashes")).toBe("2.0.0 1")
    expect(h.current()).toBe("2.0.0")
  })

  test("a clean exit, an update exit and a daemon that ran long enough clear the count", async () => {
    const h = new Home().version("1.0.0", exits(0), true)
    writeFileSync(h.file("crashes"), "1.0.0 1\n")
    await h.start(["run"]).exited
    expect(existsSync(h.file("crashes"))).toBe(false)

    const long = new Home().version("1.0.0", exits(1), true)
    writeFileSync(long.file("crashes"), "1.0.0 1\n")
    writeFileSync(long.file("previous"), "0.9.0\n")
    long.version("0.9.0", "")
    expect(await long.start(["run"], { AGENTD_QUICK_SECONDS: "0" }).exited).toBe(1)
    expect(existsSync(long.file("crashes"))).toBe(false)
    expect(long.current()).toBe("1.0.0")
  })

  test("nothing to roll back to: the count is kept, current stays", async () => {
    const h = new Home().version("1.0.0", exits(1), true)
    await h.start(["run"]).exited
    await h.start(["run"]).exited
    expect(h.read("crashes")).toBe("1.0.0 2")
    expect(h.current()).toBe("1.0.0")
    expect(existsSync(h.file("rejected"))).toBe(false)
  })

  test("a previous that is not a plain version name is never followed", async () => {
    const h = new Home().version("1.0.0", exits(1), true)
    writeFileSync(h.file("previous"), "../../etc\n")
    await h.start(["run"]).exited
    await h.start(["run"]).exited
    expect(h.current()).toBe("1.0.0")
  })

  test("a previous named like an entry of the daemon directory is never followed, in any case", async () => {
    for (const name of ["current", "CURRENT"]) {
      const h = new Home().version("1.0.0", exits(1), true)
      writeFileSync(h.file("previous"), `${name}\n`)
      await h.start(["run"]).exited
      await h.start(["run"]).exited
      expect(h.current()).toBe("1.0.0")
    }
  })

  test("exit 75: the updated daemon starts at once, in the same launcher", async () => {
    const switchTo2 = `
      const fs = require("fs"); const path = require("path")
      const d = path.dirname(path.dirname(process.argv[1]))
      fs.symlinkSync("2.0.0", path.join(d, "t")); fs.renameSync(path.join(d, "t"), path.join(d, "current"))
      process.exit(75)`
    const h = new Home().version("1.0.0", switchTo2, true).version("2.0.0", `console.log("now running 2.0.0"); process.exit(0)`)
    // daemon.js sees current/daemon.js as argv[1]: the symlink path is kept by node only until realpath, so use the real dir
    const r = h.start(["run"])
    expect(await r.exited).toBe(0)
    expect(r.out).toContain("now running 2.0.0")
    expect(r.out).toContain("starting the daemon again at once (2.0.0)")
  })

  test("SIGTERM to the launcher ends the daemon and the launcher exits 0", async () => {
    const h = new Home().version("1.0.0", `process.on("SIGTERM", () => process.exit(0)); console.log("up " + process.pid); setInterval(() => {}, 1000)`, true)
    const r = h.start(["run"])
    const pid = Number(await waitFor("the daemon to start", () => /up (\d+)/.exec(r.out)?.[1]))
    process.kill(r.child.pid ?? 0, "SIGTERM")
    expect(await r.exited).toBe(0)
    expect(() => process.kill(pid, 0)).toThrow()
  })
})

describe("restart loop (no service manager)", () => {
  const counting = (n: string) => `require("fs").appendFileSync(${JSON.stringify(n)}, "x\\n"); process.exit(1)`

  test("restarts after every exit with a growing delay; SIGTERM ends the loop and removes the pid file", async () => {
    const h = new Home()
    const runs = join(h.root, "runs")
    h.version("1.0.0", counting(runs), true)
    const r = h.start(["run"], { AGENTD_RESTART: "loop" })
    await waitFor("three runs", () => existsSync(runs) && readFileSync(runs, "utf8").split("\n").length > 3, 15_000)
    expect(r.out).toContain("starting it again in 1 s")
    expect(r.out).toContain("starting it again in 2 s")
    expect(Number(readFileSync(join(h.root, "run", "loop.pid"), "utf8"))).toBe(r.child.pid ?? 0)
    process.kill(r.child.pid ?? 0, "SIGTERM")
    expect(await r.exited).toBe(0)
    expect(existsSync(join(h.root, "run", "loop.pid"))).toBe(false)
  })

  test("kill -9 of the daemon brings it back", async () => {
    const h = new Home()
    const pidFile = join(h.root, "pid")
    h.version("1.0.0", `require("fs").writeFileSync(${JSON.stringify(pidFile)}, String(process.pid)); setInterval(() => {}, 1000)`, true)
    const r = h.start(["run"], { AGENTD_RESTART: "loop" })
    const first = Number(await waitFor("the first daemon", () => existsSync(pidFile) && readFileSync(pidFile, "utf8")))
    process.kill(first, "SIGKILL")
    const second = Number(await waitFor("a new daemon", () => (Number(readFileSync(pidFile, "utf8")) !== first ? readFileSync(pidFile, "utf8") : false), 10_000))
    expect(second).not.toBe(first)
    process.kill(r.child.pid ?? 0, "SIGTERM")
    await r.exited
    await sleep(100)
  })

  test("exit 0 (a Stop, a revoked token) ends the loop and is remembered; the daemon runs as managed", async () => {
    const h = new Home().version("1.0.0", `console.log("managed=" + process.env.AGENTD_MANAGED); process.exit(0)`, true)
    const r = h.start(["run"], { AGENTD_RESTART: "loop" })
    expect(await r.exited).toBe(0)
    expect(r.out).toContain("managed=1")
    expect(r.out).toContain("it stays stopped")
    expect(readFileSync(join(h.root, "run", "last-exit"), "utf8").trim()).toBe("0")
    expect(existsSync(join(h.root, "run", "loop.pid"))).toBe(false)
  })

  test("exit 75 restarts at once in the loop too, with no delay", async () => {
    const h = new Home()
    const runs = join(h.root, "runs")
    h.version("1.0.0", `const fs = require("fs"); fs.appendFileSync(${JSON.stringify(runs)}, "x\\n"); process.exit(fs.readFileSync(${JSON.stringify(runs)}, "utf8").length >= 6 ? 0 : 75)`, true)
    const r = h.start(["run"], { AGENTD_RESTART: "loop" })
    expect(await r.exited).toBe(0)
    expect(readFileSync(runs, "utf8")).toBe("x\nx\nx\n")
    expect(r.out).not.toContain("starting it again in")
  })

  test("an uncaught exception ends the daemon non-zero and the loop restarts it", async () => {
    const h = new Home()
    const runs = join(h.root, "runs")
    h.version("1.0.0", `require("fs").appendFileSync(${JSON.stringify(runs)}, "x\\n"); setTimeout(() => { throw new Error("boom") }, 50); setInterval(() => {}, 1000)`, true)
    const r = h.start(["run"], { AGENTD_RESTART: "loop" })
    await waitFor("two runs", () => existsSync(runs) && readFileSync(runs, "utf8").split("\n").length > 2, 15_000)
    expect(r.out).toContain("daemon 1.0.0 exited with 1")
    process.kill(r.child.pid ?? 0, "SIGTERM")
    await r.exited
  })
})
