import { accessSync, chmodSync, constants, existsSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs"
import { userInfo } from "node:os"
import { dirname, join } from "node:path"
import { appCandidates, appEnv, appPlace, desktopEntry, entryLauncher, infoPlist, isOurDesktopEntry, isOurPlist, macAppScript } from "./core/appentry.ts"
import type { Layout } from "./core/layout.ts"
import type { Host } from "./core/paths.ts"

/** The account's own home, from the user database: HOME may be a test's. */
function accountHome(): string {
  return userInfo().homedir
}

function writable(dir: string): boolean {
  try {
    accessSync(dir, constants.W_OK)
    return true
  } catch {
    return false
  }
}

/** Whether two paths name one file (macOS reaches /var by /private/var too); a path to nothing names no file. */
function sameFile(a: string | undefined, b: string): boolean {
  if (a === undefined) return false
  try {
    return realpathSync(a) === realpathSync(b)
  } catch {
    return false
  }
}

/** Writes `data` to `file` unless it holds it already: Spotlight and Launchpad look again at what changes. */
function put(file: string, data: string | Buffer, mode = 0o644): void {
  const next = typeof data === "string" ? Buffer.from(data) : data
  if (existsSync(file) && readFileSync(file).equals(next)) return
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, next)
  chmodSync(file, mode)
}

/**
 * Makes the app that starts agentd again (core/appentry.ts) for the daemon installed in `l`, at `version`, and returns
 * where it is, or undefined when this install makes none. `install` (service install) writes all of it: its script
 * names the service it starts. `refresh` (a daemon that a service runs, at start) keeps the icon and version current and
 * writes the script only when it is missing (an install from before the app existed) or starts another launcher (a
 * test's, gone): a daemon does not see the name of its own service, so it leaves a script of its own install alone.
 * The icons come from the installed version (app/).
 */
export function writeApp(l: Layout, h: Host, version: string, how: "install" | "refresh"): string | undefined {
  const place = appPlace(h.platform, h.home, accountHome(), h.env, writable)
  if (place === undefined) return undefined
  const env = appEnv(h.env)
  const icons = join(l.currentLink, "app")
  const script = place.kind === "linux" ? place.file : join(place.file, "Contents", "MacOS", "agentd")
  const writeScript = how === "install" || !sameFile(entryLauncher(existsSync(script) ? readFileSync(script, "utf8") : ""), l.launcher)
  if (place.kind === "linux") {
    if (writeScript) put(place.file, desktopEntry(l.launcher, env, join(icons, "agentd.png")))
    return place.file
  }
  put(join(place.file, "Contents", "Info.plist"), infoPlist(version))
  if (writeScript) put(script, macAppScript(l.launcher, env), 0o755)
  const icns = join(icons, "agentd.icns")
  if (existsSync(icns)) put(join(place.file, "Contents", "Resources", "agentd.icns"), readFileSync(icns))
  return place.file
}

/** Removes every app writeApp made: only agentd's own (its bundle id, its desktop entry's mark). Returns what it removed. */
export function removeApps(h: Host): string[] {
  const removed: string[] = []
  for (const file of appCandidates(h.platform, h.home, accountHome(), h.env)) {
    const mac = file.endsWith(".app")
    let text: string
    try {
      text = readFileSync(mac ? join(file, "Contents", "Info.plist") : file, "utf8")
    } catch {
      continue
    }
    if (!(mac ? isOurPlist(text) : isOurDesktopEntry(text))) continue
    rmSync(file, { recursive: true, force: true })
    removed.push(file)
  }
  return removed
}
