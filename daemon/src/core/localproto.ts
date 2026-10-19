import type { AgentStatus } from "./agents.ts"

export type FrameKind = "out" | "err" | "exit"

/** What a page sends over /v1/session. */
export type ToDaemon = { t: "open"; id: string; agent: string } | { t: "write"; id: string; line: string } | { t: "close"; id: string }

/** What the daemon sends a page. A browser app is its own ACP client: `frame` carries the agent's lines as they are. */
export type FromDaemon =
  | { t: "hello"; version: string; agents: AgentStatus[] }
  | { t: "opened"; id: string; pid: number; cwd: string }
  | { t: "error"; id: string; message: string }
  | { t: "frame"; id: string; kind: FrameKind; data: string; code: number }

/** A message of a page, or undefined when it is not one of the three the daemon accepts. */
export function parseToDaemon(text: string): ToDaemon | undefined {
  let m: { t?: unknown; id?: unknown; agent?: unknown; line?: unknown } | null
  try {
    m = JSON.parse(text) as typeof m
  } catch {
    return undefined
  }
  if (typeof m?.id !== "string" || m.id === "") return undefined
  if (m.t === "close") return { t: "close", id: m.id }
  if (m.t === "open" && typeof m.agent === "string") return { t: "open", id: m.id, agent: m.agent }
  if (m.t === "write" && typeof m.line === "string") return { t: "write", id: m.id, line: m.line }
  return undefined
}
