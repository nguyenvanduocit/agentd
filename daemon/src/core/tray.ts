import { posix, win32 } from "node:path"
import type { AgentStatus } from "./agents.ts"
import { originOf } from "./local.ts"

/** The state message the tray reads on stdin (tray/menu.go State). */
export interface TrayState {
  t: "state"
  version: string
  apps: string[]
  pages: string[]
  sessions: number
  managed: boolean
  gatewayOff: boolean
  /** Whether Open log has a file to open; without one the daemon writes to its terminal. */
  log: boolean
  agents: AgentStatus[]
  /** The setting "install updates automatically", and whether this daemon can update itself at all. */
  updates: boolean
  updatable: boolean
}

export interface TrayFacts {
  version: string
  apps: string[]
  browsers: string[]
  sessions: number
  managed: boolean
  /** The port of the local gateway, undefined when none was free. */
  gatewayPort: number | undefined
  /** The daemon's log file, when it runs as an installed service. */
  logFile: string | undefined
  agents: AgentStatus[]
  updates: boolean
  updatable: boolean
}

export function trayState(f: TrayFacts): TrayState {
  const agents = f.agents.map((a) => ({ name: a.name, title: a.title, ready: a.ready, reason: a.reason }))
  return { t: "state", version: f.version, apps: f.apps, pages: f.browsers, sessions: f.sessions, managed: f.managed, gatewayOff: f.gatewayPort === undefined, log: f.logFile !== undefined, agents, updates: f.updates, updatable: f.updatable }
}

export type TrayAction = { kind: "logs" } | { kind: "restart" } | { kind: "stop" } | { kind: "disconnect"; origin: string } | { kind: "updates"; on: boolean }

/** The choices that are a word of their own; a disconnect names its origin after DISCONNECT. */
const CHOICES = new Map<string, TrayAction>([
  ["logs", { kind: "logs" }],
  ["restart", { kind: "restart" }],
  ["stop", { kind: "stop" }],
  ["updates:on", { kind: "updates", on: true }],
  ["updates:off", { kind: "updates", on: false }],
])
const DISCONNECT = "disconnect:"

/** What the user chose in the tray menu or its settings window, or undefined for a line that is none of those choices. */
export function parseTrayLine(line: string): TrayAction | undefined {
  let m: { t?: unknown; id?: unknown } | null
  try {
    m = JSON.parse(line) as typeof m
  } catch {
    return undefined
  }
  if (m?.t !== "action" || typeof m.id !== "string") return undefined
  const choice = CHOICES.get(m.id)
  if (choice !== undefined) return choice
  if (!m.id.startsWith(DISCONNECT)) return undefined
  const origin = originOf(m.id.slice(DISCONNECT.length))
  return origin === undefined ? undefined : { kind: "disconnect", origin }
}

// Go's names for the platforms the tray is built for (scripts/build-tray.sh).
const OS: Partial<Record<NodeJS.Platform, string>> = { darwin: "darwin", linux: "linux", win32: "windows" }
const ARCH: Record<string, string> = { x64: "amd64", arm64: "arm64" }

/** Where the tray program of this platform sits beside daemon.js in `dir`; undefined when there is no build for it. */
export function trayBinary(dir: string, platform: NodeJS.Platform, arch: string): string | undefined {
  const os = OS[platform]
  const goArch = ARCH[arch]
  if (os === undefined || goArch === undefined) return undefined
  if (platform === "win32") return goArch === "amd64" ? win32.join(dir, "tray", `${os}-${goArch}`, "agentd-tray.exe") : undefined
  return posix.join(dir, "tray", `${os}-${goArch}`, "agentd-tray")
}

/** The command that opens a file with whatever the user opens such files with. */
export function openCommand(platform: NodeJS.Platform, file: string): { file: string; args: string[] } {
  if (platform === "darwin") return { file: "open", args: [file] }
  if (platform === "win32") return { file: "cmd", args: ["/c", "start", "", file] }
  return { file: "xdg-open", args: [file] }
}
