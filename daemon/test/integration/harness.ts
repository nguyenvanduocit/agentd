// Shared by the integration tests: builds the real bundle, the Go server double and the fake agent once per run, and
// starts them as real processes. Nothing here is mocked: the daemon under test is `node dist/daemon.js`.
import { execFile, execFileSync, spawn, type ChildProcess } from "node:child_process"
import { generateKeyPairSync } from "node:crypto"
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs"
import { createServer } from "node:net"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { afterAll, setDefaultTimeout } from "bun:test"
import { promisify } from "node:util"
import type { AgentInfo, Hello } from "../../../contract/define.acp.ts"
import { publicKeyBase64, publicKeyPem, signRelease } from "../../../scripts/sign-daemon.ts"

setDefaultTimeout(90_000)

// Whatever a failed test left running is killed when the file ends, so a red test cannot hang the run.
/** process -> whether it leads a process group (daemons do). */
const leftovers = new Map<ChildProcess, boolean>()
afterAll(() => {
  for (const [p, group] of leftovers) {
    try {
      if (p.pid !== undefined) process.kill(group ? -p.pid : p.pid, "SIGKILL")
    } catch {
      /* gone */
    }
  }
})

const run = promisify(execFile)
const ROOT = resolve(import.meta.dir, "../../..")
/** The node that runs the daemon, by absolute path: a test may give the daemon a PATH without it. */
const NODE = execFileSync("which", ["node"], { encoding: "utf8" }).trim()
export const VERSION = "0.0.0-itest"
export const TOKEN = "test-token"

// The release key of this run: every test bundle trusts it (as build-daemon.sh bakes in the real one), and testSign
// signs offers with it.
const TEST_KEY = generateKeyPairSync("ed25519").privateKey.export({ type: "pkcs8", format: "pem" }).toString()
/** The public half, as the bundle bakes it in: base64 of the SPKI DER. */
export const TEST_RELEASE_KEY = publicKeyBase64(publicKeyPem(TEST_KEY))
export const testSign = (version: string, sha256: string): string => signRelease(TEST_KEY, version, sha256)

/** The `bun build` arguments that bundle daemon `version` from src/main.ts into `out`, with the release key of this run. */
export function bundleArgs(version: string, out: string): string[] {
  return [
    "build", "src/main.ts", "--target=node", "--format=cjs", "--define", `AGENTD_VERSION="${version}"`,
    "--define", `AGENTD_RELEASE_KEY="${TEST_RELEASE_KEY}"`, "--define", 'AGENTD_RELEASE_URL=""', "--outfile", out,
  ]
}

export interface Built {
  bundle: string
  /** The Go test double of an app's server. */
  server: string
  /** The real release server (cmd/agentd-server). */
  releaseServer: string
  fakeAgent: string
}

let built: Promise<Built> | undefined

/** The Go binaries are tens of MB: builds of earlier runs (older than ten minutes) do not stay in the temporary directory. */
function removeStaleBuilds(): void {
  for (const name of readdirSync(tmpdir())) {
    if (!name.startsWith("mc-daemon-it-build-")) continue
    const dir = join(tmpdir(), name)
    if (Date.now() - statSync(dir).mtimeMs > 600_000) rmSync(dir, { recursive: true, force: true })
  }
}

/** Builds once per process, into a fresh directory (the bundle is rebuilt on every run, never reused). */
export function build(): Promise<Built> {
  built ??= (async () => {
    // `bun run test` gives the run its own directory and removes it at the end (test/run.sh); a bare `bun test` makes one.
    const own = process.env.MC_IT_BUILD_DIR
    removeStaleBuilds()
    const dir = own !== undefined && own !== "" ? own : mkdtempSync(join(tmpdir(), "mc-daemon-it-build-"))
    const out: Built = { bundle: join(dir, "daemon.js"), server: join(dir, "server"), releaseServer: join(dir, "agentd-server"), fakeAgent: join(dir, "fake-agent") }
    await Promise.all([
      run("bun", bundleArgs(VERSION, out.bundle), { cwd: join(ROOT, "daemon") }),
      run("go", ["build", "-tags", "daemontest", "-o", out.server, "./daemon/test/server"], { cwd: ROOT }),
      run("go", ["build", "-o", out.releaseServer, "./cmd/agentd-server"], { cwd: ROOT }),
      run("go", ["build", "-o", out.fakeAgent, "./cmd/fake-agent"], { cwd: ROOT }),
    ])
    return out
  })()
  return built
}

/** Polls `fn` until it returns something other than undefined or false. */
export async function waitFor<T>(what: string, fn: () => T | undefined | false | Promise<T | undefined | false>, ms = 20_000): Promise<T> {
  const deadline = Date.now() + ms
  for (;;) {
    const v = await fn()
    if (v !== undefined && v !== false) return v
    if (Date.now() > deadline) throw new Error(`timed out after ${ms} ms waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 50))
  }
}

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Whether any process of the group is left. */
export function groupAlive(pgid: number): boolean {
  try {
    process.kill(-pgid, 0)
    return true
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "EPERM"
  }
}

export interface Frame {
  session: string
  kind: "out" | "err" | "exit"
  data?: string
  len: number
  sha256: string
  code: number
}

/** What the server double reports is what the daemon sent: the contract's own types. */
export type { AgentInfo, Hello }

export interface Status {
  connected: boolean
  hellos: Hello[] | null
  /** The lists the daemon pushed with `agents`, in order. */
  agentLists: AgentInfo[][] | null
  connects: Record<string, number> | null
  rejects: number
  mcpLines: number
  frames: number
}

type Reply<T> = { ok: T } | { error: string }

/** The Go server double (daemon/test/server) and its control API. */
export class TestServer {
  private constructor(
    readonly proc: ChildProcess,
    readonly origin: string,
  ) {}

  static async start(token = TOKEN, flags: string[] = []): Promise<TestServer> {
    const b = await build()
    const proc = spawn(b.server, ["-token", token, ...flags], { stdio: ["ignore", "pipe", "inherit"] })
    leftovers.set(proc, false)
    let seen = ""
    const origin = await new Promise<string>((resolveOrigin, reject) => {
      proc.stdout.on("data", (d: Buffer) => {
        seen += d.toString()
        const addr = /LISTEN (\S+)/.exec(seen)?.[1]
        if (addr) resolveOrigin(`http://${addr}`)
      })
      proc.once("exit", () => reject(new Error("server exited before it listened")))
    })
    return new TestServer(proc, origin)
  }

  stop(): void {
    this.proc.kill("SIGKILL")
  }

  private async call<T>(path: string, body?: unknown): Promise<T> {
    const res = await fetch(this.origin + path, body === undefined ? {} : { method: "POST", body: JSON.stringify(body) })
    if (!res.ok) throw new Error(`${path}: HTTP ${res.status} ${await res.text()}`)
    return (await res.json()) as T
  }

  status = () => this.call<Status>("/ctl/status")
  frames = async (session?: string) => {
    const all = await this.call<Frame[] | null>("/ctl/frames")
    return (all ?? []).filter((f) => session === undefined || f.session === session)
  }
  open = (session: string, agent: string) => this.call<Reply<{ pid: number; cwd: string }>>("/ctl/open", { session, agent })
  write = (session: string, line: string) => this.call<Reply<boolean>>("/ctl/write", { session, line })
  close = (session: string) => this.call<Reply<boolean>>("/ctl/close", { session })
  kick = (transport: boolean) => this.call<boolean>(`/ctl/kick?transport=${transport ? 1 : 0}`, {})
  /** Disconnects every MCP bridge socket; resolves to how many there were. */
  restart = () => this.call<Reply<boolean>>("/ctl/restart", {})
  stopDaemon = () => this.call<Reply<boolean>>("/ctl/stop", {})
  /** Revokes the token: the daemon's socket is dropped and every later handshake is refused. */
  revoke = () => this.call<boolean>("/ctl/revoke", {})
  /** Refuses the next `n` handshakes as "unavailable", as the hub does while its token store is down. */
  unavailable = (n: number) => this.call<boolean>("/ctl/unavailable", { n })
  kickBridges = () => this.call<number>("/ctl/kick?role=mcp", {})

  /** Waits until a daemon is connected and has said hello `n` times. */
  waitHellos = (n = 1) => waitFor(`${n} hello(s)`, async () => ((await this.status()).hellos ?? []).length >= n && (await this.status()))

  /** Opens a session and returns the pid, failing the test on an RpcError. */
  async openOk(session: string, agent = "claude") {
    const r = await this.open(session, agent)
    if ("error" in r) throw new Error(`open ${session}: ${r.error}`)
    return r.ok
  }
}

/** A temporary home for one daemon: its config, state and working directories. */
export class Sandbox {
  readonly root = mkdtempSync(join(tmpdir(), "mc-daemon-it-"))
  readonly config = join(this.root, "config")
  readonly state = join(this.root, "state")
  readonly work = join(this.root, "work")

  constructor() {
    mkdirSync(this.work, { recursive: true })
  }

  get configFile(): string {
    return join(this.config, "agentd", "daemon.json")
  }

  writeConfig(server: string, token = TOKEN): void {
    this.writeApps([{ server, token }])
  }

  /** Saves these apps, in this order (the first is the update source). */
  writeApps(apps: { server: string; token: string }[], browsers: { origin: string; grantedAt: string }[] = []): void {
    mkdirSync(join(this.config, "agentd"), { recursive: true })
    writeFileSync(this.configFile, JSON.stringify({ apps, browsers }))
  }

  env(extra: Record<string, string> = {}): Record<string, string> {
    return { ...(process.env as Record<string, string>), XDG_CONFIG_HOME: this.config, XDG_STATE_HOME: this.state, AGENTD_HOME: join(this.root, "mc"), AGENTD_AGENT_CODEX: "true", AGENTD_LOCAL_PORT: "0", AGENTD_TRAY: "off", ...extra }
  }

  remove(): void {
    rmSync(this.root, { recursive: true, force: true })
  }
}

/** A running `node daemon.js`; its output is collected. */
export class TestDaemon {
  out = ""
  readonly exited: Promise<number | null>

  private constructor(readonly proc: ChildProcess) {
    const collect = (d: Buffer) => (this.out += d.toString())
    proc.stdout?.on("data", collect)
    proc.stderr?.on("data", collect)
    this.exited = new Promise((r) => proc.once("exit", (code) => r(code)))
  }

  get pid(): number {
    return this.proc.pid ?? -1
  }

  static async start(box: Sandbox, args: string[] = [], env: Record<string, string> = {}, cwd = box.work): Promise<TestDaemon> {
    const b = await build()
    const proc = spawn(NODE, [b.bundle, ...args], { cwd, env: box.env(env), detached: true, stdio: ["ignore", "pipe", "pipe"] })
    leftovers.set(proc, true)
    return new TestDaemon(proc)
  }

  /** SIGTERM and wait; a daemon that ignores it is killed with its group. */
  async stop(): Promise<void> {
    if (this.proc.exitCode !== null || this.proc.signalCode !== null) return
    this.proc.kill("SIGTERM")
    const done = await Promise.race([this.exited.then(() => true), sleep(10_000).then(() => false)])
    if (!done) this.kill("SIGKILL")
  }

  kill(signal: NodeJS.Signals): void {
    try {
      process.kill(this.pid, signal)
    } catch {
      /* gone */
    }
  }
}

/** The pids of `pid` and everything below it. */
export interface SessionsRecord {
  daemon: { pid: number; lstart: string }
  bin: string
  groups: { pgid: number; lstart: string }[]
}

export function readSessions(file: string): SessionsRecord {
  return JSON.parse(readFileSync(file, "utf8")) as SessionsRecord
}

export function processTree(pid: number): number[] {
  const rows = execFileSync("ps", ["-axo", "pid=,ppid="], { encoding: "utf8" })
    .split("\n")
    .map((l) => l.trim().split(/\s+/).map(Number))
    .filter((r) => r.length === 2 && !r.some(Number.isNaN)) as [number, number][]
  const tree = [pid]
  for (const parent of tree) for (const [p, pp] of rows) if (pp === parent && !tree.includes(p)) tree.push(p)
  return tree
}

/** Listening TCP sockets of the given pids (lsof exits 1 and prints nothing when there are none). */
export function listeningPorts(pids: number[]): string {
  try {
    return execFileSync("lsof", ["-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", pids.join(",")], { encoding: "utf8" }).trim()
  } catch (e) {
    const err = e as { status?: number; stdout?: string }
    if (err.status === 1) return (err.stdout ?? "").trim()
    throw e
  }
}

export const sh = (script: string) => `sh -c '${script}'`

export interface RigOptions {
  /** The command line the "claude" agent runs (the AGENTD_AGENT_CLAUDE test seam). */
  claude?: string
  env?: Record<string, string>
  args?: string[]
}

/** A server double, a sandbox and a daemon connected to each other. */
export class Rig {
  readonly pgids: number[] = []
  readonly daemons: TestDaemon[] = []

  private constructor(
    readonly server: TestServer,
    readonly box: Sandbox,
  ) {}

  static async start(o: RigOptions = {}): Promise<Rig> {
    const server = await TestServer.start()
    const box = new Sandbox()
    box.writeConfig(server.origin)
    const rig = new Rig(server, box)
    try {
      await rig.startDaemon(o)
      await server.waitHellos(1)
    } catch (e) {
      await rig.stop()
      throw e
    }
    return rig
  }

  get daemon(): TestDaemon {
    return this.daemons[this.daemons.length - 1]!
  }

  async startDaemon(o: RigOptions = {}): Promise<TestDaemon> {
    const env = { ...(o.claude === undefined ? {} : { AGENTD_AGENT_CLAUDE: o.claude }), ...o.env }
    const d = await TestDaemon.start(this.box, o.args ?? [], env)
    this.daemons.push(d)
    return d
  }

  async open(session: string, agent = "claude") {
    const r = await this.server.openOk(session, agent)
    this.pgids.push(r.pid)
    return r
  }

  /** Waits for a frame of `session` matching `pred`. */
  frame(session: string, what: string, pred: (f: Frame) => boolean) {
    return waitFor(what, async () => (await this.server.frames(session)).find(pred))
  }

  async stop(): Promise<void> {
    for (const d of this.daemons) await d.stop()
    for (const g of this.pgids) {
      try {
        process.kill(-g, "SIGKILL")
      } catch {
        /* gone */
      }
    }
    this.server.stop()
    this.box.remove()
  }
}

/** A free loopback port: bound and released, so the caller can hand it to a process that cannot pick its own. */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer()
    s.once("error", reject)
    s.listen(0, "127.0.0.1", () => {
      const port = (s.address() as { port: number }).port
      s.close(() => resolve(port))
    })
  })
}

/**
 * Writes install.sh of the latest release in `dist` (daemon/install.sh.in with that release's version and sha256, as
 * scripts/build-daemon.sh does), downloading from the release server at `origin`. A dist with no release has none.
 */
export function renderInstall(dist: string, origin: string): void {
  if (!existsSync(join(dist, "latest"))) return
  const version = readFileSync(join(dist, "latest"), "utf8").trim()
  const dir = join(dist, "download", `v${version}`)
  const { sha256 } = JSON.parse(readFileSync(join(dir, "latest.json"), "utf8")) as { sha256: string }
  const script = readFileSync(join(import.meta.dir, "../../install.sh.in"), "utf8")
    .replaceAll("@VERSION@", version)
    .replaceAll("@SHA256@", sha256)
    .replaceAll("@TARBALL_URL@", `${origin}/download/v${version}/agentd-${version}.tgz`)
    .replaceAll("@INSTALL_URL@", `${origin}/latest/download/install.sh`)
  writeFileSync(join(dir, "install.sh"), script, { mode: 0o755 })
}

/** The real stand-in for GitHub Releases (cmd/agentd-server) over a directory of release files. */
export class ReleaseServer {
  /** The server's request log: one `request: <method> <path> <status>` line per request. */
  log = ""

  private constructor(
    readonly proc: ChildProcess,
    readonly origin: string,
    readonly dist: string,
  ) {
    proc.stderr?.on("data", (d: Buffer) => (this.log += d.toString()))
  }

  /** How many times each daemon tarball was downloaded (status 200), by file name. */
  downloads(): Record<string, number> {
    const counts: Record<string, number> = {}
    for (const m of this.log.matchAll(/request: GET \/download\/v[^/ ]+\/(agentd-[^ ]+\.tgz) 200/g)) {
      const name = m[1]!
      counts[name] = (counts[name] ?? 0) + 1
    }
    return counts
  }

  static async start(dist: string): Promise<ReleaseServer> {
    const b = await build()
    const port = await freePort()
    const origin = `http://127.0.0.1:${port}`
    const proc = spawn(b.releaseServer, ["-addr", `127.0.0.1:${port}`, "-dist", dist], { stdio: ["ignore", "ignore", "pipe"] })
    leftovers.set(proc, false)
    await waitFor("the release server", async () => (await fetch(`${origin}/healthz`).catch(() => undefined))?.ok === true)
    renderInstall(dist, origin)
    return new ReleaseServer(proc, origin, dist)
  }

  stop(): void {
    this.proc.kill("SIGKILL")
  }
}
