/**
 * The daemon's exit codes are its protocol with whatever restarts it (launchd, systemd, the launcher's loop):
 *   0   stay stopped: a Stop from the web UI, a SIGTERM from the service manager, a revoked token
 *   75  start again now: a Restart from the web UI, an update that is in place (EX_TEMPFAIL)
 *   any other code is a crash; the service restarts it after its throttle.
 * launcher.sh carries the same 75.
 */
export const EXIT_STOPPED = 0
export const EXIT_RESTART = 75

export type ExitMeaning = "stopped" | "restart" | "crash"

export function classifyExit(code: number): ExitMeaning {
  if (code === EXIT_STOPPED) return "stopped"
  return code === EXIT_RESTART ? "restart" : "crash"
}

export type Control = "restart" | "stop"

export type ControlOutcome = { kind: "exit"; code: number } | { kind: "ignore"; why: string }

/**
 * What a Restart or Stop from the server does. A daemon that no service restarts would exit and stay gone on Restart,
 * so it ignores it; Stop is always honoured.
 */
export function controlOutcome(control: Control, managed: boolean): ControlOutcome {
  if (control === "stop") return { kind: "exit", code: EXIT_STOPPED }
  return managed ? { kind: "exit", code: EXIT_RESTART } : { kind: "ignore", why: "not managed: restart ignored" }
}

/** AGENTD_MANAGED=1 is set by the service definitions and by the launcher's restart loop. */
export function isManaged(env: Readonly<Record<string, string | undefined>>): boolean {
  return env.AGENTD_MANAGED === "1"
}
