import { spawn } from "node:child_process"
import { join } from "node:path"
import { taskkillArgs } from "./core/spawnplan.ts"

// The system copy, not whatever the PATH names taskkill.
const TASKKILL = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "taskkill.exe")

/**
 * Signals the process group led by `pgid` (POSIX) or ends the tree rooted at it (Windows). A group that is already
 * gone is not an error.
 */
export function signalGroup(pgid: number, hard: boolean): void {
  // No group to signal: -1 is a failed spawn (kill(1) is init), 0 the daemon's own group, 1 every process of the user (kill(-1)).
  if (pgid <= 1) return
  if (process.platform === "win32") {
    spawn(TASKKILL, taskkillArgs(pgid), { stdio: "ignore", windowsHide: true }).on("error", () => undefined)
    return
  }
  try {
    process.kill(-pgid, hard ? "SIGKILL" : "SIGTERM")
  } catch {
    /* ESRCH: the group is gone */
  }
}

const tracked = new Set<number>()

/** Remembers a process group that must not outlive the daemon (an adapter install). */
export function trackGroup(pgid: number): void {
  tracked.add(pgid)
}

export function untrackGroup(pgid: number): void {
  tracked.delete(pgid)
}

/** SIGKILLs every tracked group. The daemon calls it on every way out, including a fatal error. */
export function killTrackedGroups(): void {
  for (const pgid of tracked) signalGroup(pgid, true)
  tracked.clear()
}
