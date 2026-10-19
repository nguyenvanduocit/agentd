import { accessSync, chmodSync, constants, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { appCandidates, appEnv, appPlace, desktopEntry, infoPlist, isOurDesktopEntry, isOurPlist, macAppScript } from "./core/appentry.ts"
import type { Layout } from "./core/layout.ts"
import type { Host } from "./core/paths.ts"

function writable(dir: string): boolean {
  try {
    accessSync(dir, constants.W_OK)
    return true
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
 * names the service it starts. `refresh` (a daemon that a service runs, at start) only makes an app that is missing (an
 * install from before the app existed) and keeps the icon and version of one that is there: a daemon does not see the
 * name of its own service, so it never rewrites a script. The icons come from the installed version (app/).
 */
export function writeApp(l: Layout, h: Host, version: string, how: "install" | "refresh"): string | undefined {
  const place = appPlace(h.platform, h.home, h.env, writable)
  if (place === undefined) return undefined
  const env = appEnv(h.env)
  const icons = join(l.currentLink, "app")
  const script = place.kind === "linux" ? place.file : join(place.file, "Contents", "MacOS", "agentd")
  const writeScript = how === "install" || !existsSync(script)
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
  for (const file of appCandidates(h.platform, h.home, h.env)) {
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
