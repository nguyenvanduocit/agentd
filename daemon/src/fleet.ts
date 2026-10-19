import { spawn } from "node:child_process"
import type { AdapterName, AdapterState } from "./core/adapters.ts"
import { AGENT_NAMES, agentCommand, isAgentName, openRefusal } from "./core/agents.ts"
import type { Env } from "./core/executable.ts"
import { spawnPlan } from "./core/spawnplan.ts"
import { agentEnv } from "./core/wrapper.ts"
import type { Opened } from "../../contract/define.acp.ts"
import { findProgram } from "./host.ts"
import { say } from "./log.ts"
import { AgentSession, type FrameSink } from "./session.ts"
import type { SessionState } from "./state.ts"

export interface FleetOptions {
  /** The working directory of every agent. */
  cwd: string
  /** The directory that holds the agentd-mcp wrapper; it goes first on every agent's PATH. */
  binDir: string
  /** The daemon's environment. */
  env: Env
  /** What the adapter installs of claude and codex are doing. */
  adapter: (name: AdapterName) => AdapterState | undefined
  state: Pick<SessionState, "add" | "remove">
  /** The sink for the connection that is current now. */
  sink: () => FrameSink
  /** A session's agent is gone (it exited or was ended); its id may be opened again from now on. */
  ended?: (id: string) => void
}

/** The agents running on this machine, by session id. */
export class Fleet {
  private readonly sessions = new Map<string, AgentSession>()
  private draining = false

  constructor(private readonly o: FleetOptions) {}

  get size(): number {
    return this.sessions.size
  }

  async open(id: string, agent: string): Promise<Opened> {
    if (this.draining) throw new Error("this daemon is stopping")
    if (id === "") throw new Error("empty session id")
    if (this.sessions.has(id)) throw new Error(`session ${id} exists`)
    if (!isAgentName(agent)) throw new Error(`unknown agent "${agent}": this daemon runs ${AGENT_NAMES.join(", ")}`)
    const env = agentEnv(this.o.env, this.o.binDir, process.platform)
    const refusal = openRefusal(agent, env, (p) => findProgram(p, env), this.o.adapter)
    if (refusal !== undefined) throw new Error(refusal)
    const cmd = agentCommand(agent, env, this.o.adapter, process.execPath)
    // Resolved against the agent's PATH here, not left to spawn: whether spawn looks in the child's PATH or the
    // daemon's differs by platform and Node version.
    const found = findProgram(cmd.command, env)
    if (!found) throw new Error(`agent ${agent}: command "${cmd.command}" not found in PATH`)
    return this.start(id, found, cmd.args, env)
  }

  private async start(id: string, file: string, args: string[], env: Record<string, string>): Promise<Opened> {
    const began = Date.now()
    const plan = spawnPlan(file, args, process.platform)
    const child = spawn(plan.file, plan.args, {
      cwd: this.o.cwd,
      env,
      shell: plan.shell,
      detached: plan.detached,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"],
    })
    let pgid = -1
    const session = new AgentSession(id, child, this.o.sink(), () => {
      this.sessions.delete(id)
      this.o.state.remove(pgid)
      this.o.ended?.(id)
    })
    // Registered before the spawn settles: a socket that drops meanwhile still ends this agent.
    this.sessions.set(id, session)
    try {
      pgid = await session.started()
    } catch (e) {
      this.sessions.delete(id)
      throw new Error(`cannot start ${file}: ${e instanceof Error ? e.message : String(e)}`, { cause: e })
    }
    this.o.state.add(pgid)
    say(`started ${file} for session ${id} in ${Date.now() - began} ms`)
    return { pid: pgid, cwd: this.o.cwd }
  }

  async write(id: string, line: string): Promise<boolean> {
    return (await this.sessions.get(id)?.write(line)) ?? false
  }

  close(id: string): void {
    this.sessions.get(id)?.end()
  }

  /** The socket is gone, and with it the server's way to reach the agents: end them all. */
  endAll(): void {
    for (const s of this.sessions.values()) s.end()
  }

  /** Ends every agent and resolves once they are gone, or after `graceMs` (the stragglers are SIGKILLed anyway). No session opens after this. */
  async shutdown(graceMs: number): Promise<void> {
    this.draining = true
    this.endAll()
    const deadline = Date.now() + graceMs
    while (this.sessions.size > 0 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50))
  }
}
