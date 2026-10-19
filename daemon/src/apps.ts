import { chmodSync, mkdirSync, realpathSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import type { AgentInfo, Hello } from "../../contract/define.acp.ts"
import { isRpcError } from "../../contract/types.generated.ts"
import { EXIT_STOPPED } from "./core/exit.ts"
import type { App } from "./core/registry.ts"
import { wrapperFileName, wrapperScript } from "./core/wrapper.ts"
import { Fleet, type FleetOptions } from "./fleet.ts"
import { Link, openSocket } from "./link.ts"
import { say, warn } from "./log.ts"
import { PROTOCOL } from "./version.ts"

const HELLO_TIMEOUT_MS = 10_000

export interface AppHubOptions {
  /** What every app's fleet shares: the working directory, the environment, the session record, the adapters. */
  fleet: Omit<FleetOptions, "sink" | "binDir">
  /** The directory that holds one `<n>/agentd-mcp` wrapper per app. */
  wrapperRoot: string
  /** daemon.json, which every wrapper passes to `mcp`. */
  configFile: string
  hello: () => Hello
  /** An app left (Stop, or a refused token) and no link remains. */
  onEmpty: (code: number) => void
  /** An app (its origin) asked the daemon to restart. */
  onRestart: (server: string) => void
}

interface Entry {
  app: App
  link: Link
  fleet: Fleet
}

function describeAgents(agents: AgentInfo[]): string {
  return agents.map((a) => (a.ready ? `${a.name} ready` : `${a.name} not ready (${a.reason})`)).join(", ")
}

/** Writes the wrapper this app's agents find as `agentd-mcp`; returns its directory. */
function installWrapper(root: string, n: number, configFile: string, server: string): string {
  const dir = join(root, String(n))
  mkdirSync(dir, { recursive: true })
  const script = realpathSync(process.argv[1] ?? "")
  const file = join(dir, wrapperFileName(process.platform))
  writeFileSync(file, wrapperScript(process.platform, { node: process.execPath, script, args: ["--config", configFile, "--app", server] }))
  chmodSync(file, 0o755)
  return dir
}

/** The apps this daemon serves: one socket, one fleet of agents and one MCP wrapper each, so their sessions never mix. */
export class AppHub {
  private readonly entries = new Map<string, Entry>()
  private n = 0

  constructor(private readonly o: AppHubOptions) {}

  /** Sessions running over all apps. */
  get size(): number {
    let total = 0
    for (const e of this.entries.values()) total += e.fleet.size
    return total
  }

  /** Apps with a link (connected or reconnecting). */
  get linked(): number {
    return this.entries.size
  }

  has(server: string): boolean {
    return this.entries.has(server)
  }

  /** The servers of the apps with a link. */
  servers(): string[] {
    return [...this.entries.keys()]
  }

  /** Connects to the app now. */
  add(app: App): void {
    if (this.entries.has(app.server)) return
    const binDir = installWrapper(this.o.wrapperRoot, ++this.n, this.o.configFile, app.server)
    const socket = openSocket({ server: app.server, auth: { role: "daemon", token: app.token } })
    const link = new Link(socket, {
      onConnect: () => void this.sayHello(entry).catch((e: unknown) => warn(`hello to ${app.server} failed: ${e instanceof Error ? e.message : String(e)}`)),
      onDisconnect: (reason) => {
        say(`disconnected from ${app.server} (${reason}); ending ${entry.fleet.size} agent(s), reconnecting`)
        entry.fleet.endAll()
      },
      onTrouble: (message) => warn(`cannot reach ${app.server} (${message}); retrying`),
      onRejected: (message) => {
        warn(`${app.server} refused this daemon (${message}): the token is revoked or wrong, so this app stays stopped. To use it again: agentd pair --app ${app.server} --code <code>, then agentd service start`)
        void this.detach(app.server)
      },
    })
    const fleet = new Fleet({ ...this.o.fleet, binDir, sink: () => link.sink() })
    const entry: Entry = { app, link, fleet }
    this.entries.set(app.server, entry)
    link.rpc.handle.open((sessionId, agent) => fleet.open(sessionId, agent))
    link.rpc.handle.write((sessionId, line) => fleet.write(sessionId, line))
    link.rpc.handle.close((sessionId) => {
      fleet.close(sessionId)
      return Promise.resolve()
    })
    link.rpc.handle.restart(() => {
      this.o.onRestart(app.server)
      return Promise.resolve()
    })
    link.rpc.handle.stop(() => {
      say(`stop requested by the server ${app.server}: disconnecting it`)
      void this.detach(app.server)
      return Promise.resolve()
    })
    link.rpc.handle.mcpOut(() => Promise.reject(new Error("this socket is not an MCP bridge")))
  }

  private async sayHello(e: Entry): Promise<void> {
    const hello = this.o.hello()
    const reply = await e.link.rpc.server.hello(hello, { timeout: HELLO_TIMEOUT_MS })
    if (isRpcError(reply)) {
      warn(`${e.app.server} did not accept hello: ${reply.message}`)
      return
    }
    if (reply.protocol !== PROTOCOL) {
      warn(`${e.app.server} speaks protocol ${reply.protocol}, this daemon speaks ${PROTOCOL}: disconnecting it. Update the daemon or the app.`)
      void this.detach(e.app.server)
      return
    }
    say(`connected to ${e.app.server} (protocol ${reply.protocol}); agents: ${describeAgents(hello.agents)}`)
  }

  /** Ends this app's sessions and closes its link for good; calls `onEmpty` when no app is left, unless `quiet`. */
  async detach(server: string, quiet = false): Promise<void> {
    const e = this.entries.get(server)
    if (e === undefined) return
    this.entries.delete(server)
    await e.fleet.shutdown(1000)
    await e.link.close()
    if (this.entries.size === 0 && !quiet) this.o.onEmpty(EXIT_STOPPED)
  }

  /** Tells every connected app which agents are ready now. */
  agentsChanged(agents: AgentInfo[]): void {
    for (const e of this.entries.values()) if (e.link.socket.connected) e.link.rpc.server.agents(agents)
  }

  /** Ends every agent of every app and closes every link. No session opens after this. */
  async shutdown(graceMs: number): Promise<void> {
    const all = [...this.entries.values()]
    await Promise.all(all.map((e) => e.fleet.shutdown(graceMs)))
    await Promise.all(all.map((e) => e.link.close()))
    this.entries.clear()
  }
}
