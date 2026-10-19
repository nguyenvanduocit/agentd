import type { McpServer } from "./localproto.ts"

/** The daemon's socket for page bridges, in the environment of `agentd-mcp --page`. */
export const MCP_SOCKET_ENV = "AGENTD_MCP_SOCKET"
/** The token of one page session, in the environment of `agentd-mcp --page`. */
export const MCP_TOKEN_ENV = "AGENTD_MCP_TOKEN"

/**
 * The stdio MCP server a page passes to session/new: the pages' wrapper `command` (it runs `mcp --page`), by absolute
 * path as the ACP schema asks, so it starts whatever PATH the agent gives its MCP servers. The socket and the token are
 * in `env`, not in `args`: any OS user can read a process's arguments, only its own user its environment.
 */
export function pageMcpServer(command: string, socket: string, token: string): McpServer {
  return {
    command,
    args: [],
    env: [
      { name: MCP_SOCKET_ENV, value: socket },
      { name: MCP_TOKEN_ENV, value: token },
    ],
  }
}

/** The first line a bridge sends on the daemon's socket: the token of its session. */
export function bridgeHello(token: string): string {
  return JSON.stringify({ token })
}

/** The token of a bridge's first line, or undefined when the line is not a hello. */
export function parseBridgeHello(line: string): string | undefined {
  try {
    const m = JSON.parse(line) as { token?: unknown } | null
    return typeof m?.token === "string" && m.token !== "" ? m.token : undefined
  } catch {
    return undefined
  }
}

export type BridgeAnswer = { ok: true } | { ok: false; message: string }

/** The daemon's answer to a hello: ok, or the reason it refuses (`refusal`). The page's lines follow an ok. */
export function bridgeAnswer(refusal: string | undefined): string {
  return JSON.stringify(refusal === undefined ? { ok: true } : { ok: false, message: refusal })
}

/** What a bridge reads in the daemon's first line. */
export function parseBridgeAnswer(line: string): BridgeAnswer {
  try {
    const m = JSON.parse(line) as { ok?: unknown; message?: unknown } | null
    if (m?.ok === true) return { ok: true }
    if (m?.ok === false && typeof m.message === "string") return { ok: false, message: m.message }
  } catch {
    /* said below */
  }
  return { ok: false, message: `the daemon answered something else: ${line}` }
}
