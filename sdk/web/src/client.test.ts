import { ClientSideConnection, PROTOCOL_VERSION, client, ndJsonStream, type Client, type Stream } from "@agentclientprotocol/sdk"
import { describe, expect, test } from "bun:test"
import { SUPPORTED_PROTOCOLS, connectBrowser, connectServer, detect, disconnect, openSession, reasonText, type Reason } from "./client.ts"

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>
const fetchWith = (handler: Handler): typeof fetch => ((input: RequestInfo | URL, init?: RequestInit) => Promise.resolve(handler(String(input), init))) as typeof fetch
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
const STATUS = { name: "agentd", version: "1.2.3", granted: { server: false, browser: false } }

describe("detect", () => {
  test("finds the daemon on the first port that answers like one", async () => {
    const seen: string[] = []
    const f = fetchWith((url) => {
      seen.push(url)
      if (url.startsWith("http://127.0.0.1:47616/")) return json(200, STATUS)
      throw new TypeError("Failed to fetch")
    })
    expect(await detect({ fetch: f })).toEqual({ found: true, port: 47616, version: "1.2.3", granted: { server: false, browser: false }, agents: undefined })
    expect(seen).toContain("http://127.0.0.1:47615/v1/status")
  })

  test("carries the protocol a daemon states, and none from a daemon that states none", async () => {
    const stated = await detect({ fetch: fetchWith(() => json(200, { ...STATUS, protocol: 1 })), ports: [47615] })
    expect(stated).toMatchObject({ found: true, protocol: 1 })
    const silent = await detect({ fetch: fetchWith(() => json(200, STATUS)), ports: [47615] })
    expect(silent.found && "protocol" in silent).toBe(false)
    const odd = await detect({ fetch: fetchWith(() => json(200, { ...STATUS, protocol: "1" })), ports: [47615] })
    expect(odd.found && "protocol" in odd).toBe(false)
  })

  test("SUPPORTED_PROTOCOLS names the protocols this SDK speaks", () => {
    expect(SUPPORTED_PROTOCOLS).toEqual([1])
  })

  test("every agent has a title: the daemon's, or its name from a daemon that sends none", async () => {
    const agents = [
      { name: "claude", title: "Claude Code", ready: true, reason: "" },
      { name: "codex", ready: false, reason: "not installed" },
    ]
    const f = fetchWith(() => json(200, { ...STATUS, agents }))
    expect(await detect({ fetch: f, ports: [47615] })).toMatchObject({
      agents: [
        { name: "claude", title: "Claude Code", ready: true, reason: "" },
        { name: "codex", title: "codex", ready: false, reason: "not installed" },
      ],
    })
  })

  test("another program on a port is not the daemon", async () => {
    const f = fetchWith(() => json(200, { name: "something-else" }))
    expect(await detect({ fetch: f })).toEqual({ found: false })
  })

  test("a refusal, a bad body and a hang all mean not found", async () => {
    const f = fetchWith((url) => {
      if (url.includes(":47615/")) return json(403, { reason: "origin" })
      if (url.includes(":47616/")) return new Response("not json", { status: 200 })
      return new Promise<Response>(() => undefined)
    })
    const started = Date.now()
    expect(await detect({ fetch: f, timeoutMs: 100, ports: [47615, 47616, 47617] })).toEqual({ found: false })
    expect(Date.now() - started).toBeLessThan(1500)
  })
})

describe("connect", () => {
  const found = { port: 47615 }

  test("connectServer posts the code and no server, and reports Allow", async () => {
    let got: { url: string; init?: RequestInit } | undefined
    const f = fetchWith((url, init) => {
      got = { url, init }
      return json(200, { granted: true })
    })
    expect(await connectServer(found, { code: "ABCD" }, { fetch: f })).toEqual({ granted: true })
    expect(got?.url).toBe("http://127.0.0.1:47615/v1/connect")
    expect(got?.init?.method).toBe("POST")
    expect(JSON.parse(String(got?.init?.body))).toEqual({ mode: "server", code: "ABCD" })
  })

  test("connectBrowser posts mode browser", async () => {
    let body = ""
    const f = fetchWith((_u, init) => {
      body = String(init?.body)
      return json(200, { granted: true })
    })
    expect(await connectBrowser(found, { fetch: f })).toEqual({ granted: true })
    expect(JSON.parse(body)).toEqual({ mode: "browser" })
  })

  test.each<[number, string, Reason]>([
    [403, "denied", "denied"],
    [403, "timeout", "timeout"],
    [429, "cooldown", "cooldown"],
    [409, "busy", "busy"],
    [503, "no-dialog", "no-dialog"],
    [502, "pairing", "pairing"],
    [403, "origin", "origin"],
    [400, "malformed", "malformed"],
    [403, "peer", "peer"],
    [421, "host", "host"],
    [404, "not-found", "not-found"],
    [500, "error", "error"],
  ])("HTTP %i %s is the reason %s", async (status, reason, expected) => {
    const f = fetchWith(() => json(status, { granted: false, reason, message: "why" }))
    expect(await connectBrowser(found, { fetch: f })).toEqual({ granted: false, reason: expected, message: "why" })
  })

  test("a daemon that went away is unreachable", async () => {
    const f = fetchWith(() => {
      throw new TypeError("Failed to fetch")
    })
    expect(await connectBrowser(found, { fetch: f })).toMatchObject({ granted: false, reason: "unreachable" })
  })

  test("disconnect sends DELETE", async () => {
    let got = ""
    let method = ""
    const f = fetchWith((url, init) => {
      got = url
      method = init?.method ?? ""
      return json(200, { granted: false })
    })
    await disconnect(found, { fetch: f })
    expect(method).toBe("DELETE")
    expect(got).toBe("http://127.0.0.1:47615/v1/connect")
  })

  test("disconnect says it worked when the daemon answers 200 (its body is {granted:false}: nothing is granted any more)", async () => {
    const f = fetchWith(() => json(200, { granted: false }))
    expect(await disconnect(found, { fetch: f })).toEqual({ disconnected: true })
  })

  test("disconnect says why it did not: the daemon's refusal, or an unreachable daemon", async () => {
    const refused = fetchWith(() => json(500, { granted: false, reason: "error", message: "disk full" }))
    expect(await disconnect(found, { fetch: refused })).toEqual({ disconnected: false, reason: "error", message: "disk full" })
    const garbled = fetchWith(() => new Response("oops", { status: 502 }))
    expect(await disconnect(found, { fetch: garbled })).toMatchObject({ disconnected: false, reason: "malformed" })
    const gone = fetchWith(() => {
      throw new TypeError("Failed to fetch")
    })
    expect(await disconnect(found, { fetch: gone })).toMatchObject({ disconnected: false, reason: "unreachable", message: "Failed to fetch" })
  })
})

describe("reasonText", () => {
  test("every reason has a sentence a person can act on", () => {
    const all: Reason[] = ["denied", "timeout", "cooldown", "locked", "busy", "no-dialog", "no-folder", "pairing", "not-saved", "origin", "malformed", "unreachable", "cancelled", "incompatible", "peer", "host", "not-found", "error"]
    for (const r of all) expect(reasonText(r).length).toBeGreaterThan(20)
  })

  test("a reason a newer daemon sends still gets a sentence that names it", () => {
    expect(reasonText("something-new")).toBe("agentd on this computer refused the request (something-new).")
  })
})

class FakeSocket {
  static last: FakeSocket
  readonly sent: Record<string, unknown>[] = []
  /** What the fake daemon does with each message the page sends. */
  onsend: ((m: Record<string, unknown>) => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) {
    FakeSocket.last = this
    queueMicrotask(() => this.onopen?.())
  }
  send(text: string): void {
    const m = JSON.parse(text) as Record<string, unknown>
    this.sent.push(m)
    this.onsend?.(m)
  }
  close(): void {
    this.onclose?.()
  }
  say(m: object): void {
    this.onmessage?.({ data: JSON.stringify(m) })
  }
}

describe("openSession", () => {
  test("opens one agent on its own socket and relays lines both ways", async () => {
    const pending = openSession({ port: 47615 }, "claude", { WebSocket: FakeSocket as unknown as typeof WebSocket })
    await new Promise((r) => setTimeout(r, 0))
    const sock = FakeSocket.last
    expect(sock.url).toBe("ws://127.0.0.1:47615/v1/session")
    sock.say({ t: "hello", version: "1", agents: [] })
    await new Promise((r) => setTimeout(r, 0))
    const open = sock.sent[0]
    expect(open).toMatchObject({ t: "open", agent: "claude" })
    sock.say({ t: "opened", id: open?.id, pid: 42, cwd: "/work" })
    const session = await pending
    expect(session).toMatchObject({ pid: 42, cwd: "/work" })

    const lines: string[] = []
    session.onFrame((f) => f.kind === "out" && lines.push(f.data))
    sock.say({ t: "frame", id: open?.id, kind: "out", data: "hello", code: 0 })
    sock.say({ t: "frame", id: "someone-else", kind: "out", data: "ignored", code: 0 })
    expect(lines).toEqual(["hello"])
    session.write("ping")
    expect(sock.sent[1]).toEqual({ t: "write", id: open?.id, line: "ping" })
    session.close()
    expect(sock.sent[2]).toEqual({ t: "close", id: open?.id })
  })

  test("an error for the open rejects with the daemon's message", async () => {
    const pending = openSession({ port: 47615 }, "rm -rf", { WebSocket: FakeSocket as unknown as typeof WebSocket })
    await new Promise((r) => setTimeout(r, 0))
    const sock = FakeSocket.last
    sock.say({ t: "hello", version: "1", agents: [] })
    await new Promise((r) => setTimeout(r, 0))
    sock.say({ t: "error", id: sock.sent[0]?.id, message: 'unknown agent "rm -rf"' })
    await expect(pending).rejects.toThrow('unknown agent "rm -rf"')
  })
})

const tick = () => new Promise((r) => setTimeout(r, 0))

/** A session opened on a fake daemon: the socket, the session's id and the session. */
/** What a daemon that bridges pages' MCP servers puts in `opened`. */
const MCP = {
  command: "/tmp/agentd-x1/bin/page/agentd-mcp",
  args: [],
  env: [
    { name: "AGENTD_MCP_SOCKET", value: "/tmp/agentd-abc123/mcp.sock" },
    { name: "AGENTD_MCP_TOKEN", value: "tok" },
  ],
}

/** `mcp` null: a daemon that sends no `mcp` in opened. */
async function opened(mcp: object | null = MCP, version = "0.4.0") {
  const pending = openSession({ port: 47615 }, "claude", { WebSocket: FakeSocket as unknown as typeof WebSocket })
  await tick()
  const sock = FakeSocket.last
  sock.say({ t: "hello", version, agents: [] })
  await tick()
  const id = sock.sent[0]?.id
  sock.say({ t: "opened", id, pid: 42, cwd: "/work", ...(mcp === null ? {} : { mcp }) })
  const session = await pending
  const out = (data: string) => sock.say({ t: "frame", id, kind: "out", data, code: 0 })
  const writes = () => sock.sent.filter((m) => m.t === "write").map((m) => m.line)
  return { sock, id, session, out, writes }
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<string> {
  let text = ""
  const decoder = new TextDecoder()
  for await (const chunk of stream) text += decoder.decode(chunk, { stream: true })
  return text + decoder.decode()
}

describe("AgentSession streams", () => {
  test("readable is the agent's stdout, one line per frame, and ends when the agent exits", async () => {
    const { sock, id, session, out } = await opened()
    const all = readAll(session.readable)
    out('{"a":1}')
    sock.say({ t: "frame", id, kind: "err", data: "stderr is not stdout", code: 0 })
    out("héllo")
    sock.say({ t: "frame", id, kind: "exit", data: "", code: 0 })
    expect(await all).toBe('{"a":1}\nhéllo\n')
  })

  test("readable ends when the socket closes", async () => {
    const { sock, session, out } = await opened()
    const all = readAll(session.readable)
    out("last")
    sock.close()
    expect(await all).toBe("last\n")
  })

  test("readable taken after the agent is gone is already over", async () => {
    const { sock, session } = await opened()
    sock.close()
    expect(await readAll(session.readable)).toBe("")
  })

  test("readable is one stream however often it is read", async () => {
    const { session } = await opened()
    expect(session.readable).toBeInstanceOf(ReadableStream)
    expect(session.readable).toBe(session.readable)
  })

  test("writable sends each complete line once, whatever the chunk boundaries", async () => {
    const { session, writes } = await opened()
    const bytes = new TextEncoder().encode('{"a":1}\n{"b":"é"}\n\n{"c"')
    const writer = session.writable.getWriter()
    const cut = bytes.indexOf(0xc3) + 1 // inside the two bytes of "é"
    await writer.write(bytes.slice(0, 3))
    expect(writes()).toEqual([])
    await writer.write(bytes.slice(3, cut))
    await writer.write(bytes.slice(cut))
    expect(writes()).toEqual(['{"a":1}', '{"b":"é"}'])
    await writer.write(new TextEncoder().encode(":3}\n"))
    expect(writes()).toEqual(['{"a":1}', '{"b":"é"}', '{"c":3}'])
  })

  test("closing writable sends the line still waiting for its newline", async () => {
    const { session, writes } = await opened()
    const writer = session.writable.getWriter()
    await writer.write(new TextEncoder().encode("one\ntwo"))
    await writer.close()
    expect(writes()).toEqual(["one", "two"])
  })
})

/** A fake agent on the fake daemon: answers ACP's initialize and session/new as an agent would on its stdout. */
function actAsAgent(sock: FakeSocket, id: unknown) {
  sock.onsend = (m) => {
    if (m.t !== "write") return
    const req = JSON.parse(String(m.line)) as { id: number; method: string }
    const result =
      req.method === "initialize"
        ? { protocolVersion: PROTOCOL_VERSION, agentCapabilities: {}, authMethods: [] }
        : req.method === "session/new"
          ? { sessionId: "s-1" }
          : undefined
    const answer = result === undefined ? { error: { code: -32601, message: "no such method" } } : { result }
    queueMicrotask(() => sock.say({ t: "frame", id, kind: "out", data: JSON.stringify({ jsonrpc: "2.0", id: req.id, ...answer }), code: 0 }))
  }
}

const INITIALIZE = { protocolVersion: PROTOCOL_VERSION, clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false } }

describe("the official ACP SDK over a session", () => {
  test("ClientSideConnection runs initialize and session/new through the streams", async () => {
    const { sock, id, session } = await opened()
    actAsAgent(sock, id)
    const page: Client = {
      requestPermission: () => Promise.resolve({ outcome: { outcome: "cancelled" } }),
      sessionUpdate: () => Promise.resolve(),
    }
    const acp = new ClientSideConnection(() => page, ndJsonStream(session.writable, session.readable))
    expect((await acp.initialize(INITIALIZE)).protocolVersion).toBe(PROTOCOL_VERSION)
    expect(await acp.newSession({ cwd: session.cwd, mcpServers: [] })).toEqual({ sessionId: "s-1" })
  })

  test("client().connectWith runs over the same streams", async () => {
    const { sock, id, session } = await opened()
    actAsAgent(sock, id)
    const sessionId = await client({ name: "test page" }).connectWith(ndJsonStream(session.writable, session.readable), async (agent) => {
      await agent.request("initialize", INITIALIZE)
      return (await agent.request("session/new", { cwd: session.cwd, mcpServers: [] })).sessionId
    })
    expect(sessionId).toBe("s-1")
  })

  test("a request in flight fails when the agent exits", async () => {
    const { sock, id, session } = await opened()
    actAsAgent(sock, id)
    const page: Client = { requestPermission: () => Promise.resolve({ outcome: { outcome: "cancelled" } }), sessionUpdate: () => Promise.resolve() }
    const acp = new ClientSideConnection(() => page, ndJsonStream(session.writable, session.readable))
    await acp.initialize(INITIALIZE)
    sock.onsend = () => queueMicrotask(() => sock.say({ t: "frame", id, kind: "exit", data: "", code: 1 }))
    await expect(acp.prompt({ sessionId: "s-1", prompt: [{ type: "text", text: "hi" }] })).rejects.toThrow()
  })

  test("session/new takes mcpServer() as one of its mcpServers", async () => {
    const { sock, id, session } = await opened()
    actAsAgent(sock, id)
    let asked: unknown
    const onsend = sock.onsend
    sock.onsend = (m) => {
      const req = m.t === "write" ? (JSON.parse(String(m.line)) as { method: string; params: unknown }) : undefined
      if (req?.method === "session/new") asked = req.params
      onsend?.(m)
    }
    const page: Client = { requestPermission: () => Promise.resolve({ outcome: { outcome: "cancelled" } }), sessionUpdate: () => Promise.resolve() }
    const acp = new ClientSideConnection(() => page, ndJsonStream(session.writable, session.readable))
    await acp.initialize(INITIALIZE)
    await acp.newSession({ cwd: session.cwd, mcpServers: [session.mcpServer("page")] })
    expect(asked).toEqual({ cwd: "/work", mcpServers: [{ name: "page", ...MCP }] })
  })
})

/** The page's MCP server of sdk/web/README.md ("Tools from the page"), as it is there. */
async function serveTools({ readable, writable }: Stream) {
  const out = writable.getWriter()
  for await (const msg of readable) {
    if (!("method" in msg) || !("id" in msg)) continue // a notification, or an answer
    const result =
      msg.method === "initialize"
        ? { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "page", version: "1" } }
        : msg.method === "tools/list"
          ? { tools: [{ name: "current_page", inputSchema: { type: "object" } }] }
          : msg.method === "tools/call"
            ? { content: [{ type: "text", text: location.pathname }] }
            : undefined
    await out.write(result === undefined ? { jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: `no method ${msg.method}` } } : { jsonrpc: "2.0", id: msg.id, result })
  }
}

describe("the page's MCP server", () => {
  test("mcpServer(name) is the stdio server the daemon named in opened, under that name", async () => {
    const { session } = await opened()
    expect(session.mcpServer("aiocean")).toEqual({ name: "aiocean", ...MCP })
    const a = session.mcpServer("a")
    a.env.push({ name: "X", value: "y" })
    expect(session.mcpServer("a").env).toHaveLength(2) // each call is a copy
  })

  test("mcpServer() on a daemon that bridges no MCP server says so and names the daemon's version", async () => {
    const { session } = await opened(null, "0.3.0")
    expect(() => session.mcpServer("page")).toThrow("agentd 0.3.0 does not bridge a page's MCP server")
  })

  test("mcp.readable is the agent's MCP lines for this session; mcp.writable sends each line back as mcp", async () => {
    const { sock, id, session } = await opened()
    const reader = session.mcp.readable.getReader()
    sock.say({ t: "mcp", id: "someone-else", line: '{"id":9}' })
    sock.say({ t: "mcp", id, line: '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' })
    expect(new TextDecoder().decode((await reader.read()).value)).toBe('{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n')
    const writer = session.mcp.writable.getWriter()
    await writer.write(new TextEncoder().encode('{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}\n'))
    expect(sock.sent.filter((m) => m.t === "mcp")).toEqual([{ t: "mcp", id, line: '{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}' }])
    expect(sock.sent.some((m) => m.t === "write")).toBe(false)
  })

  test("the README's page server: ndJsonStream over session.mcp answers the agent's tools/list", async () => {
    const { sock, id, session } = await opened()
    void serveTools(ndJsonStream(session.mcp.writable, session.mcp.readable))
    sock.say({ t: "mcp", id, line: JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) })
    sock.say({ t: "mcp", id, line: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }) })
    for (let i = 0; i < 20 && !sock.sent.some((m) => m.t === "mcp"); i++) await tick()
    const answer = sock.sent.find((m) => m.t === "mcp")
    expect(JSON.parse(String(answer?.line))).toEqual({ jsonrpc: "2.0", id: 1, result: { tools: [{ name: "current_page", inputSchema: { type: "object" } }] } })
  })

  test("mcp.readable is one stream, and ends when the agent exits or the socket closes", async () => {
    const exits = await opened()
    expect(exits.session.mcp.readable).toBe(exits.session.mcp.readable)
    const all = readAll(exits.session.mcp.readable)
    exits.sock.say({ t: "mcp", id: exits.id, line: "last" })
    exits.sock.say({ t: "frame", id: exits.id, kind: "exit", data: "", code: 0 })
    expect(await all).toBe("last\n")

    const closes = await opened()
    const rest = readAll(closes.session.mcp.readable)
    closes.sock.close()
    expect(await rest).toBe("")
  })
})
