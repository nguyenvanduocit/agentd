import { mkdirSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs"
import { dirname } from "node:path"
import { lingerOff, parseSystemctlShow, serviceEnv, systemdUnit, type Running } from "./core/service.ts"
import type { Manager, ServiceCtx } from "./manager.ts"
import { exec } from "./proc.ts"

const unit = (c: ServiceCtx): string => `${c.name}.service`
const ctl = (...args: string[]) => exec("systemctl", ["--user", ...args])

function real(file: string): string {
  try {
    return realpathSync(file)
  } catch {
    return file
  }
}

async function show(c: ServiceCtx): Promise<(Running & { loaded: boolean }) | undefined> {
  const r = await ctl("show", "-p", "LoadState,ActiveState,ExecMainStatus,MainPID,FragmentPath", unit(c))
  if (r.code !== 0) return undefined
  return { ...parseSystemctlShow(r.stdout), loaded: /^LoadState=loaded$/m.test(r.stdout) }
}

async function lingerHint(): Promise<string[]> {
  const user = process.env.USER ?? ""
  const r = await exec("loginctl", ["show-user", user, "-p", "Linger"])
  if (r.code !== 0 || !lingerOff(r.stdout)) return []
  return [`linger is off: the daemon stops when you log out. To keep it running without a login, run: loginctl enable-linger ${user}`]
}

async function systemctl(verb: string, c: ServiceCtx): Promise<void> {
  const r = await ctl(verb, unit(c))
  if (r.code !== 0) throw new Error(`systemctl --user ${verb} ${unit(c)} failed: ${(r.stderr || r.stdout).trim()}`)
}

export const systemd: Manager = {
  async install(c) {
    const now = await show(c)
    if (now?.loaded && now.path !== undefined && real(now.path) !== real(c.file)) {
      throw new Error(`systemd already has a unit named ${unit(c)} from ${now.path}, not from ${c.file}: not touching it`)
    }
    mkdirSync(dirname(c.layout.logFile), { recursive: true })
    mkdirSync(dirname(c.file), { recursive: true })
    const tmp = `${c.file}.${process.pid}.tmp`
    writeFileSync(tmp, systemdUnit({ launcher: c.layout.launcher, workDir: c.workDir, logFile: c.layout.logFile, env: serviceEnv(c.env) }), { mode: 0o644 })
    renameSync(tmp, c.file)
    for (const args of [["daemon-reload"], ["enable", unit(c)], ["restart", unit(c)]]) {
      const r = await ctl(...args)
      if (r.code !== 0) throw new Error(`systemctl --user ${args.join(" ")} failed: ${(r.stderr || r.stdout).trim()}`)
    }
    return [`started the systemd user unit ${unit(c)} (${c.file}); it starts at login and restarts when the daemon exits`, ...(await lingerHint())]
  },

  async uninstall(c) {
    const now = await show(c)
    if (now?.loaded && (now.path === undefined || real(now.path) === real(c.file))) await ctl("disable", "--now", unit(c))
    rmSync(c.file, { force: true })
    await ctl("daemon-reload")
  },

  async status(c) {
    const now = await show(c)
    return now?.loaded ? now : undefined
  },

  async start(c) {
    await systemctl("start", c)
  },

  async restart(c) {
    await systemctl("restart", c)
  },
}
