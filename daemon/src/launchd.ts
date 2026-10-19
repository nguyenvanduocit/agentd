import { existsSync, mkdirSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs"
import { dirname } from "node:path"
import { parseLaunchctlPrint, plist, serviceEnv, type Running } from "./core/service.ts"
import type { Manager, ServiceCtx } from "./manager.ts"
import { exec, sleep } from "./proc.ts"

const uid = (): number => process.getuid?.() ?? 0
const domain = (): string => `gui/${uid()}`

function real(file: string): string {
  try {
    return realpathSync(file)
  } catch {
    return file
  }
}

/** The service as launchd has it loaded, or undefined when it is not loaded. */
async function loaded(c: ServiceCtx): Promise<Running | undefined> {
  const r = await exec("launchctl", ["print", `${domain()}/${c.name}`])
  return r.code === 0 ? parseLaunchctlPrint(r.stdout) : undefined
}

async function unload(c: ServiceCtx): Promise<void> {
  await exec("launchctl", ["bootout", `${domain()}/${c.name}`])
  for (let i = 0; i < 50 && (await loaded(c)); i++) await sleep(200)
}

/** bootstrap right after bootout can fail with EIO while launchd still tears the old job down: try a few times. */
async function load(c: ServiceCtx): Promise<void> {
  let last = ""
  for (let i = 0; i < 8; i++) {
    const r = await exec("launchctl", ["bootstrap", domain(), c.file])
    if (r.code === 0) return
    last = (r.stderr || r.stdout).trim()
    await sleep(500)
  }
  throw new Error(`launchctl bootstrap ${c.file} failed: ${last}`)
}

function writeAtomic(file: string, text: string): void {
  mkdirSync(dirname(file), { recursive: true })
  const tmp = `${file}.${process.pid}.tmp`
  writeFileSync(tmp, text, { mode: 0o644 })
  renameSync(tmp, file)
}

/** A label that is loaded from another file is another install's service: leave it alone. */
function refuseForeign(c: ServiceCtx, now: Running | undefined): void {
  if (now?.path !== undefined && real(now.path) !== real(c.file)) {
    throw new Error(`launchd already runs a service named ${c.name} from ${now.path}, not from ${c.file}: not touching it`)
  }
}

/** Starts a loaded job now; with `kill`, ends the running instance first. */
async function kickstart(c: ServiceCtx, kill: boolean): Promise<void> {
  const r = await exec("launchctl", ["kickstart", ...(kill ? ["-k"] : []), `${domain()}/${c.name}`])
  if (r.code !== 0) throw new Error(`launchctl kickstart failed: ${(r.stderr || r.stdout).trim()}`)
}

export const launchd: Manager = {
  async install(c) {
    mkdirSync(dirname(c.layout.logFile), { recursive: true })
    const text = plist({ label: c.name, launcher: c.layout.launcher, workDir: c.workDir ?? c.home, logFile: c.layout.logFile, env: serviceEnv(c.env) })
    const now = await loaded(c)
    refuseForeign(c, now)
    writeAtomic(c.file, text)
    await exec("launchctl", ["enable", `${domain()}/${c.name}`])
    if (now) await unload(c)
    await load(c)
    return [`started the launchd agent ${c.name} (${c.file}); it starts at login and restarts when the daemon exits`]
  },

  async uninstall(c) {
    const now = await loaded(c)
    if (now && (now.path === undefined || real(now.path) === real(c.file))) await unload(c)
    rmSync(c.file, { force: true })
  },

  async status(c) {
    return loaded(c)
  },

  async start(c) {
    const now = await loaded(c)
    if (!now) {
      if (!existsSync(c.file)) throw new Error(`${c.name} is not installed: run agentd service install`)
      await load(c)
      return
    }
    refuseForeign(c, now)
    if (!now.running) await kickstart(c, false)
  },

  async restart(c) {
    const now = await loaded(c)
    if (!now) return launchd.start(c)
    refuseForeign(c, now)
    await kickstart(c, true)
  },
}
