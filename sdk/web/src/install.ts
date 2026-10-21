// A releases page outside this pattern is refused instead of being put into a download link.
const SAFE_RELEASES = /^https?:\/\/([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?(\/[A-Za-z0-9._-]+)*$/

/** Where agentd publishes its releases: the Mac app and the SDK are files of the latest one. */
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

/** How a person gets agentd on this computer: the disk image of agentd.app on a Mac, or not at all. */
export type InstallRecommendation = "dmg" | "unsupported"

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
 * Which way to install agentd on the computer this page is open on: "dmg" on a Mac (agentd.app), "unsupported" on
 * Linux, Windows, phones and tablets. Without an argument it reads the browser. A phone's user agent says Linux or
 * Mac OS X, so those are refused first.
 */
export function recommendInstall(platform: Platform = browserPlatform()): InstallRecommendation {
  const os = (platform.os ?? "").toLowerCase()
  if (/android|iphone|ipad|ipod|cros/.test(os)) return "unsupported"
  if (/mac|darwin/.test(os)) return "dmg"
  return "unsupported"
}
