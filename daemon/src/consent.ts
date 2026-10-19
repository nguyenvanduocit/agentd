import { execFile } from "node:child_process"
import type { Env } from "./core/executable.ts"
import { answerOf, consentText, dialogPlan, type Answer, type Question } from "./core/consent.ts"
import { findProgram } from "./host.ts"

export const CONSENT_TIMEOUT_S = 60

interface Exit {
  code: number
  stdout: string
  timedOut: boolean
  /** The program could not be run at all. */
  failed: boolean
}

function exitOf(err: Error | null, stdout: string): Exit {
  if (err === null) return { code: 0, stdout, timedOut: false, failed: false }
  const e = err as NodeJS.ErrnoException & { killed?: boolean; code?: number | string }
  if (e.killed === true) return { code: -1, stdout, timedOut: true, failed: false }
  if (typeof e.code === "number") return { code: e.code, stdout, timedOut: false, failed: false }
  return { code: -1, stdout, timedOut: false, failed: true }
}

function run(file: string, args: string[], timeoutS: number): Promise<Exit> {
  return new Promise((resolve) => {
    // The dialog tools stop by themselves after timeoutS; this is for those that cannot (kdialog) or that hang.
    execFile(file, args, { timeout: (timeoutS + 5) * 1000, encoding: "utf8" }, (err, stdout) => {
      resolve(exitOf(err, stdout))
    })
  })
}

/**
 * Asks the user in a native dialog. `AGENTD_CONSENT` (a test seam, like AGENTD_AGENT_<NAME>) names a program
 * that is run instead, with the title and the body as arguments; exit 0 means Allow. "unavailable" means the machine
 * has no way to ask (no tool, no graphical session): the caller must not treat it as consent.
 */
export async function ask(q: Question, env: Env, timeoutS = CONSENT_TIMEOUT_S): Promise<Answer> {
  const text = consentText(q)
  const seam = env.AGENTD_CONSENT
  const plan = seam ? { kind: undefined, file: seam, args: [text.title, text.body] } : dialogPlan(process.platform, (c) => findProgram(c, env) !== undefined, text, timeoutS)
  if (plan === undefined) return "unavailable"
  const r = await run(plan.file, plan.args, timeoutS)
  if (r.timedOut) return "timeout"
  if (r.failed) return "unavailable"
  if (plan.kind === undefined) return r.code === 0 ? "allow" : "deny"
  return answerOf(plan.kind, r.code, r.stdout)
}
