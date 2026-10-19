import { posix, win32 } from "node:path"
import { pathKey, type Env } from "./executable.ts"

export const WRAPPER = "agentd-mcp"

export interface WrapperTarget {
  /** The node binary that runs the daemon. */
  node: string
  /** The daemon's own script. */
  script: string
  /** Options placed before the caller's own arguments, e.g. ["--config", file]. */
  args: string[]
}

export function wrapperFileName(platform: NodeJS.Platform): string {
  return platform === "win32" ? `${WRAPPER}.cmd` : WRAPPER
}

function shQuote(s: string): string {
  return "'" + s.replaceAll("'", "'\\''") + "'"
}

function cmdQuote(s: string): string {
  return `"${s.replaceAll("%", "%%")}"`
}

/**
 * The wrapper the agent finds as `agentd-mcp` on its PATH: it runs `node daemon.js mcp <args> "$@"`.
 * The server names the command without a path, and a wrapper (not a symlink) keeps the daemon's own script and the
 * config location out of the server's hands.
 */
export function wrapperScript(platform: NodeJS.Platform, t: WrapperTarget): string {
  if (platform === "win32") {
    const words = [t.node, t.script, "mcp", ...t.args].map(cmdQuote).join(" ")
    return "@echo off\r\n" + words + " %*\r\n"
  }
  const words = [t.node, t.script, "mcp", ...t.args].map(shQuote).join(" ")
  return "#!/bin/sh\nexec " + words + ' "$@"\n'
}

/** The environment of an agent: the daemon's, with `binDir` first on PATH. */
export function agentEnv(base: Env, binDir: string, platform: NodeJS.Platform): Record<string, string> {
  const key = pathKey(base, platform)
  const delimiter = (platform === "win32" ? win32 : posix).delimiter
  const old = base[key]
  const env: Record<string, string> = {}
  for (const [k, v] of Object.entries(base)) if (v !== undefined) env[k] = v
  env[key] = old ? `${binDir}${delimiter}${old}` : binDir
  return env
}
