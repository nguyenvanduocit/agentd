import { spawn } from "node:child_process"
import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import {
  ADAPTER_RETRY,
  adaptersDisabled,
  DISABLED_REASON,
  ADAPTERS,
  adapterAgentDir,
  adapterDir,
  adapterKey,
  adapterSpec,
  binEntry,
  failedReason,
  isPartial,
  markerText,
  MARKER,
  npmArgs,
  npmTail,
  oldInstalls,
  packageDir,
  parseMarker,
  partialDir,
  type Adapter,
  type AdapterName,
  type AdapterState,
} from "./core/adapters.ts"
import { backoffDelay } from "./core/backoff.ts"
import type { Env } from "./core/executable.ts"
import type { Layout } from "./core/layout.ts"
import { seamVariable } from "./core/agents.ts"
import { findProgram } from "./host.ts"
import { say, warn } from "./log.ts"
import { killTrackedGroups, signalGroup, trackGroup, untrackGroup } from "./procgroup.ts"

const NPM_TIMEOUT_MS = 30 * 60_000

/** The executable of a complete install, or undefined when the directory is missing, has no valid marker, or lost its file. */
export function readInstalled(dir: string): string | undefined {
  try {
    const rel = parseMarker(readFileSync(join(dir, MARKER), "utf8"))
    if (rel === undefined) return undefined
    const bin = join(dir, rel)
    return existsSync(bin) ? bin : undefined
  } catch {
    return undefined
  }
}

/** Runs npm in its own process group; rejects with the tail of npm's stderr. */
function runNpm(npm: string, args: string[], cwd: string, env: Env): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(npm, args, { cwd, env, detached: true, stdio: ["ignore", "ignore", "pipe"] })
    const pgid = child.pid ?? -1
    trackGroup(pgid)
    let err = ""
    child.stderr.on("data", (d: Buffer) => {
      err = (err + d.toString()).slice(-4000)
    })
    const timer = setTimeout(() => signalGroup(pgid, true), NPM_TIMEOUT_MS)
    child.once("error", (e) => {
      untrackGroup(pgid)
      clearTimeout(timer)
      reject(e)
    })
    child.once("close", (code, signal) => {
      untrackGroup(pgid)
      clearTimeout(timer)
      if (code === 0) resolve()
      else reject(new Error(npmTail(err) || `npm exited with ${String(code ?? signal)}`))
    })
  })
}

/** Removes what an interrupted install left, and the versions older than the previous one. */
function tidy(l: Layout, a: Adapter, keep: string): void {
  const dir = adapterAgentDir(l, a.name)
  const entries = readdirSync(dir)
  for (const e of entries) if (isPartial(e)) rmSync(join(dir, e), { recursive: true, force: true })
  const installs = entries.filter((e) => !isPartial(e)).map((name) => ({ name, mtimeMs: statSync(join(dir, name)).mtimeMs }))
  for (const name of oldInstalls(installs, keep)) rmSync(join(dir, name), { recursive: true, force: true })
}

/**
 * Installs the adapter once: `npm install` into a `.partial` directory, the marker, then one rename to the final name.
 * A directory with the final name and no marker is a leftover and is removed first. Resolves to the adapter's executable.
 */
async function install(l: Layout, a: Adapter, env: Env, partials: Set<string>): Promise<string> {
  const key = adapterKey(a, env)
  const final = adapterDir(l, a.name, key)
  mkdirSync(adapterAgentDir(l, a.name), { recursive: true })
  const done = readInstalled(final)
  if (done !== undefined) return done
  rmSync(final, { recursive: true, force: true })
  for (const e of readdirSync(adapterAgentDir(l, a.name))) if (isPartial(e)) rmSync(join(adapterAgentDir(l, a.name), e), { recursive: true, force: true })

  const npm = findProgram("npm", env)
  if (npm === undefined) throw new Error("npm not found: install Node.js (it comes with npm)")
  const part = partialDir(l, a.name, key, process.pid)
  mkdirSync(part)
  partials.add(part)
  const spec = adapterSpec(a, env)
  try {
    await runNpm(npm, npmArgs(part, spec), part, env)
    const found = binEntry(a.pkg, JSON.parse(readFileSync(join(packageDir(part, a.pkg), "package.json"), "utf8")))
    if (!found.ok) throw new Error(found.error)
    const rel = join("node_modules", a.pkg, found.bin)
    if (!existsSync(join(part, rel))) throw new Error(`${a.pkg} installed without its bin ${found.bin}`)
    writeFileSync(join(part, MARKER), markerText(spec, rel))
    renameSync(part, final)
    partials.delete(part)
    tidy(l, a, key)
    return join(final, rel)
  } catch (e) {
    rmSync(part, { recursive: true, force: true })
    partials.delete(part)
    throw e
  }
}

export interface AdapterStoreOptions {
  layout: Layout
  /** The environment npm and the lookups get (PATH, AGENTD_*). */
  env: Env
  retry?: typeof ADAPTER_RETRY
}

/**
 * The adapters of the agents that run one (claude, codex). Each is installed once, in the background of the daemon,
 * the moment it starts: not by `open`, so no timeout of a chat session can cut an install. One install per agent at a
 * time; a failure is kept as the agent's reason and retried with backoff.
 */
export class AdapterStore {
  private readonly states = new Map<AdapterName, AdapterState>()
  private readonly timers = new Set<NodeJS.Timeout>()
  private readonly partials = new Set<string>()
  private stopped = false
  private listener: () => void = () => undefined

  constructor(private readonly o: AdapterStoreOptions) {}

  /** `fn` runs when an agent turns ready or its reason changes. */
  onChange(fn: () => void): void {
    this.listener = fn
  }

  state = (name: AdapterName): AdapterState | undefined => this.states.get(name)

  /** Marks what is installed already as ready (before the first hello) and installs the rest in the background. */
  start(): void {
    for (const a of Object.values(ADAPTERS)) {
      const seam = this.o.env[seamVariable(a.name)]
      if (seam !== undefined && seam.trim() !== "") continue
      if (adaptersDisabled(this.o.env)) {
        this.states.set(a.name, { kind: "failed", reason: DISABLED_REASON })
        continue
      }
      const bin = readInstalled(adapterDir(this.o.layout, a.name, adapterKey(a, this.o.env)))
      if (bin !== undefined) {
        this.states.set(a.name, { kind: "ready", bin })
        continue
      }
      this.states.set(a.name, { kind: "installing" })
      say(`installing the ${a.title} adapter (first run only, this can take several minutes)`)
      void this.run(a, 0)
    }
  }

  private async run(a: Adapter, attempt: number): Promise<void> {
    try {
      const bin = await install(this.o.layout, a, this.o.env, this.partials)
      if (this.stopped) return
      say(`the ${a.title} adapter is installed`)
      this.set(a.name, { kind: "ready", bin })
    } catch (e) {
      if (this.stopped) return
      const delay = backoffDelay(attempt, this.o.retry ?? ADAPTER_RETRY)
      const reason = failedReason(a, e instanceof Error ? e.message : String(e), delay)
      warn(reason)
      this.set(a.name, { kind: "failed", reason })
      const timer = setTimeout(() => {
        this.timers.delete(timer)
        void this.run(a, attempt + 1)
      }, delay)
      this.timers.add(timer)
    }
  }

  private set(name: AdapterName, next: AdapterState): void {
    this.states.set(name, next)
    this.listener()
  }

  /** Ends installs that are running (npm and what it started), removes what they had written, cancels the retries. */
  stop(): void {
    this.stopped = true
    for (const t of this.timers) clearTimeout(t)
    killTrackedGroups()
    for (const p of this.partials) rmSync(p, { recursive: true, force: true })
    this.partials.clear()
  }
}
