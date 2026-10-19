import { createHash } from "node:crypto"
import { posix } from "node:path"
import type { BackoffOptions } from "./backoff.ts"
import type { Env } from "./executable.ts"
import type { Layout } from "./layout.ts"

export type AdapterName = "claude" | "codex"

export interface Adapter {
  name: AdapterName
  title: string
  /** The npm package, installed once per version under ~/.agentd/adapters. */
  pkg: string
  /** The pinned version: the ones in agent/package.json; change both together. */
  version: string
}

export const ADAPTERS: Readonly<Record<AdapterName, Adapter>> = {
  claude: { name: "claude", title: "Claude", pkg: "@agentclientprotocol/claude-agent-acp", version: "0.86.0" },
  codex: { name: "codex", title: "Codex", pkg: "@agentclientprotocol/codex-acp", version: "2.1.1" },
}

/** A failed install is retried from 60 s, doubling, up to 30 min. */
export const ADAPTER_RETRY: BackoffOptions = { baseMs: 60_000, maxMs: 30 * 60_000, jitter: 0.2 }

/** The marker inside an install directory: written last, before the directory gets its final name. */
export const MARKER = ".agentd-adapter.json"

/**
 * TEST SEAM: AGENTD_ADAPTER_SPEC_<NAME> (for example `file:/path/fake-adapter.tgz`) replaces the package spec that
 * `npm install` gets. The server cannot set it.
 */
export function specVariable(name: AdapterName): string {
  return `AGENTD_ADAPTER_SPEC_${name.toUpperCase()}`
}

export function adapterSpec(a: Adapter, env: Env): string {
  const own = env[specVariable(a.name)]
  return own !== undefined && own.trim() !== "" ? own.trim() : `${a.pkg}@${a.version}`
}

/** The directory name of an install: the pinned version, or a hash of the seam's spec. */
export function adapterKey(a: Adapter, env: Env): string {
  const spec = adapterSpec(a, env)
  return spec === `${a.pkg}@${a.version}` ? a.version : `spec-${createHash("sha256").update(spec).digest("hex").slice(0, 12)}`
}

/** `adapters/<agent>`: every version of one adapter. */
export function adapterAgentDir(l: Layout, name: AdapterName): string {
  return posix.join(l.adaptersDir, name)
}

/** `adapters/<agent>/<version>`: exists only complete (renamed from a partial directory, marker inside). */
export function adapterDir(l: Layout, name: AdapterName, key: string): string {
  return posix.join(adapterAgentDir(l, name), key)
}

export function partialDir(l: Layout, name: AdapterName, key: string, pid: number): string {
  return posix.join(adapterAgentDir(l, name), `.partial-${key}-${String(pid)}`)
}

export function isPartial(entry: string): boolean {
  return entry.startsWith(".partial")
}

export type AdapterState =
  | { kind: "installing" }
  | { kind: "ready"; bin: string }
  | { kind: "failed"; reason: string }

/** AGENTD_ADAPTERS=off: no adapter is installed (tests, e2e, debugging); an agent without a seam says so. */
export function adaptersDisabled(env: Env): boolean {
  return env.AGENTD_ADAPTERS === "off"
}

export const DISABLED_REASON = "adapter install is disabled"

export function installingReason(a: Adapter): string {
  return `installing the ${a.title} adapter (first run only, this can take several minutes)`
}

export function failedReason(a: Adapter, detail: string, retryMs: number): string {
  return `cannot install the ${a.title} adapter: ${detail}; retrying in ${Math.round(retryMs / 1000)} s`
}

/** The last lines of npm's stderr: what the user needs to see, not the whole log. */
export function npmTail(stderr: string): string {
  const lines = stderr
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "")
  return lines.slice(-4).join(" | ").slice(-400)
}

/** The arguments of the install: one package into its own prefix, quietly, without npm's audit and fund calls. */
export function npmArgs(prefix: string, spec: string): string[] {
  return ["install", "--prefix", prefix, "--no-audit", "--no-fund", "--loglevel", "error", spec]
}

function binEntries(pkgName: string, bin: unknown): [string, unknown][] {
  if (typeof bin === "string") return [[pkgName.split("/").pop() ?? pkgName, bin]]
  return typeof bin === "object" && bin !== null ? Object.entries(bin) : []
}

/**
 * The adapter's executable, relative to its package, from the `bin` of the installed package.json: a string, or the
 * entry named like the package, or the only entry. Anything else is an error: the path is never guessed.
 */
export function binEntry(pkgName: string, json: unknown): { ok: true; bin: string } | { ok: false; error: string } {
  const entries = binEntries(pkgName, (json as { bin?: unknown } | null)?.bin)
  const unscoped = pkgName.split("/").pop() ?? pkgName
  const rel = (entries.find(([k]) => k === unscoped) ?? (entries.length === 1 ? entries[0] : undefined))?.[1]
  if (typeof rel !== "string" || rel === "") return { ok: false, error: `${pkgName} declares no usable bin in its package.json` }
  const clean = posix.normalize(rel)
  if (posix.isAbsolute(clean) || clean.startsWith("..")) return { ok: false, error: `${pkgName} has a bin outside its package: ${rel}` }
  return { ok: true, bin: clean }
}

/** Where the package's files are, inside an install directory. */
export function packageDir(installDir: string, pkgName: string): string {
  return posix.join(installDir, "node_modules", pkgName)
}

export function markerText(spec: string, binRelativeToInstall: string): string {
  return `${JSON.stringify({ spec, bin: binRelativeToInstall })}\n`
}

/** The executable of a complete install (relative to the install directory), or undefined when the marker is not valid. */
export function parseMarker(text: string): string | undefined {
  try {
    const bin = (JSON.parse(text) as { bin?: unknown }).bin
    if (typeof bin !== "string" || bin === "" || posix.isAbsolute(bin) || posix.normalize(bin).startsWith("..")) return undefined
    return bin
  } catch {
    return undefined
  }
}

export interface Entry {
  name: string
  mtimeMs: number
}

/** The old install directories to remove: everything except `keep` and the newest other one (the previous version). */
export function oldInstalls(entries: readonly Entry[], keep: string): string[] {
  const others = entries.filter((e) => e.name !== keep && !isPartial(e.name)).sort((a, b) => b.mtimeMs - a.mtimeMs)
  return others.slice(1).map((e) => e.name)
}
