// Daemon versions for the update tests: real bundles, packed like scripts/build-daemon.sh packs them, installed the
// way install.sh installs them, and run under the real launcher with the restart loop standing in for a service.
import { execFile, spawn, type ChildProcess } from "node:child_process"
import { createHash } from "node:crypto"
import {
  chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync,
} from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join, resolve } from "node:path"
import { promisify } from "node:util"
import { tarballPath, type Offer } from "../../src/core/update.ts"
import { bundleArgs, Sandbox, testSign, TOKEN } from "./harness.ts"

const run = promisify(execFile)
const ROOT = resolve(import.meta.dir, "../../..")
const LAUNCHER = join(ROOT, "daemon", "launcher.sh")
const NODE_DIR = dirname(Bun.which("node") ?? "/usr/bin/node")

export interface Version {
  version: string
  /** The tarball's directory (the server double's -dir) and path. */
  dir: string
  tgz: string
  sha256: string
}

/** The offer (latest.json) that serves `v`, with `o` in place of its sha256 or signature. The release key of this run signs the sha256 it carries. */
export const offerOf = (v: Version, o: Partial<{ sha256: string; signature: string }> = {}): Offer => {
  const sha256 = o.sha256 ?? v.sha256
  return { version: v.version, url: tarballPath(v.version), sha256, signature: o.signature ?? testSign(v.version, sha256) }
}

/** Publishes `v` as the latest release in a release server's dist directory: the tarball and latest.json. */
export function writeRelease(dist: string, v: Version, o: Parameters<typeof offerOf>[1] = {}): void {
  mkdirSync(join(dist, "releases"), { recursive: true })
  copyFileSync(v.tgz, join(dist, "releases", `agentd-${v.version}.tgz`))
  writeFileSync(join(dist, "releases", "latest.json"), JSON.stringify(offerOf(v, o)))
}

/**
 * A release server a test steers: what latest.json says, which tarballs it holds, how many times each was downloaded and how
 * long a download is held back. (The real server, cmd/agentd-server, is exercised by the install tests.)
 */
export class ReleaseStub {
  private readonly files = new Map<string, Buffer>()
  private latest: Offer | undefined
  readonly downloads: Record<string, number> = {}
  /** How many times latest.json was asked for. */
  latestHits = 0
  /** Holds every tarball download back by this many ms. */
  delayMs = 0
  private readonly server: ReturnType<typeof Bun.serve>

  constructor() {
    this.server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (req) => {
        const path = new URL(req.url).pathname
        if (path === "/releases/latest.json") {
          this.latestHits++
          return this.latest === undefined ? new Response("no release", { status: 503 }) : Response.json(this.latest)
        }
        const bytes = this.files.get(path)
        if (bytes === undefined) return new Response("nope", { status: 404 })
        const name = path.slice(path.lastIndexOf("/") + 1)
        this.downloads[name] = (this.downloads[name] ?? 0) + 1
        if (this.delayMs > 0) await new Promise((r) => setTimeout(r, this.delayMs))
        return new Response(bytes)
      },
    })
  }

  get origin(): string {
    return `http://127.0.0.1:${this.server.port}`
  }

  /** Holds the tarball of `v`, whether or not latest.json offers it. */
  add(v: Version): void {
    this.files.set(tarballPath(v.version), readFileSync(v.tgz))
  }

  /** Makes `offer` the latest release (undefined: none). */
  offer(offer: Offer | undefined): void {
    this.latest = offer
  }

  stop(): void {
    void this.server.stop(true)
  }
}

const FAIL_ONCE = `;(function(){var f=process.env.MC_TEST_FAIL_FILE;if(!f)return;var fs=require("fs");if(fs.existsSync(f))return;var kind=process.env.MC_TEST_FAIL||"throw";fs.writeFileSync(f,"1");function fire(){if(kind==="reject"){Promise.reject(new Error("rejected from test"))}else{throw new Error("thrown from test")}}var when=process.env.MC_TEST_FAIL_WHEN;if(when){var t=setInterval(function(){if(fs.existsSync(when)){clearInterval(t);fire()}},100)}else{setTimeout(fire,1500)}})();\n`

export interface BuildOptions {
  /** Prepended to the bundle: runs before the daemon does. */
  head?: string
  /** Appended: when MC_TEST_FAIL_FILE is set, the first process throws (MC_TEST_FAIL=throw) or rejects (reject) after 1.5 s, or once the file MC_TEST_FAIL_WHEN exists. */
  failOnce?: boolean
}

const outDirs: string[] = []
export function cleanVersions(): void {
  for (const d of outDirs.splice(0)) rmSync(d, { recursive: true, force: true })
}

/** Builds daemon `version` from daemon/src and packs the tarball (npm layout, plus launcher.sh) into a new directory. */
export async function buildVersion(version: string, o: BuildOptions = {}, dir = mkdtempSync(join(tmpdir(), "mc-version-"))): Promise<Version> {
  if (!outDirs.includes(dir)) outDirs.push(dir)
  const stage = mkdtempSync(join(tmpdir(), "mc-stage-"))
  try {
    const pkg = join(stage, "package")
    mkdirSync(pkg)
    const bundle = join(pkg, "daemon.js")
    await run("bun", bundleArgs(version, bundle), { cwd: join(ROOT, "daemon") })
    const text = readFileSync(bundle, "utf8")
    const eol = text.startsWith("#!") ? text.indexOf("\n") + 1 : 0 // the shebang stays the first line
    writeFileSync(bundle, text.slice(0, eol) + (o.head ?? "") + text.slice(eol) + (o.failOnce ? FAIL_ONCE : ""))
    chmodSync(bundle, 0o755)
    copyFileSync(LAUNCHER, join(pkg, "launcher.sh"))
    chmodSync(join(pkg, "launcher.sh"), 0o755)
    writeFileSync(join(pkg, "package.json"), JSON.stringify({ name: "agentd", version, bin: { "agentd": "daemon.js" } }))
    const tgz = join(dir, `agentd-${version}.tgz`)
    await run("sh", ["-c", `tar -cf - -C "$1" package | gzip -n > "$2"`, "sh", stage, tgz])
    const sha256 = createHash("sha256").update(readFileSync(tgz)).digest("hex")
    return { version, dir, tgz, sha256 }
  } finally {
    rmSync(stage, { recursive: true, force: true })
  }
}

/** A machine: a home with ~/.agentd in it, installed like install.sh installs. */
export class Machine {
  readonly box = new Sandbox()
  readonly home = join(this.box.root, "mc")
  private launcherProc: ChildProcess | undefined
  out = ""
  /** Resolves with the exit code of the launcher when it ends (a Stop or a revoke ends the loop). */
  launcherExit = new Promise<number>(() => undefined)

  get daemonDir(): string {
    return join(this.home, "daemon")
  }

  read(name: string): string | undefined {
    const f = join(this.daemonDir, name)
    return existsSync(f) ? readFileSync(f, "utf8").trim() : undefined
  }

  current(): string {
    return readlinkSync(join(this.daemonDir, "current"))
  }

  versions(): string[] {
    return readdirSync(this.daemonDir).filter((n) => /^\d/.test(n)).sort((a, b) => a.localeCompare(b))
  }

  leftovers(): string[] {
    return readdirSync(this.daemonDir).filter((n) => n.startsWith(".update-") || n.startsWith(".current."))
  }

  /** Unpacks `v` into daemon/<version>, points current at it and writes the launcher, as install.sh does. */
  async install(v: Version): Promise<void> {
    const dest = join(this.daemonDir, v.version)
    mkdirSync(dest, { recursive: true })
    await run("tar", ["-xzf", v.tgz, "-C", dest, "--strip-components=1"])
    const current = join(this.daemonDir, "current")
    rmSync(current, { force: true })
    symlinkSync(v.version, current)
    mkdirSync(join(this.home, "bin"), { recursive: true })
    copyFileSync(join(dest, "launcher.sh"), join(this.home, "bin", "agentd"))
    chmodSync(join(this.home, "bin", "agentd"), 0o755)
  }

  pair(server: string): void {
    this.box.writeConfig(server, TOKEN)
  }

  env(extra: Record<string, string> = {}): Record<string, string> {
    return {
      PATH: `${NODE_DIR}:/usr/bin:/bin`,
      HOME: this.box.root,
      AGENTD_HOME: this.home,
      XDG_CONFIG_HOME: this.box.config,
      XDG_STATE_HOME: this.box.state,
      // The agents are fakes: no test installs an adapter from the network unless it asks to.
      AGENTD_TRAY: "off",
      AGENTD_AGENT_CLAUDE: "true",
      AGENTD_AGENT_CODEX: "true",
      ...extra,
    }
  }

  /** `agentd run` under the launcher in restart-loop mode: what a service manager does, with the launcher's own loop. */
  startLoop(extra: Record<string, string> = {}): void {
    const p = spawn(join(this.home, "bin", "agentd"), ["run"], {
      cwd: this.box.work,
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
      env: this.env({ AGENTD_RESTART: "loop", ...extra }),
    })
    p.stdout.on("data", (d: Buffer) => (this.out += d.toString()))
    p.stderr.on("data", (d: Buffer) => (this.out += d.toString()))
    this.launcherProc = p
    this.launcherExit = new Promise((resolve) => p.once("exit", (code, signal) => resolve(code ?? (signal ? 128 : -1))))
  }

  get launcherPid(): number {
    return this.launcherProc?.pid ?? -1
  }

  /** The pid of the daemon the last "(pid N)" line of the output names. */
  daemonPid(): number | undefined {
    const all = [...this.out.matchAll(/\(pid (\d+)\) serving agents/g)]
    const last = all[all.length - 1]?.[1]
    return last === undefined ? undefined : Number(last)
  }

  async stop(): Promise<void> {
    const p = this.launcherProc
    if (p?.pid !== undefined) {
      try {
        process.kill(p.pid, "SIGTERM")
        await new Promise<void>((r) => {
          const t = setTimeout(() => {
            try {
              process.kill(-p.pid!, "SIGKILL")
            } catch {
              /* gone */
            }
            r()
          }, 15_000)
          p.once("exit", () => {
            clearTimeout(t)
            r()
          })
        })
      } catch {
        /* gone */
      }
    }
    this.box.remove()
  }
}
