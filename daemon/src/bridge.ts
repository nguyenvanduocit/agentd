import type { Command } from "./core/cli.ts"
import { LineSplitter, MAX_LINE_BYTES } from "./core/lines.ts"
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
