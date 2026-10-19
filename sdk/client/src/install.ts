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
