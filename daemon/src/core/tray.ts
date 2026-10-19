import { posix, win32 } from "node:path"
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
}

export interface TrayFacts {
  version: string
  apps: string[]
  browsers: string[]
  sessions: number
  managed: boolean
  /** The port of the local gateway, undefined when none was free. */
  gatewayPort: number | undefined
}

export function trayState(f: TrayFacts): TrayState {
  return { t: "state", version: f.version, apps: f.apps, pages: f.browsers, sessions: f.sessions, managed: f.managed, gatewayOff: f.gatewayPort === undefined }
}

export type TrayAction = { kind: "logs" } | { kind: "restart" } | { kind: "stop" } | { kind: "disconnect"; origin: string }

const DISCONNECT = "disconnect:"

/** What the user chose in the tray menu, or undefined for a line that is not one of the four choices. */
export function parseTrayLine(line: string): TrayAction | undefined {
  let m: { t?: unknown; id?: unknown } | null
  try {
    m = JSON.parse(line) as typeof m
  } catch {
    return undefined
  }
  if (m?.t !== "action" || typeof m.id !== "string") return undefined
  if (m.id === "logs" || m.id === "restart" || m.id === "stop") return { kind: m.id }
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
