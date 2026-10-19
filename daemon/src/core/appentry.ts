import { posix } from "node:path"
import type { Env } from "./executable.ts"
import { passthroughEnv } from "./service.ts"

/**
 * The app a user opens to start agentd again after a Stop (the menu's Stop leaves the service stopped, and the menu goes
 * with the daemon): agentd.app on macOS, a desktop entry on Linux. It runs `agentd service start` through the launcher.
 */

/** agentd.app's bundle id: uninstall removes an agentd.app only when it carries this one. */
export const BUNDLE_ID = "com.agentd.app"

export interface AppPlace {
  kind: "mac" | "linux"
  /** agentd.app (a directory) on macOS, agentd.desktop on Linux. */
  file: string
}

/** The directory AGENTD_APPS_DIR names, or "" when it names none; undefined when no app may be made at all. */
function appsDir(env: Env): string | undefined {
  const dir = env.AGENTD_APPS_DIR ?? ""
  // An agentd whose home was moved (tests, a trial copy) is not the user's install: it makes no app on its own.
  if (dir === "" && (env.AGENTD_HOME ?? "") !== "") return undefined
  return dir
}

function linuxApps(home: string, env: Env): string {
  const data = env.XDG_DATA_HOME ?? ""
  return posix.join(data === "" ? posix.join(home, ".local", "share") : data, "applications")
}

/**
 * Where the app goes, or undefined for none. macOS: /Applications when this user may write it (an admin may, without
 * sudo), else ~/Applications; both show in Spotlight and Launchpad. Linux: the user's desktop entries.
 */
export function appPlace(platform: NodeJS.Platform, home: string, env: Env, writable: (dir: string) => boolean): AppPlace | undefined {
  const dir = appsDir(env)
  if (dir === undefined) return undefined
  if (platform === "darwin") {
    const parent = dir || (writable("/Applications") ? "/Applications" : posix.join(home, "Applications"))
    return { kind: "mac", file: posix.join(parent, "agentd.app") }
  }
  if (platform === "linux") return { kind: "linux", file: posix.join(dir || linuxApps(home, env), "agentd.desktop") }
  return undefined
}

/** Every place an app may have been made: uninstall removes the ones that are agentd's. */
export function appCandidates(platform: NodeJS.Platform, home: string, env: Env): string[] {
  const dir = appsDir(env)
  if (dir === undefined) return []
  if (platform === "darwin") return dir ? [posix.join(dir, "agentd.app")] : ["/Applications/agentd.app", posix.join(home, "Applications", "agentd.app")]
  if (platform === "linux") return [posix.join(dir || linuxApps(home, env), "agentd.desktop")]
  return []
}

/**
 * The environment the app runs the launcher in: the service's own (passthroughEnv) and the name and place of the service
 * it was made for, which the service's definition leaves out, so that the app starts that service and no other.
 */
export function appEnv(env: Env): Record<string, string> {
  const out = passthroughEnv(env)
  for (const name of ["AGENTD_SERVICE_LABEL", "AGENTD_SERVICE_DIR"]) {
    const v = env[name]
    if (v !== undefined && v !== "") out[name] = v
  }
  return out
}

export function infoPlist(version: string): string {
  const entries: [string, string][] = [
    ["CFBundleIdentifier", BUNDLE_ID],
    ["CFBundleName", "agentd"],
    ["CFBundleDisplayName", "agentd"],
    ["CFBundleExecutable", "agentd"],
    ["CFBundleIconFile", "agentd"],
    ["CFBundlePackageType", "APPL"],
    ["CFBundleShortVersionString", version],
    ["CFBundleVersion", version],
  ]
  return [
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">',
    '<plist version="1.0">',
    "<dict>",
    ...entries.map(([k, v]) => `  <key>${k}</key>\n  <string>${v}</string>`),
    // No Dock icon: the icon in the menu bar is agentd's face; the app only starts it.
    "  <key>LSUIElement</key>\n  <true/>",
    "</dict>",
    "</plist>",
    "",
  ].join("\n")
}

export function isOurPlist(text: string): boolean {
  return text.includes(`<string>${BUNDLE_ID}</string>`)
}

/** A quote inside single quotes: end them, an escaped quote, open them again. */
const QUOTE_IN_QUOTES = "'\\''"

const shellQuote = (s: string): string => `'${s.replaceAll("'", QUOTE_IN_QUOTES)}'`

/** The variables of `env` a shell can export: a name that is not one is dropped. */
const exported = (env: Readonly<Record<string, string>>): [string, string][] => Object.entries(env).filter(([k]) => /^[A-Za-z_]\w*$/.test(k))

/**
 * agentd.app's executable. An app opened from the Finder gets a bare environment, so the script carries the one the
 * service was installed with (PATH, where Node.js is; a moved home or service name). Already running: a notification
 * says so, the click is not lost. A start that fails says what to run instead.
 */
export function macAppScript(launcher: string, env: Readonly<Record<string, string>>): string {
  return [
    "#!/bin/sh",
    "# agentd.app: starts the agentd service again (its icon comes back in the menu bar); says so when it already runs.",
    ...exported(env).map(([k, v]) => `export ${k}=${shellQuote(v)}`),
    `L=${shellQuote(launcher)}`,
    'if "$L" service status >/dev/null 2>&1; then',
    `  osascript -e 'display notification "agentd is running: its icon is in the menu bar." with title "agentd"'`,
    'elif ! "$L" service start >/dev/null 2>&1; then',
    `  osascript -e 'on run argv' -e 'display alert (item 1 of argv) message (item 2 of argv) giving up after 60' -e 'end run' "agentd did not start" "In a terminal: $L service status"`,
    "fi",
    "",
  ].join("\n")
}

/** An argument of a desktop entry's Exec key: in double quotes, with the characters the spec reserves escaped. */
const execQuote = (s: string): string => `"${s.replace(/(["`$\\])/g, "\\$1")}"`

/** agentd's desktop entry: it starts the service in the environment the service was installed with. */
export function desktopEntry(launcher: string, env: Readonly<Record<string, string>>, icon: string): string {
  const vars = exported(env)
    .map(([k, v]) => execQuote(k + "=" + v))
    .join(" ")
  return [
    "[Desktop Entry]",
    "Type=Application",
    "Name=agentd",
    "Comment=Start agentd again: it runs your coding agents for the web apps you allow",
    `Exec=env ${vars} ${execQuote(launcher)} service start`,
    `Icon=${icon}`,
    "Terminal=false",
    "Categories=Development;",
    "X-Agentd=1",
    "",
  ].join("\n")
}

export function isOurDesktopEntry(text: string): boolean {
  return text.split("\n").includes("X-Agentd=1")
}
