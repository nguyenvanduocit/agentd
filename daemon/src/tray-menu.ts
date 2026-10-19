import { spawn } from "node:child_process"
import { existsSync, realpathSync } from "node:fs"
import { dirname } from "node:path"
import type { Hello } from "../../contract/define.acp.ts"
import { loadRegistry } from "./config.ts"
import { EXIT_STOPPED } from "./core/exit.ts"
import { openCommand, trayBinary, trayState, type TrayAction, type TrayState } from "./core/tray.ts"
import { say, warn } from "./log.ts"

const REFRESH_MS = 2000

/** The tray program to start: AGENTD_TRAY (a path; "off" for none) or the one packaged beside daemon.js. */
export function trayFile(env: Readonly<Record<string, string | undefined>>): string | undefined {
  const seam = env.AGENTD_TRAY
  if (seam === "off") return undefined
  if (seam !== undefined && seam !== "") return seam
  try {
    const packaged = trayBinary(dirname(realpathSync(process.argv[1] ?? "")), process.platform, process.arch)
    return packaged !== undefined && existsSync(packaged) ? packaged : undefined
  } catch {
    return undefined
  }
}

/** What shows the daemon's state and hands back the user's choices: the tray program it starts, or the app that started it. */
export interface TrayLink {
  start(): void
  update(state: TrayState): void
  stop(): void
}

export interface TrayMenuDeps {
  /** Opens the link, which calls `onAction` with each choice of the user. */
  open: (onAction: (action: TrayAction) => void) => TrayLink
  registryFile: string
  hello: () => Hello
  /** Whether the daemon is linked to the app at `server` now. */
  linked: (server: string) => boolean
  /** Agents running for apps and for web pages. */
  sessions: () => number
  gatewayPort: number | undefined
  /** The daemon's log file, when it runs as an installed service. */
  logFile: string | undefined
  stop: (code: number) => void
  restart: () => void
  disconnect: (origin: string) => Promise<void>
  /** The setting "install updates automatically". */
  updates: () => boolean
  /** Whether this daemon can update itself at all; the setting changes nothing otherwise. */
  updatable: boolean
  /** Saves the updates setting and turns the checks on or off. */
  setUpdates: (on: boolean) => Promise<void>
}

function openLog(logFile: string | undefined): void {
  if (logFile === undefined) {
    say("tray: no log file: this daemon writes to its terminal")
    return
  }
  const cmd = openCommand(process.platform, logFile)
  spawn(cmd.file, cmd.args, { stdio: "ignore", detached: true }).on("error", (e) => warn(`tray: cannot open the log: ${e.message}`)).unref()
}

/** Does what the user chose; `refresh` sends the state again at once, for a choice that changes it. */
function act(d: TrayMenuDeps, action: TrayAction, refresh: () => void): void {
  switch (action.kind) {
    case "logs":
      openLog(d.logFile)
      return
    case "restart":
      d.restart()
      return
    case "stop":
      say("stop requested from the tray menu")
      d.stop(EXIT_STOPPED)
      return
    case "disconnect":
      say(`disconnect ${action.origin} requested from the tray menu`)
      d.disconnect(action.origin).catch((e: unknown) => warn(`tray: cannot disconnect ${action.origin}: ${e instanceof Error ? e.message : String(e)}`))
      return
    case "updates":
      // A setting that could not be saved is sent back as it was, so the window's switch goes back too.
      d.setUpdates(action.on)
        .catch((e: unknown) => warn(`tray: cannot save the updates setting: ${e instanceof Error ? e.message : String(e)}`))
        .finally(refresh)
  }
}

/** Starts the tray, or the link to the app, and keeps it showing what the daemon serves. Returns what ends it. */
export function startTrayMenu(d: TrayMenuDeps): () => void {
  const push = (): void => {
    loadRegistry(d.registryFile).then(
      (r) => {
        const hello = d.hello()
        tray.update(
          trayState({
            version: hello.version,
            apps: r.apps.map((a) => a.server).filter(d.linked),
            browsers: r.browsers.map((b) => b.origin),
            sessions: d.sessions(),
            managed: hello.managed,
            gatewayPort: d.gatewayPort,
            logFile: d.logFile,
            agents: hello.agents,
            updates: d.updates(),
            updatable: d.updatable,
          }),
        )
      },
      () => undefined,
    )
  }
  const tray = d.open((action) => act(d, action, push))
  tray.start()
  push()
  const timer = setInterval(push, REFRESH_MS)
  return () => {
    clearInterval(timer)
    tray.stop()
  }
}
