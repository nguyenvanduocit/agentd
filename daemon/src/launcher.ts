import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs"
import { dirname, join } from "node:path"
import type { Layout } from "./core/layout.ts"

/**
 * Puts the launcher that ships beside `daemon.js` (launcher.sh) at ~/.agentd/bin/agentd when that file
 * differs. A new version can fix the launcher; the file is replaced by rename, so a running launcher keeps its copy.
 * Returns whether it wrote.
 */
export function refreshLauncher(scriptDir: string, l: Layout): boolean {
  const shipped = join(scriptDir, "launcher.sh")
  if (!existsSync(shipped)) return false
  const text = readFileSync(shipped)
  if (existsSync(l.launcher) && readFileSync(l.launcher).equals(text)) return false
  mkdirSync(dirname(l.launcher), { recursive: true })
  const tmp = `${l.launcher}.${process.pid}.tmp`
  writeFileSync(tmp, text)
  chmodSync(tmp, 0o755)
  renameSync(tmp, l.launcher)
  return true
}
