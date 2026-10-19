import type { Layout } from "./core/layout.ts"
import type { Running } from "./core/service.ts"

/** What one service manager (launchd, systemd) needs to know about the service it controls. */
export interface ServiceCtx {
  layout: Layout
  /** The environment of the shell that runs `service install`: PATH and the test seams are copied from it. */
  env: Readonly<Record<string, string | undefined>>
  /** The user's home: the daemon's working directory unless `service install --dir` names another. */
  home: string
  workDir: string | undefined
  /** launchd label or systemd unit name. */
  name: string
  /** The definition file: the plist or the unit. */
  file: string
}

export interface Manager {
  /** Writes the definition and starts the service; when it runs already, restarts it so it runs the current version. */
  install(c: ServiceCtx): Promise<string[]>
  /** Stops the service and removes its definition; leaves ~/.agentd. */
  uninstall(c: ServiceCtx): Promise<void>
  status(c: ServiceCtx): Promise<Running | undefined>
  /** Starts a stopped service (after a Stop from the web UI); one that runs is left alone. */
  start(c: ServiceCtx): Promise<void>
  /** Ends the daemon and starts it again, whatever state it was in. */
  restart(c: ServiceCtx): Promise<void>
}
