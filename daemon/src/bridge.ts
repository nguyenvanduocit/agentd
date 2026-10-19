import { connect, type Socket } from "node:net"
import type { Command } from "./core/cli.ts"
import type { Env } from "./core/executable.ts"
import { LineSplitter, MAX_LINE_BYTES } from "./core/lines.ts"
import { bridgeHello, MCP_SOCKET_ENV, MCP_TOKEN_ENV, parseBridgeAnswer } from "./core/pagemcp.ts"
import { loadRegistry, registryFile } from "./config.ts"
import { selectApp } from "./core/registry.ts"
import { currentHost } from "./host.ts"
import { Link, openSocket } from "./link.ts"
import { warn } from "./log.ts"

type McpCommand = Extract<Command, { kind: "mcp" }>

const RECONNECT_ATTEMPTS = 3
/** The longest the bridge waits to hand over the last lines before it exits anyway. */
const CLOSE_MS = 10_000

/**
 * `agentd-mcp --session <id>`: an MCP stdio server that forwards JSON-RPC lines, one per message, to the
 * app's server over its own socket and prints the server's answers. It opens no port. Resolves to the exit code.
 */
export async function runBridge(cmd: McpCommand): Promise<number> {
  const config = selectApp(await loadRegistry(registryFile(cmd, currentHost())), cmd.app)
  return new Promise<number>((resolve) => {
    const auth = { role: "mcp", token: config.token, session: cmd.session }
    const link = new Link(openSocket({ server: config.server, auth, reconnectionAttempts: RECONNECT_ATTEMPTS }), {
      onConnect: () => undefined,
      // The MCP session lives on the server's side of this socket: a new socket would be a new, empty session.
      onDisconnect: (reason) => {
        if (reason === "io client disconnect") return // our own close, after stdin ended
        warn(`agentd-mcp: the server connection ended (${reason})`)
        resolve(1)
      },
      onTrouble: (message) => warn(`agentd-mcp: cannot reach ${config.server} (${message})`),
      onRejected: (message) => {
        warn(`agentd-mcp: ${config.server} refused the bridge (${message})`)
        resolve(1)
      },
      onGiveUp: () => resolve(1),
    })
    link.rpc.handle.mcpOut((line) => {
      process.stdout.write(`${line}\n`)
      return Promise.resolve()
    })

    const splitter = new LineSplitter()
    process.stdin.on("data", (chunk: Buffer) => {
      for (const line of splitter.push(chunk)) if (line.trim() !== "") link.rpc.server.mcp(line)
      if (!splitter.tooLong) return
      // The server would refuse the message; a bridge that dropped it would leave the client waiting for an answer.
      warn(`agentd-mcp: the client sent a line longer than ${MAX_LINE_BYTES / (1024 * 1024)} MiB`)
      process.stdin.destroy()
      resolve(1)
    })
    process.stdin.on("end", () => {
      // The client is done. Lines it sent before the socket was up wait in socket.io's buffer: let them go out
      // (connect flushes the buffer), then close, and exit once the connection itself has closed.
      const finish = () => {
        link.socket.io.once("close", () => resolve(0))
        link.socket.disconnect()
      }
      if (link.socket.connected) finish()
      else link.socket.once("connect", finish)
      setTimeout(() => resolve(0), CLOSE_MS).unref()
    })
  })
}

const MAX_LINE_MIB = MAX_LINE_BYTES / (1024 * 1024)

/** Settles the page bridge once: the first outcome is its exit code, and its message, if any, goes to stderr. */
type Finish = (code: number, message?: string) => void

/** The daemon's lines: its answer to the hello first, then the page's lines, which go to stdout. */
function readDaemon(sock: Socket, finish: Finish): void {
  const splitter = new LineSplitter()
  let accepted = false
  sock.on("data", (chunk: Buffer) => {
    for (const line of splitter.push(chunk)) {
      if (accepted) {
        process.stdout.write(`${line}\n`)
        continue
      }
      const answer = parseBridgeAnswer(line)
      if (!answer.ok) {
        finish(1, `agentd-mcp: the daemon refused the bridge (${answer.message})`)
        return
      }
      accepted = true
    }
    if (splitter.tooLong) finish(1, `agentd-mcp: the page sent a line longer than ${MAX_LINE_MIB} MiB`)
  })
}

/** The client's lines on stdin go to the daemon; the end of stdin ends the bridge with 0 once its lines are out. */
function readClient(sock: Socket, finish: Finish): void {
  const splitter = new LineSplitter()
  process.stdin.on("data", (chunk: Buffer) => {
    for (const line of splitter.push(chunk)) if (line.trim() !== "") sock.write(`${line}\n`)
    // The page would never get the message; a bridge that dropped it would leave the client waiting for an answer.
    if (splitter.tooLong) finish(1, `agentd-mcp: the client sent a line longer than ${MAX_LINE_MIB} MiB`)
  })
  process.stdin.on("end", () => {
    // The client is done: the daemon closes its end once it has read the last lines, and that close is a clean exit.
    sock.removeAllListeners("close")
    sock.once("close", () => finish(0))
    sock.end()
    setTimeout(() => finish(0), CLOSE_MS).unref()
  })
}

/**
 * `agentd-mcp --page`: an MCP stdio server that forwards JSON-RPC lines, one per message, to the page whose session
 * started the agent, and prints the page's answers. It reaches the page through the daemon's socket
 * ($AGENTD_MCP_SOCKET) with the session's token ($AGENTD_MCP_TOKEN), both from the `opened.mcp` the page passed to
 * session/new. It opens no port. Resolves to the exit code.
 */
export function runPageBridge(env: Env): Promise<number> {
  const path = env[MCP_SOCKET_ENV]
  const token = env[MCP_TOKEN_ENV]
  if (!path || !token) {
    warn(`agentd-mcp --page needs ${MCP_SOCKET_ENV} and ${MCP_TOKEN_ENV}: the env of the MCP server in a page session's opened.mcp`)
    return Promise.resolve(1)
  }
  return new Promise<number>((resolve) => {
    const sock = connect(path)
    let done = false
    const finish: Finish = (code, message) => {
      if (done) return
      done = true
      if (message !== undefined) warn(message)
      process.stdin.destroy()
      sock.destroy()
      resolve(code)
    }
    sock.on("error", (e) => finish(1, `agentd-mcp: cannot reach the daemon at ${path} (${e.message})`))
    sock.on("close", () => finish(1, "agentd-mcp: the daemon ended the bridge: the page or its session closed, or the agent started a newer bridge"))
    sock.write(`${bridgeHello(token)}\n`)
    readDaemon(sock, finish)
    readClient(sock, finish)
  })
}
