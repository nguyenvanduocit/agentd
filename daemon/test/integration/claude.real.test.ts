// Runs the real Claude adapter through the daemon and the server double. It costs a little money (two short turns),
// so it only runs with RUN_REAL_CLAUDE=1:  RUN_REAL_CLAUDE=1 bun test test/integration/claude.real.test.ts
import { afterEach, describe, expect, test } from "bun:test"
import { existsSync } from "node:fs"
import { resolve } from "node:path"
import { listeningPorts, processTree, Rig, waitFor, type Frame } from "./harness.ts"

const ADAPTER = resolve(import.meta.dir, "../../../agent/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js")
const enabled = process.env.RUN_REAL_CLAUDE === "1"

let rig: Rig | undefined
afterEach(async () => {
  await rig?.stop()
  rig = undefined
})

/** Speaks ACP JSON-RPC to the agent through the server double, by hand. */
class Acp {
  private nextId = 1
  private answered = 0

  constructor(
    private readonly r: Rig,
    private readonly session: string,
  ) {}

  private lines = async (): Promise<Record<string, unknown>[]> =>
    (await this.r.server.frames(this.session)).filter((f: Frame) => f.kind === "out").map((f) => JSON.parse(f.data ?? "null") as Record<string, unknown>)

  private send(message: Record<string, unknown>) {
    return this.r.server.write(this.session, JSON.stringify({ jsonrpc: "2.0", ...message }))
  }

  /** Answers what the agent asks of its client while a request is pending: permission requests get the first option. */
  private async serve(): Promise<void> {
    const all = await this.lines()
    for (const m of all.slice(this.answered)) {
      if (typeof m.method !== "string" || m.id === undefined) continue
      const params = m.params as { options?: { optionId: string }[] } | undefined
      const result = m.method === "session/request_permission" ? { outcome: { outcome: "selected", optionId: params?.options?.[0]?.optionId } } : undefined
      await this.send(result ? { id: m.id, result } : { id: m.id, error: { code: -32601, message: "not supported" } })
    }
    this.answered = all.length
  }

  async request(method: string, params: Record<string, unknown>, ms = 180_000): Promise<Record<string, unknown>> {
    const id = this.nextId++
    await this.send({ id, method, params })
    return waitFor(
      `the answer to ${method}`,
      async () => {
        await this.serve()
        const reply = (await this.lines()).find((m) => m.id === id && m.method === undefined)
        if (reply?.error) throw new Error(`${method}: ${JSON.stringify(reply.error)}`)
        return reply?.result as Record<string, unknown> | undefined
      },
      ms,
    )
  }

  async text(): Promise<string> {
    return (await this.r.server.frames(this.session)).map((f) => f.data ?? "").join("\n")
  }
}

describe.skipIf(!enabled)("real Claude adapter through the daemon", () => {
  test("a turn, then a graph tool call through agentd-mcp; the gateway is the only listening port", async () => {
    expect(existsSync(ADAPTER)).toBe(true)
    rig = await Rig.start({ claude: `node ${ADAPTER}` })
    await rig.open("s1")
    const acp = new Acp(rig, "s1")

    await acp.request("initialize", { protocolVersion: 1, clientInfo: { name: "daemon-test", version: "1" } })
    const mcp = { name: "app", command: "agentd-mcp", args: ["--session", "s1"], env: [] }
    const meta = { claudeCode: { emitRawSDKMessages: true, options: { settingSources: [] } } }
    const created = await acp.request("session/new", { cwd: rig.box.work, mcpServers: [mcp], _meta: meta })
    const sessionId = created.sessionId as string
    await acp.request("session/set_config_option", { sessionId, configId: "mode", value: "bypassPermissions" })

    const turn1 = await acp.request("session/prompt", { sessionId, prompt: [{ type: "text", text: "Setup check. Reply with the single word: ready." }] })
    expect(turn1.stopReason).toBe("end_turn")
    expect((await acp.text()).toLowerCase()).toContain("ready")

    const turn2 = await acp.request("session/prompt", {
      sessionId,
      prompt: [{ type: "text", text: "Call the ping tool of the app MCP server and tell me exactly what it returned." }],
    })
    expect(turn2.stopReason).toBe("end_turn")
    const events = await acp.text()
    expect(events).toContain("mcp__app__ping")
    expect(events).toContain("pong")

    const listeners = listeningPorts(processTree(rig.daemon.pid))
    const costs = [...events.matchAll(/"total_cost_usd":([0-9.]+)/g)].map((m) => m[1]).join(",")
    process.stdout.write(`REAL CLAUDE: turn1=${String(turn1.stopReason)} turn2=${String(turn2.stopReason)} listeners="${listeners}" cost_usd=${costs}\n`)
    const rows = listeners.split("\n").slice(1)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toContain("127.0.0.1:")
  }, 600_000)
})
