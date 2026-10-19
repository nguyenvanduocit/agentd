import { spawn } from "node:child_process"
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, readlinkSync, rmSync, statSync } from "node:fs"
import { basename, dirname, resolve } from "node:path"
import type { Command } from "./core/cli.ts"
import { layoutOf } from "./core/layout.ts"
import { agentdHome } from "./core/paths.ts"
import { serviceFile, serviceName, stateText, type ManagerName, type Running } from "./core/service.ts"
import { currentHost } from "./host.ts"
import { launchd } from "./launchd.ts"
import { say, warn } from "./log.ts"
import type { Manager, ServiceCtx } from "./manager.ts"
import { exec, sleep } from "./proc.ts"
import { systemd } from "./systemd.ts"

const TAIL = "/usr/bin/tail"

type ServiceCommand = Extract<Command, { kind: "service" }>

/** launchd on macOS and systemd --user on Linux when they answer; otherwise the launcher's own restart loop. */
async function detect(): Promise<ManagerName> {
  const forced = process.env.AGENTD_SERVICE_MANAGER
  if (forced === "loop" || forced === "launchd" || forced === "systemd") return forced
  if (process.platform === "darwin" && (await exec("launchctl", ["print", `gui/${process.getuid?.() ?? 0}`])).code === 0) return "launchd"
  if (process.platform === "linux" && (await exec("systemctl", ["--user", "show-environment"])).code === 0) return "systemd"
  return "loop"
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "EPERM"
  }
}

function readPid(file: string): number | undefined {
  try {
    const pid = Number(readFileSync(file, "utf8").trim())
    return Number.isInteger(pid) && pid > 1 && alive(pid) ? pid : undefined
  } catch {
    return undefined
  }
}

/** No service manager: the launcher runs in the background and restarts the daemon with backoff. It does not outlive a reboot. */
async function spawnLoop(c: ServiceCtx): Promise<void> {
  mkdirSync(dirname(c.layout.logFile), { recursive: true })
  const log = openSync(c.layout.logFile, "a")
  try {
    const child = spawn(c.layout.launcher, ["run"], { detached: true, stdio: ["ignore", log, log], cwd: c.workDir ?? c.home, env: { ...process.env, AGENTD_RESTART: "loop" } })
    child.unref()
  } finally {
    closeSync(log)
  }
  for (let i = 0; i < 30 && readPid(c.layout.loopPidFile) === undefined; i++) await sleep(100)
}

async function endLoop(c: ServiceCtx): Promise<void> {
  const pid = readPid(c.layout.loopPidFile)
  if (pid === undefined) return
  process.kill(pid, "SIGTERM")
  for (let i = 0; i < 100 && alive(pid); i++) await sleep(100)
  rmSync(c.layout.loopPidFile, { force: true })
}

function loopExit(c: ServiceCtx): number | undefined {
  try {
    const n = Number(readFileSync(c.layout.lastExitFile, "utf8").trim())
    return Number.isInteger(n) ? n : undefined
  } catch {
    return undefined
  }
}

const loop: Manager = {
  async install(c) {
    await endLoop(c)
    await spawnLoop(c)
    return ["no service manager here: the daemon runs in the background and restarts when it crashes, but it does not start at login or after a reboot; run `agentd service install` again then"]
  },

  uninstall: endLoop,

  status(c) {
    const pid = readPid(c.layout.loopPidFile)
    if (pid !== undefined) return Promise.resolve({ running: true, pid, path: undefined, lastExit: undefined, pending: false })
    const lastExit = loopExit(c)
    return Promise.resolve(lastExit === undefined ? undefined : { running: false, pid: undefined, path: undefined, lastExit, pending: lastExit !== 0 })
  },

  async start(c) {
    if (readPid(c.layout.loopPidFile) === undefined) await spawnLoop(c)
  },

  async restart(c) {
    await endLoop(c)
    await spawnLoop(c)
  },
}

const MANAGERS: Record<ManagerName, Manager> = { launchd, systemd, loop }

function context(manager: ManagerName, dir: string | undefined): ServiceCtx {
  const host = currentHost()
  const name = serviceName(manager, host.env)
  const file = manager === "loop" ? "" : serviceFile(manager, name, host.home, host.env)
  return { layout: layoutOf(agentdHome(host)), env: host.env, home: host.home, workDir: dir, name, file }
}

function installedVersion(c: ServiceCtx): string {
  try {
    return basename(readlinkSync(c.layout.currentLink))
  } catch {
    return "none"
  }
}

function describe(manager: ManagerName, c: ServiceCtx, now: Running | undefined): string[] {
  const where = manager === "loop" ? "restart loop (no service manager)" : `${manager}, ${c.name}`
  return [
    `agentd service: ${where}`,
    `  state:   ${stateText(now)}`,
    `  version: ${installedVersion(c)}`,
    `  log:     ${c.layout.logFile}`,
    ...(c.file === "" ? [] : [`  file:    ${c.file}`]),
  ]
}

function launcherReady(c: ServiceCtx): void {
  if (!existsSync(c.layout.launcher)) {
    throw new Error(`${c.layout.launcher} is missing: install the daemon first with the command from the app's web UI`)
  }
}

async function logs(c: ServiceCtx, cmd: ServiceCommand): Promise<number> {
  if (!existsSync(c.layout.logFile)) {
    warn(`no log yet: ${c.layout.logFile}`)
    return 1
  }
  const n = String(cmd.lines)
  if (cmd.follow) {
    const tail = spawn(TAIL, ["-n", n, "-f", c.layout.logFile], { stdio: "inherit" })
    return new Promise((resolve) => tail.once("exit", (code) => resolve(code ?? 0)))
  }
  const r = await exec(TAIL, ["-n", n, c.layout.logFile])
  process.stdout.write(r.stdout)
  return r.code
}

async function show(manager: ManagerName, c: ServiceCtx, m: Manager): Promise<void> {
  for (const line of describe(manager, c, await m.status(c))) say(line)
}

/** `service install --dir <dir>`: an existing directory, as an absolute path; undefined is the user's home. */
function serviceDir(dir: string | undefined): string | undefined {
  if (dir === undefined) return undefined
  const abs = resolve(dir)
  if (!statSync(abs, { throwIfNoEntry: false })?.isDirectory()) throw new Error(`--dir ${abs} is not a directory`)
  return abs
}

async function status(manager: ManagerName, c: ServiceCtx, m: Manager): Promise<number> {
  const now = await m.status(c)
  for (const line of describe(manager, c, now)) say(line)
  return now?.running ? 0 : 1
}

/** `agentd service install|uninstall|start|restart|status|logs`; resolves to the exit code. */
export async function runService(cmd: ServiceCommand): Promise<number> {
  const manager = await detect()
  const c = context(manager, cmd.action === "install" ? serviceDir(cmd.dir) : undefined)
  const m = MANAGERS[manager]
  switch (cmd.action) {
    case "install":
      launcherReady(c)
      for (const line of await m.install(c)) say(line)
      await show(manager, c, m)
      return 0
    case "start":
    case "restart":
      launcherReady(c)
      await m[cmd.action](c)
      await show(manager, c, m)
      return 0
    case "uninstall":
      await m.uninstall(c)
      say(`removed the ${manager} service ${c.name}; ${c.layout.home} stays (rm -rf it to remove the rest)`)
      return 0
    case "status":
      return status(manager, c, m)
    case "logs":
      return logs(c, cmd)
  }
}
