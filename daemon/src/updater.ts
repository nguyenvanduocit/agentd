import { execFile } from "node:child_process"
import { createHash } from "node:crypto"
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { promisify } from "node:util"
import { backoffDelay } from "./core/backoff.ts"
import { versionDir, type Layout } from "./core/layout.ts"
import { signatureProblem } from "./core/signature.ts"
import { decide, parseVersionRecord, RETRY, VERSION_PATTERN, type Offer } from "./core/update.ts"
import { fetchFollowing } from "./http.ts"
import { say, warn } from "./log.ts"

const run = promisify(execFile)

const DOWNLOAD_TIMEOUT_MS = 120_000
const MAX_TARBALL_BYTES = 100 * 1024 * 1024
/** How often a staged update looks again for an idle daemon. */
export const IDLE_CHECK_MS = 5000

function readRecord(file: string): string | undefined {
  try {
    return parseVersionRecord(readFileSync(file, "utf8"))
  } catch {
    return undefined
  }
}

/** Points `current` at a version by renaming a new symlink over it: a reader sees the old target or the new, never none. */
function pointCurrent(l: Layout, version: string): void {
  const tmp = join(l.daemonDir, `.current.${process.pid}`)
  rmSync(tmp, { force: true })
  symlinkSync(version, tmp)
  renameSync(tmp, l.currentLink)
}

/** Downloads the offer's tarball into `dir/pkg.tgz`; returns the path once its sha256 matches. */
async function download(origin: string, offer: Offer, dir: string, fetchFn: typeof fetch): Promise<string> {
  const res = await fetchFollowing(fetchFn, `${origin}${offer.url}`, AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS))
  if (!res.ok) throw new Error(`${origin}${offer.url}: HTTP ${res.status}`)
  const body = Buffer.from(await res.arrayBuffer())
  if (body.length > MAX_TARBALL_BYTES) throw new Error(`the download is ${body.length} bytes: too large for a daemon`)
  const actual = createHash("sha256").update(body).digest("hex")
  if (actual !== offer.sha256) throw new Error(`sha256 is ${actual}, the server said ${offer.sha256}`)
  const file = join(dir, "pkg.tgz")
  writeFileSync(file, body)
  return file
}

/** Scans the tarball entries and rejects any that contain path traversal or symlinks. */
async function scanTarball(tgz: string): Promise<void> {
  const { stdout } = await run("tar", ["-tvzf", tgz])
  for (const line of stdout.split("\n")) {
    if (line === "") continue
    if (line.includes(" -> ")) throw new Error(`tarball contains a symlink: ${line}`)
    // The last field in `tar -tv` output is the file path
    const path = line.split(/\s+/).pop() ?? ""
    if (path.split("/").some((c) => c === "..")) throw new Error(`tarball contains path traversal: ${line}`)
  }
}

/** Unpacks a verified tarball (npm layout: one top-level directory) into `dir/pkg` and checks what came out. */
async function unpack(tgz: string, dir: string, version: string): Promise<string> {
  await scanTarball(tgz)
  const out = join(dir, "pkg")
  mkdirSync(out)
  await run("tar", ["-xzf", tgz, "-C", out, "--strip-components=1"])
  if (!existsSync(join(out, "daemon.js"))) throw new Error("the tarball has no daemon.js")
  const pkg = JSON.parse(readFileSync(join(out, "package.json"), "utf8")) as { version?: unknown }
  if (pkg.version !== version) throw new Error(`the tarball holds version ${String(pkg.version)}, not ${version}`)
  return out
}

/** Removes every version directory except those in `keep`. */
function prune(l: Layout, keep: Set<string>): void {
  for (const e of readdirSync(l.daemonDir, { withFileTypes: true })) {
    if (e.isDirectory() && VERSION_PATTERN.test(e.name) && !keep.has(e.name)) rmSync(join(l.daemonDir, e.name), { recursive: true, force: true })
  }
}

export interface UpdaterOptions {
  layout: Layout
  /** The releases page the tarball is fetched from (see releaseBase). */
  server: string
  /** The version this process runs. */
  running: string
  /** The release public key (base64 SPKI DER) every update must be signed with; undefined refuses every update. */
  releaseKey: string | undefined
  /** Whether no agent session is running. */
  idle: () => boolean
  /** The update is in place and the daemon is idle: exit so the service starts the new version. */
  restart: (version: string) => void
  fetchFn?: typeof fetch
  idleCheckMs?: number
  retry?: typeof RETRY
}

/**
 * Keeps the daemon on the version the server serves. `offer` takes every hello reply: a different version signed by
 * the release key is downloaded, its sha256 checked, unpacked beside the running one and made `current`; then, once no
 * session runs, `restart` is called. A failure (a bad signature too) changes nothing and is retried with backoff.
 */
export class Updater {
  private staged: string | undefined
  private busy: Promise<void> | undefined
  private queued: Offer | undefined
  private attempt = 0
  private timer: NodeJS.Timeout | undefined
  private stopped = false
  private readonly told = new Set<string>()

  constructor(private readonly o: UpdaterOptions) {}

  offer(reply: Offer): void {
    if (this.stopped) return
    if (this.busy) {
      this.queued = reply
      return
    }
    this.busy = this.consider(reply).finally(() => {
      this.busy = undefined
      const next = this.queued
      this.queued = undefined
      if (next) this.offer(next)
    })
  }

  stop(): void {
    this.stopped = true
    clearTimeout(this.timer)
  }

  private async consider(reply: Offer): Promise<void> {
    const d = decide({ running: this.o.running, staged: this.staged, rejected: readRecord(this.o.layout.rejectedFile) }, reply)
    switch (d.kind) {
      case "none":
        this.forget()
        return
      case "waiting":
        return
      case "unstage":
        this.unstage()
        return
      case "rejected":
        if (!this.told.has(d.version)) warn(`not updating to ${d.version}: it crashed at start twice here; waiting for another version`)
        this.told.add(d.version)
        return
      case "invalid":
        warn(`not updating: the server's offer is wrong (${d.why})`)
        return
      case "update":
        await this.update(d.offer)
    }
  }

  /** The server has nothing newer: a retry that is waiting has nothing left to do. */
  private forget(): void {
    clearTimeout(this.timer)
    this.attempt = 0
  }

  private unstage(): void {
    clearTimeout(this.timer)
    pointCurrent(this.o.layout, this.o.running)
    say(`the server serves ${this.o.running} again: update to ${this.staged ?? "?"} cancelled`)
    this.staged = undefined
  }

  private async update(offer: Offer): Promise<void> {
    clearTimeout(this.timer)
    // A signature that does not verify will not verify later: say so, like an invalid offer, and do not retry.
    const unsigned = signatureProblem(this.o.releaseKey, offer)
    if (unsigned !== undefined) {
      warn(`not updating to ${offer.version}: ${unsigned}`)
      return
    }
    try {
      await this.stage(offer)
    } catch (e) {
      const delay = backoffDelay(this.attempt++, this.o.retry ?? RETRY)
      warn(`cannot update to ${offer.version}: ${e instanceof Error ? e.message : String(e)}; staying on ${this.o.running}, retrying in ${Math.round(delay / 1000)} s`)
      this.timer = setTimeout(() => this.offer(offer), delay)
      return
    }
    this.attempt = 0
    this.staged = offer.version
    say(`updated to ${offer.version}; restarting when no session is running`)
    this.waitIdle(offer.version)
  }

  /**
   * Download, check the sha256, unpack, point `current`. Nothing is left half done: a temporary directory holds the
   * work. `update` has checked the offer's signature, which covers the version and the sha256, and the download must
   * hash to that sha256, so only a tarball the release key signed is unpacked.
   */
  private async stage(offer: Offer): Promise<void> {
    const l = this.o.layout
    mkdirSync(l.daemonDir, { recursive: true })
    const dest = versionDir(l, offer.version)
    if (!existsSync(join(dest, "daemon.js"))) {
      const work = mkdtempSync(join(l.daemonDir, ".update-"))
      try {
        const tgz = await download(this.o.server, offer, work, this.o.fetchFn ?? fetch)
        const pkg = await unpack(tgz, work, offer.version)
        rmSync(dest, { recursive: true, force: true })
        renameSync(pkg, dest)
      } finally {
        rmSync(work, { recursive: true, force: true })
      }
    }
    writeFileSync(l.previousFile, `${this.o.running}\n`)
    rmSync(l.rejectedFile, { force: true })
    pointCurrent(l, offer.version)
    prune(l, new Set([offer.version, this.o.running]))
  }

  private waitIdle(version: string): void {
    if (this.stopped || this.staged !== version) return
    if (this.o.idle()) {
      this.o.restart(version)
      return
    }
    this.timer = setTimeout(() => this.waitIdle(version), this.o.idleCheckMs ?? IDLE_CHECK_MS)
  }
}
