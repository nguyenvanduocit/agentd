// Contract between the server and a user's daemon, source of truth for socketrpc-gen. Regenerate with scripts/gen-contract.sh.
// The server implements ServerFunctions (the daemon calls them); the daemon implements ClientFunctions (the server calls them).
// Methods must be arrow-typed properties; void return means fire-and-forget.

export interface Opened {
  pid: number
  cwd: string
}

// One ordered stream per socket: stdout lines, stderr chunks and the exit all travel in `frame`, because the Go
// server runs each event name on its own queue, so separate events would not keep their order across names.
export type FrameKind = "out" | "err" | "exit"

// An agent the daemon can start, and its name as a user reads it (title; a daemon older than 0.3 sends none). ready is
// false when the machine lacks what it needs; reason says what.
export interface AgentInfo {
  name: string
  title: string
  ready: boolean
  reason: string
}

export interface Hello {
  version: string
  hostname: string
  os: string
  arch: string
  agents: AgentInfo[]
  // true when a service restarts the daemon after it exits (not started with --foreground)
  managed: boolean
  // the oldest and the newest protocol this daemon speaks; a daemon older than the range (0.6) sends neither, and speaks
  // exactly the protocol it was built with (1)
  protocolMin: number
  protocolMax: number
}

// What the app's server answers to hello: the protocol version it speaks. The daemon refuses an app whose protocol is
// outside the range it speaks, and the server marks a daemon whose range does not hold its protocol as incompatible.
// Updates are not part of this contract: the daemon takes them from the release server.
export interface HelloReply {
  protocol: number
}

export interface ServerFunctions {
  // daemon -> server, fire-and-forget. out: data = one stdout line; err: data = stderr text; exit: code = exit code
  frame: (sessionId: string, kind: FrameKind, data: string, code: number) => void
  // daemon -> server on every (re)connect
  hello: (info: Hello) => HelloReply
  // daemon -> server, fire-and-forget: which agents are ready changed (an adapter finished installing, or failed to)
  agents: (agents: AgentInfo[]) => void
  // agentd-mcp bridge -> server (its own connection, one MCP JSON-RPC message per call)
  mcp: (line: string) => void
}

export interface ClientFunctions {
  // server -> daemon: start the named agent (claude, codex, grok) in a working directory the daemon chooses
  open: (sessionId: string, agent: string) => Opened
  write: (sessionId: string, line: string) => boolean
  // server -> daemon, fire-and-forget
  close: (sessionId: string) => void
  // server -> daemon: end every agent session and exit with code 75, so the service starts it again
  restart: () => void
  // server -> daemon: end every agent session and exit with code 0, so the service leaves it stopped
  stop: () => void
  // server -> agentd-mcp bridge
  mcpOut: (line: string) => void
}
