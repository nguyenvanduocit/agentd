// Runs the real Claude adapter as a page's agent and has it call a tool the page serves (agentd-mcp --page). It costs
// a little money (one short turn), so it only runs with RUN_REAL_CLAUDE=1:
//   RUN_REAL_CLAUDE=1 sh test/run.sh test/integration/pagetools.real.test.ts
import { afterEach, describe, expect, test } from "bun:test"
import { randomBytes } from "node:crypto"
import { existsSync } from "node:fs"
import { resolve } from "node:path"
import { WebSocket } from "ws"
import { Sandbox, TestDaemon, waitFor } from "./harness.ts"

const ADAPTER = resolve(import.meta.dir, "../../../agent/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js")
const enabled = process.env.RUN_REAL_CLAUDE === "1"
const PAGE = "http://localhost:5173"

type Msg = Record<string, unknown>

let daemon: TestDaemon | undefined
let box: Sandbox | undefined
let socket: WebSocket | undefined
afterEach(async () => {
  socket?.terminate()
  await daemon?.stop()
  box?.remove()
  daemon = box = socket = undefined
})

/** The page's MCP server: one tool that returns the word only this test knows. */
function serveTool(word: string, called: () => void) {
  return (m: Msg): Msg | undefined => {
    if (m.id === undefined) return undefined // notifications/initialized and other notices need no answer
    const params = (m.params ?? {}) as { protocolVersion?: string }
    if (m.method === "initialize") return { protocolVersion: params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "page", version: "1" } }
    if (m.method === "tools/list") {
      return { tools: [{ name: "secret_word", description: "Returns the secret word of this page", inputSchema: { type: "object", properties: {} } }] }
    }
    if (m.method === "tools/call") {
      called()
      return { content: [{ type: "text", text: word }] }
    }
    return { error: { code: -32601, message: `${String(m.method)} is not served` } }
  }
}

/** The client's answer to what the agent asks it: permission for the page's tool is the page's to give, nothing else is offered. */
function replyTo(acp: Msg): Msg {
  if (acp.method !== "session/request_permission") return { id: acp.id, error: { code: -32601, message: "not offered" } }
  const options = (acp.params as { options?: { optionId: string; kind: string }[] } | undefined)?.options ?? []
  return { id: acp.id, result: { outcome: { outcome: "selected", optionId: options.find((o) => o.kind.startsWith("allow"))?.optionId } } }
}

/** The text a session/update adds to the agent's reply. */
function chunkText(acp: Msg): string {
  const u = (acp.params as { update?: { sessionUpdate?: string; content?: { text?: string } } } | undefined)?.update
  return u?.sessionUpdate === "agent_message_chunk" ? (u.content?.text ?? "") : ""
}

describe.skipIf(!enabled || !existsSync(ADAPTER))("a real Claude agent and a page's MCP server", () => {
  test("the agent calls the page's tool through agentd-mcp and says what it returned", async () => {
    const word = `agentd${randomBytes(4).toString("hex")}`
    box = new Sandbox()
    box.writeApps([], [{ origin: PAGE, grantedAt: "2026-10-19T00:00:00.000Z" }])
    daemon = await TestDaemon.start(box, [], { AGENTD_AGENT_CLAUDE: `node ${ADAPTER}` })
    const port = Number(await waitFor("the gateway port", () => /local gateway on 127\.0\.0\.1:(\d+)/.exec(daemon?.out ?? "")?.[1]))

    const ws = (socket = new WebSocket(`ws://127.0.0.1:${port}/v1/session`, { headers: { Origin: PAGE } }))
    const id = "s1"
    const send = (m: Msg) => ws.send(JSON.stringify(m))
    const toAgent = (m: Msg) => send({ t: "write", id, line: JSON.stringify({ jsonrpc: "2.0", ...m }) })
    const answers = new Map<number, Msg>()
    let text = ""
    let toolCalled = false
    const tool = serveTool(word, () => (toolCalled = true))
    let opened: Msg | undefined

    const onMcp = (line: string) => {
      const req = JSON.parse(line) as Msg
      const out = tool(req)
      if (out === undefined) return
      send({ t: "mcp", id, line: JSON.stringify("error" in out ? { jsonrpc: "2.0", id: req.id, ...out } : { jsonrpc: "2.0", id: req.id, result: out }) })
    }
    const onAgentLine = (acp: Msg) => {
      if (typeof acp.method === "string" && acp.id !== undefined) toAgent(replyTo(acp))
      else if (acp.method === "session/update") text += chunkText(acp)
      else if (typeof acp.id === "number") answers.set(acp.id, acp)
    }
    ws.on("message", (raw: Buffer) => {
      const m = JSON.parse(raw.toString()) as Msg
      if (m.t === "hello") send({ t: "open", id, agent: "claude" })
      else if (m.t === "opened") opened = m
      else if (m.t === "mcp") onMcp(String(m.line))
      else if (m.t === "frame" && m.kind === "out") onAgentLine(JSON.parse(String(m.data)) as Msg)
    })
    const answer = (n: number, what: string, ms = 120_000) => waitFor(what, () => answers.get(n), ms)

    await waitFor("opened", () => opened)
    const mcp = opened?.mcp as { command: string; args: string[]; env: { name: string; value: string }[] }
    expect(mcp.command.startsWith("/")).toBe(true)
    toAgent({ id: 1, method: "initialize", params: { protocolVersion: 1, clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false } } })
    await answer(1, "initialize")
    toAgent({ id: 2, method: "session/new", params: { cwd: box.work, mcpServers: [{ name: "page", ...mcp }] } })
    const sessionId = ((await answer(2, "session/new")).result as { sessionId: string }).sessionId
    toAgent({
      id: 3,
      method: "session/prompt",
      params: { sessionId, prompt: [{ type: "text", text: "Call the tool secret_word of the MCP server named page, then reply with exactly the word it returned and nothing else." }] },
    })
    const done = await answer(3, "the turn", 150_000)
    process.stderr.write(`stopReason=${JSON.stringify(done.result)} tool called=${toolCalled} reply=${JSON.stringify(text)}\n`)
    expect(toolCalled).toBe(true)
    expect(text).toContain(word)
  }, 200_000)
})
