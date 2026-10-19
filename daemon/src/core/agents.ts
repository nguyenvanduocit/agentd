import { ADAPTERS, installingReason, type AdapterName, type AdapterState } from "./adapters.ts"
import type { Env } from "./executable.ts"

export interface AgentCommand {
  command: string
  args: string[]
}

export interface AgentStatus {
  name: string
  ready: boolean
  reason: string
}

interface ProgramDef {
  /** The command the daemon runs; the server only ever names the agent. */
  command: AgentCommand
  /** The program that must be on PATH, and what to tell the user when it is not. */
  needs: { program: string; reason: string }
}

/** Agents that run a program from the user's PATH. claude and codex run an adapter the daemon installs (core/adapters.ts). */
const PROGRAMS = {
  grok: {
    command: { command: "grok", args: ["agent", "--always-approve", "stdio"] },
    needs: { program: "grok", reason: "grok not found in PATH" },
  },
} as const satisfies Record<string, ProgramDef>

export type AgentName = AdapterName | keyof typeof PROGRAMS

export const AGENT_NAMES: readonly AgentName[] = ["claude", "codex", "grok"]

export function isAgentName(name: string): name is AgentName {
  return (AGENT_NAMES as readonly string[]).includes(name)
}

/**
 * TEST SEAM: AGENTD_AGENT_<NAME> (for example AGENTD_AGENT_CLAUDE="/path/fake-agent --flag") replaces the
 * agent's command line entirely and marks the agent ready. Tests and e2e use it to run a fake agent; it is not a
 * user feature, and the server cannot set it.
 */
export function seamVariable(name: AgentName): string {
  return `AGENTD_AGENT_${name.toUpperCase()}`
}

// A word is a run of plain characters and quoted spans, so --flag="a b" is one word.
const WORD = /(?:[^\s"']|"[^"]*"|'[^']*')+/g
const QUOTED = /"([^"]*)"|'([^']*)'/g

/** Splits a command line into words. Single and double quotes group characters; there are no escapes. */
export function splitCommand(line: string): string[] {
  if (/["']/.test(line.replace(WORD, ""))) throw new Error(`unterminated quote in command line: ${line}`)
  return (line.match(WORD) ?? []).map((w) => w.replace(QUOTED, (_, d: string | undefined, s: string | undefined) => d ?? s ?? ""))
}

/** The adapter's state as the agent sees it: installed (ready), being installed, or failed. */
export type AdapterOf = (name: AdapterName) => AdapterState | undefined

function seamed(name: AgentName, env: Env): boolean {
  const seam = env[seamVariable(name)]
  return seam !== undefined && seam.trim() !== ""
}

/**
 * The command that starts `name`: the seam's, or `node <adapter's bin>` for an installed adapter, or the program on PATH.
 * `node` is the Node.js that runs the daemon.
 */
export function agentCommand(name: AgentName, env: Env, adapter: AdapterOf, node: string): AgentCommand {
  const override = env[seamVariable(name)]
  if (override !== undefined && override.trim() !== "") {
    const [command, ...args] = splitCommand(override)
    if (command === undefined) throw new Error(`${seamVariable(name)} holds no command`)
    return { command, args }
  }
  if (name === "grok") return { command: PROGRAMS.grok.command.command, args: [...PROGRAMS.grok.command.args] }
  const state = adapter(name)
  if (state?.kind !== "ready") throw new Error(`the ${name} adapter is not installed`)
  return { command: node, args: [state.bin] }
}

/** Whether the machine can start `name` now, and if not, why. */
export function agentStatus(name: AgentName, env: Env, find: (program: string) => string | undefined, adapter: AdapterOf): AgentStatus {
  if (seamed(name, env)) return { name, ready: true, reason: "" }
  if (name === "grok") {
    const { program, reason } = PROGRAMS.grok.needs
    return find(program) ? { name, ready: true, reason: "" } : { name, ready: false, reason }
  }
  const state = adapter(name)
  if (state?.kind === "ready") return { name, ready: true, reason: "" }
  return { name, ready: false, reason: state?.kind === "failed" ? state.reason : installingReason(ADAPTERS[name]) }
}

/** Why `open` for this agent is refused, or undefined when it can start. The server sees this text. */
export function openRefusal(name: AgentName, env: Env, find: (program: string) => string | undefined, adapter: AdapterOf): string | undefined {
  const status = agentStatus(name, env, find, adapter)
  if (status.ready) return undefined
  if (!seamed(name, env) && name !== "grok" && adapter(name)?.kind !== "failed") return `the ${name} adapter is still installing on this machine`
  return `agent ${name} is not ready: ${status.reason}`
}

export function allAgentStatuses(env: Env, find: (program: string) => string | undefined, adapter: AdapterOf): AgentStatus[] {
  return AGENT_NAMES.map((n) => agentStatus(n, env, find, adapter))
}
