// The command is pasted into a shell: an origin, a releases page or a code outside these patterns is refused instead of
// quoted. The app's origin pattern is the one agentd's installer checks (daemon/install.sh.in).
const SAFE_ORIGIN = /^https?:\/\/([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?$/
const SAFE_RELEASES = /^https?:\/\/([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?(\/[A-Za-z0-9._-]+)*$/
const SAFE_CODE = /^[A-Za-z0-9-]{4,32}$/

/** Where agentd publishes its releases: the desktop app, the installer, the daemon and the SDK are files of the latest one. */
export const AGENTD_RELEASES = "https://github.com/nguyenvanduocit/agentd/releases"

/** The disk images of agentd.app, the desktop app for macOS: one per processor. */
export interface DesktopDownloads {
  appleSilicon: string
  intel: string
}

/**
 * Where the user downloads agentd.app from the latest release (default: agentd's on GitHub; a fork or a development
 * server names its own). Opened, it runs agentd, and the page finds it and connects it as it finds an installed one.
 * Undefined while the releases page is not a safe http(s) URL.
 */
export function desktopDownloads(o: { agentd?: string } = {}): DesktopDownloads | undefined {
  const releases = o.agentd ?? AGENTD_RELEASES
  if (!SAFE_RELEASES.test(releases) || releases.split("/").includes("..")) return undefined
  return {
    appleSilicon: `${releases}/latest/download/agentd-darwin-arm64.dmg`,
    intel: `${releases}/latest/download/agentd-darwin-amd64.dmg`,
  }
}

/** How a person gets agentd on this computer: the disk image of agentd.app, the install command, or neither (Windows: use WSL). */
export type InstallRecommendation = "dmg" | "script" | "unsupported"

/** What the browser says about the computer it runs on; `arch` is reserved (both disk images are offered, so it does not change the answer). */
export interface Platform {
  /** `navigator.userAgentData.platform`, `navigator.platform` or the user agent: "macOS", "MacIntel", "Linux x86_64", "Win32" … */
  os?: string
  arch?: string
}

/** The platform the browser reports: `userAgentData.platform`, else `platform`, else the user agent. */
function browserPlatform(): Platform {
  const nav = (globalThis as { navigator?: { userAgentData?: { platform?: string }; platform?: string; userAgent?: string } }).navigator
  return { os: nav?.userAgentData?.platform || nav?.platform || nav?.userAgent || "" }
}

/**
 * Which way to install agentd on the computer this page is open on: "dmg" on a Mac (agentd.app), "script" on Linux (the
 * command of `installCommand`), "unsupported" anywhere else (Windows: use WSL; phones and tablets have no daemon).
 * Without an argument it reads the browser. A phone's user agent says Linux or Mac OS X, so those are refused first.
 */
export function recommendInstall(platform: Platform = browserPlatform()): InstallRecommendation {
  const os = (platform.os ?? "").toLowerCase()
  if (/android|iphone|ipad|ipod|cros/.test(os)) return "unsupported"
  if (/mac|darwin/.test(os)) return "dmg"
  if (/linux/.test(os)) return "script"
  return "unsupported"
}

export interface InstallOptions {
  /** The releases page the installer comes from (default: agentd's on GitHub; a fork or a development server names its own). */
  agentd?: string
  /** The origin of the app the daemon will pair with: the one that minted `code`. */
  app: string
  /** The one-time pairing code the app's server gave. */
  code: string
}

/**
 * The one command that installs agentd on the user's machine, pairs it with the app and starts it. Undefined while the
 * origins or the code are not safe to put in a command.
 */
export function installCommand(o: InstallOptions): string | undefined {
  const releases = o.agentd ?? AGENTD_RELEASES
  if (!SAFE_RELEASES.test(releases) || releases.split("/").includes("..") || !SAFE_ORIGIN.test(o.app) || !SAFE_CODE.test(o.code)) return undefined
  return `curl -fsSL ${releases}/latest/download/install.sh | bash -s -- --app ${o.app} --code ${o.code}`
}

/** What a user runs on the machine that has the daemon: the service's state, where its log is read, how to start it again. */
export const AGENTD_COMMANDS = {
  status: "agentd service status",
  logs: "~/.agentd/bin/agentd service logs",
  start: "agentd service start",
} as const
