import type { AgentStatus } from "./agents.ts"

export type FrameKind = "out" | "err" | "exit"

/** A stdio MCP server as ACP's session/new takes it, without its name: the page names it. */
export interface McpServer {
  command: string
  args: string[]
  env: { name: string; value: string }[]
}

/**
 * What a page sends over /v1/session. `mcp` is a line of the page's own MCP server for the session's bridge (the
 * `agentd-mcp` the agent started from `opened.mcp`).
 */
export type ToDaemon =
  | { t: "open"; id: string; agent: string }
  | { t: "write"; id: string; line: string }
  | { t: "close"; id: string }
  | { t: "mcp"; id: string; line: string }

/**
 * What the daemon sends a page. A browser app is its own ACP client: `frame` carries the agent's lines as they are.
 * `opened.mcp` is the stdio server through which the agent reaches the page's MCP server (absent when the daemon has no
 * socket for it); `mcp` carries a line of the agent's MCP client to the page's server.
 */
export type FromDaemon =
  | { t: "hello"; version: string; agents: AgentStatus[] }
  | { t: "opened"; id: string; pid: number; cwd: string; mcp?: McpServer }
  | { t: "error"; id: string; message: string }
  | { t: "frame"; id: string; kind: FrameKind; data: string; code: number }
  | { t: "mcp"; id: string; line: string }

/** A `write` or an `mcp`: a line for the agent, or for its bridge. */
function lineMessage(t: unknown, id: string, line: unknown): ToDaemon | undefined {
  if (typeof line !== "string") return undefined
  return t === "write" || t === "mcp" ? { t, id, line } : undefined
}

/** A message of a page, or undefined when it is not one of the four the daemon accepts. */
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
  return lineMessage(m.t, m.id, m.line)
}
