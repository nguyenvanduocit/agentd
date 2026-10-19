import { posix } from "node:path"
import type { Env } from "./executable.ts"

export type ManagerName = "launchd" | "systemd" | "loop"

export const LAUNCHD_LABEL = "com.agentd.daemon"
export const SYSTEMD_UNIT = "agentd"
/** Seconds the service manager waits between two starts. */
export const RESTART_SECONDS = 5

/**
 * TEST SEAMS: $AGENTD_SERVICE_LABEL renames the service (launchd label, systemd unit name) and
 * $AGENTD_SERVICE_DIR is where its definition file is written. A test never touches the user's real service.
 */
export function serviceName(manager: ManagerName, env: Env): string {
  const own = env.AGENTD_SERVICE_LABEL
  if (own !== undefined && own !== "") return own
  return manager === "launchd" ? LAUNCHD_LABEL : SYSTEMD_UNIT
}

export function serviceFile(manager: "launchd" | "systemd", name: string, home: string, env: Env): string {
  const dir = env.AGENTD_SERVICE_DIR
  if (dir !== undefined && dir !== "") return posix.join(dir, fileName(manager, name))
  if (manager === "launchd") return posix.join(home, "Library", "LaunchAgents", fileName(manager, name))
  const config = env.XDG_CONFIG_HOME
  return posix.join(config !== undefined && config !== "" ? config : posix.join(home, ".config"), "systemd", "user", fileName(manager, name))
}

function fileName(manager: "launchd" | "systemd", name: string): string {
  return manager === "launchd" ? `${name}.plist` : `${name}.service`
}

/**
 * What a service needs from the shell that installs it: a service starts with an empty environment, so PATH (where
 * node, npm and the agents are), the seams of the tests (AGENTD_*), the XDG directories and the variables that name
 * the user's screen (the tray program needs them to find it) are copied into its definition. AGENTD_RESTART and the
 * service seams themselves are not.
 */
const SCREEN = ["DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY"]

function copiedIntoService(name: string): boolean {
  if (name === "AGENTD_RESTART" || name === "AGENTD_MANAGED" || name.startsWith("AGENTD_SERVICE_")) return false
  return name === "PATH" || name === "XDG_CONFIG_HOME" || name === "XDG_STATE_HOME" || SCREEN.includes(name) || name.startsWith("AGENTD_")
}

export function passthroughEnv(env: Env): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(env)) {
    if (v === undefined || v === "" || /[\n\r\0]/.test(v)) continue
    if (copiedIntoService(k)) out[k] = v
  }
  return out
}

function xml(s: string): string {
  return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;")
}

export interface PlistInput {
  label: string
  launcher: string
  /** The daemon's working directory: launchd would start it in `/`. */
  workDir: string
  logFile: string
  env: Record<string, string>
}

/** The LaunchAgent: starts at login; launchd starts the launcher again when it exits non-zero, and leaves it after exit 0. */
export function plist(i: PlistInput): string {
  const env = Object.entries(i.env)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `    <key>${xml(k)}</key>\n    <string>${xml(v)}</string>\n`)
    .join("")
  const envBlock = env === "" ? "" : "  <key>EnvironmentVariables</key>\n  <dict>\n" + env + "  </dict>\n"
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${xml(i.label)}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${xml(i.launcher)}</string>
    <string>run</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${xml(i.workDir)}</string>
${envBlock}  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>${RESTART_SECONDS}</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>${xml(i.logFile)}</string>
  <key>StandardErrorPath</key>
  <string>${xml(i.logFile)}</string>
</dict>
</plist>
`
}

export interface UnitInput {
  launcher: string
  /** The daemon's working directory; undefined is the user's home (%h). */
  workDir: string | undefined
  logFile: string
  env: Record<string, string>
}

/** systemd's quoting inside "...": backslash, double quote, and % and $ which systemd would expand. */
function unitQuote(s: string): string {
  return `"${s.replaceAll("\\", "\\\\").replaceAll('"', '\\"').replaceAll("%", "%%").replaceAll("$", "$$$$")}"`
}

/** The systemd user unit: starts at login (default.target); systemd starts the launcher again when it exits non-zero, and leaves it after exit 0. */
export function systemdUnit(i: UnitInput): string {
  // A unit is read line by line: a line break in a path would start a directive of its own.
  for (const path of [i.launcher, i.workDir ?? "", i.logFile]) {
    if (/[\n\r]/.test(path)) throw new Error(`${JSON.stringify(path)}: a path with a line break cannot go into a systemd unit`)
  }
  const env = Object.entries(i.env)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => "Environment=" + unitQuote(k + "=" + v) + "\n")
    .join("")
  return `[Unit]
Description=agentd: runs your agents for the apps you allow
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=${i.workDir === undefined ? "%h" : i.workDir.replaceAll("%", "%%")}
ExecStart=${unitQuote(i.launcher)} run
Restart=on-failure
RestartSec=${RESTART_SECONDS}
TimeoutStopSec=15
${env}StandardOutput=append:${i.logFile.replaceAll("%", "%%")}
StandardError=append:${i.logFile.replaceAll("%", "%%")}

[Install]
WantedBy=default.target
`
}

export interface Running {
  running: boolean
  pid: number | undefined
  /** The definition file the manager loaded, when it says. */
  path: string | undefined
  /** How the last run ended, when the manager says: the exit code, or -1 for a signal. */
  lastExit: number | undefined
  /** The manager has a restart scheduled (after a crash, within its throttle). */
  pending: boolean
}

function launchctlExit(code: string | undefined, signal: string | undefined): number | undefined {
  const n = Number(code)
  if (code !== undefined && Number.isInteger(n)) return n
  return signal === undefined ? undefined : -1
}

/** `launchctl print gui/<uid>/<label>`: the top-level `path`, `state` and `pid` lines come before any nested block. */
export function parseLaunchctlPrint(text: string): Running {
  const first = (key: string): string | undefined => new RegExp(`^\\s*${key} = (.+)$`, "m").exec(text)?.[1]?.trim()
  const pid = first("pid")
  const lastExit = launchctlExit(first("last exit code"), first("last terminating signal"))
  const running = first("state") === "running"
  return { running, pid: pid === undefined ? undefined : Number(pid), path: first("path"), lastExit, pending: !running && lastExit !== undefined && lastExit !== 0 }
}

/** `systemctl --user show -p ActiveState,ExecMainStatus,MainPID,FragmentPath <unit>`: KEY=VALUE lines. */
export function parseSystemctlShow(text: string): Running {
  const v: Record<string, string> = {}
  for (const line of text.split("\n")) {
    const i = line.indexOf("=")
    if (i > 0) v[line.slice(0, i)] = line.slice(i + 1).trim()
  }
  const pid = Number(v.MainPID ?? 0)
  const path = v.FragmentPath
  const exit = Number(v.ExecMainStatus)
  return {
    running: v.ActiveState === "active",
    pid: pid > 0 ? pid : undefined,
    path: path === undefined || path === "" ? undefined : path,
    lastExit: v.ExecMainStatus === undefined || v.ExecMainStatus === "" || Number.isNaN(exit) ? undefined : exit,
    pending: v.ActiveState === "activating",
  }
}

/** The environment of the service: what the installing shell passes on, and the word that says a service restarts the daemon. */
export function serviceEnv(env: Env): Record<string, string> {
  return { ...passthroughEnv(env), AGENTD_MANAGED: "1" }
}

/** One line for `service status`: what the daemon is doing, told apart by how it last exited (see core/exit.ts). */
export function stateText(now: Running | undefined): string {
  if (!now) return "not installed"
  if (now.running) return now.pid === undefined ? "running" : "running (pid " + String(now.pid) + ")"
  if (now.pending) return "restarting after a crash (last exit code " + String(now.lastExit ?? "?") + ")"
  if (now.lastExit === 0) return "stopped (it exited 0: a Stop from the web UI, or a revoked token). Start it again with: agentd service start"
  return "loaded, not running"
}

/** `loginctl show-user <user> -p Linger`: without linger the service stops when the last session ends. */
export function lingerOff(text: string): boolean {
  return /^Linger=no$/m.test(text)
}
