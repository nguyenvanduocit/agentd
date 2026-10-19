import { posix } from "node:path"

/** Everything the installer, the launcher and the daemon share under the daemon's home (~/.agentd). */
export interface Layout {
  home: string
  /** A Node.js the installer downloaded; its bin directory goes first on the launcher's PATH. */
  nodeDir: string
  /** <version>/daemon.js directories, `current` (a symlink to one of them) and the rollback records. */
  daemonDir: string
  currentLink: string
  /** The version before `current`: the launcher points `current` back at it after two quick crashes. */
  previousFile: string
  /** A version that crashed twice at start: the daemon does not update to it again until the server serves another. */
  rejectedFile: string
  /** "<version> <count>": consecutive quick crashes of one version, kept by the launcher. */
  crashesFile: string
  launcher: string
  logFile: string
  /** The process id of a launcher that runs in restart-loop mode (no service manager). */
  loopPidFile: string
  /** The adapters of the agents (claude, codex): adapters/<agent>/<version>, installed once by the daemon. */
  adaptersDir: string
  /** The exit code of the last daemon run under the restart loop; it tells a Stop (0) from a crash. */
  lastExitFile: string
}

export function layoutOf(home: string): Layout {
  const daemonDir = posix.join(home, "daemon")
  return {
    home,
    nodeDir: posix.join(home, "node"),
    daemonDir,
    currentLink: posix.join(daemonDir, "current"),
    previousFile: posix.join(daemonDir, "previous"),
    rejectedFile: posix.join(daemonDir, "rejected"),
    crashesFile: posix.join(daemonDir, "crashes"),
    launcher: posix.join(home, "bin", "agentd"),
    logFile: posix.join(home, "logs", "daemon.log"),
    loopPidFile: posix.join(home, "run", "loop.pid"),
    adaptersDir: posix.join(home, "adapters"),
    lastExitFile: posix.join(home, "run", "last-exit"),
  }
}

export function versionDir(l: Layout, version: string): string {
  return posix.join(l.daemonDir, version)
}

/**
 * The version a daemon script belongs to when it is an installed one, `<home>/daemon/<version>/daemon.js` (both
 * paths already resolved with realpath); undefined for a source run, a test bundle or an npx copy. Only an installed
 * daemon updates itself: anything else would write into a home it was never installed in.
 */
export function installedVersion(realScript: string, l: Layout): string | undefined {
  if (posix.basename(realScript) !== "daemon.js") return undefined
  const dir = posix.dirname(realScript)
  return posix.dirname(dir) === l.daemonDir ? posix.basename(dir) : undefined
}
