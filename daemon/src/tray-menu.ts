import { spawn } from "node:child_process"
import { existsSync, realpathSync } from "node:fs"
import { dirname } from "node:path"
import type { Hello } from "../../contract/define.acp.ts"
import { loadRegistry } from "./config.ts"
import { EXIT_STOPPED } from "./core/exit.ts"
import { openCommand, trayBinary, trayState, type TrayAction } from "./core/tray.ts"
import { Tray } from "./tray.ts"
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

export interface TrayMenuDeps {
  file: string
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
}

function openLog(logFile: string | undefined): void {
  if (logFile === undefined) {
    say("tray: no log file: this daemon writes to its terminal")
    return
  }
  const cmd = openCommand(process.platform, logFile)
  spawn(cmd.file, cmd.args, { stdio: "ignore", detached: true }).on("error", (e) => warn(`tray: cannot open the log: ${e.message}`)).unref()
}

function act(d: TrayMenuDeps, action: TrayAction): void {
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
  }
}

/** Starts the tray and keeps it showing what the daemon serves. Returns what ends it. */
export function startTrayMenu(d: TrayMenuDeps): () => void {
  const tray = new Tray(d.file, (action) => act(d, action))
  const push = (): void => {
    loadRegistry(d.registryFile).then(
      (r) => {
        const hello = d.hello()
        tray.update(trayState({ version: hello.version, apps: r.apps.map((a) => a.server).filter(d.linked), browsers: r.browsers.map((b) => b.origin), sessions: d.sessions(), managed: hello.managed, gatewayPort: d.gatewayPort }))
      },
      () => undefined,
    )
  }
  tray.start()
  push()
  const timer = setInterval(push, REFRESH_MS)
  return () => {
    clearInterval(timer)
    tray.stop()
  }
}
