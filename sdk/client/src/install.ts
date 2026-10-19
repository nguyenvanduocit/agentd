// The command is pasted into a shell: an origin or a code outside these patterns is refused instead of quoted. The
// origin pattern is the one agentd's installer checks (server/install.sh.tmpl).
const SAFE_ORIGIN = /^https?:\/\/([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?$/
const SAFE_CODE = /^[A-Za-z0-9-]{4,32}$/

export interface InstallOptions {
  /** The origin of the agentd release server that serves the installer (https://host[:port], no path). */
  agentd: string
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
  if (!SAFE_ORIGIN.test(o.agentd) || !SAFE_ORIGIN.test(o.app) || !SAFE_CODE.test(o.code)) return undefined
  return `curl -fsSL ${o.agentd}/install.sh | bash -s -- --app ${o.app} --code ${o.code}`
}

/** What a user runs on the machine that has the daemon: the service's state, where its log is read, how to start it again. */
export const AGENTD_COMMANDS = {
  status: "agentd service status",
  logs: "~/.agentd/bin/agentd service logs",
  start: "agentd service start",
} as const
