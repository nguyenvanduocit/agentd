import { posix, win32 } from "node:path"
import type { Env } from "./executable.ts"

export interface Host {
  platform: NodeJS.Platform
  env: Env
  home: string
}

function join(h: Host, ...parts: string[]): string {
  return (h.platform === "win32" ? win32 : posix).join(...parts)
}

/** Linux and macOS: $XDG_CONFIG_HOME or ~/.config. Windows: %APPDATA%. */
function configRoot(h: Host): string {
  if (h.platform === "win32") return h.env.APPDATA ?? join(h, h.home, "AppData", "Roaming")
  return nonEmpty(h.env.XDG_CONFIG_HOME) ?? join(h, h.home, ".config")
}

/** Where daemon state that is not configuration lives: $XDG_STATE_HOME or ~/.local/state, %LOCALAPPDATA% on Windows. */
function stateRoot(h: Host): string {
  if (h.platform === "win32") return h.env.LOCALAPPDATA ?? join(h, h.home, "AppData", "Local")
  return nonEmpty(h.env.XDG_STATE_HOME) ?? join(h, h.home, ".local", "state")
}

function nonEmpty(v: string | undefined): string | undefined {
  return v === undefined || v === "" ? undefined : v
}

export function configFile(h: Host): string {
  return join(h, configRoot(h), "agentd", "daemon.json")
}

/** The record of the process groups of running agents, read by the next start to end what a killed daemon left. */
export function sessionsFile(h: Host): string {
  return join(h, stateRoot(h), "agentd", "sessions.json")
}

/** The daemon's own home: $AGENTD_HOME (a test seam, and for people who keep tools elsewhere) or ~/.agentd. */
export function agentdHome(h: Host): string {
  return nonEmpty(h.env.AGENTD_HOME) ?? join(h, h.home, ".agentd")
}
