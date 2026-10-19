import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from "node:child_process"
import { mkdtempSync, writeFileSync } from "node:fs"
import { createServer } from "node:net"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { io } from "socket.io-client"
import { build, listeningPorts, processTree, Rig, Sandbox, sh, sleep, TestDaemon, TestServer, TOKEN, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

let rig: Rig | undefined
afterEach(async () => {
  await rig?.stop()
  rig = undefined
})
const start = async (...a: Parameters<typeof Rig.start>) => (rig = await Rig.start(...a))

const INIT = { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "test", version: "1" } } }
const INITIALIZED = { jsonrpc: "2.0", method: "notifications/initialized" }
const PING = { jsonrpc: "2.0", id: 2, method: "tools/call", params: { name: "ping", arguments: {} } }

describe("agentd-mcp", () => {
  test("is on the agent's PATH, in a directory of its own per app", async () => {
    const r = await start({ claude: sh("command -v agentd-mcp; sleep 60") })
    await r.open("s1")
    const line = await r.frame("s1", "the wrapper path", (f) => f.kind === "out")
    expect(line.data).toMatch(/agentd-[^/]+\/bin\/\d+\/agentd-mcp$/)
  })

  test("the agent runs it by name and gets pong from the server's ping tool", async () => {
    const r = await start({ claude: sh("agentd-mcp --session s1") })
    await r.open("s1")
    for (const m of [INIT, INITIALIZED, PING]) expect(await r.server.write("s1", JSON.stringify(m))).toEqual({ ok: true })
    const pong = await r.frame("s1", "the ping result", (f) => f.kind === "out" && (f.data ?? "").includes('"id":2'))
    expect(JSON.parse(pong.data!)).toMatchObject({ id: 2, result: { content: [{ type: "text", text: "pong" }] } })
    expect((await r.server.status()).connects).toEqual({ daemon: 1, mcp: 1 })
  })

  test("the bridge exits with 1 when the server ends its connection", async () => {
    const r = await start({ claude: sh("agentd-mcp --session s1; echo bridge-exit=$?") })
    await r.open("s1")
    await r.server.write("s1", JSON.stringify(INIT))
    await r.frame("s1", "initialize reply", (f) => f.kind === "out")
    expect(await r.server.kickBridges()).toBe(1)
    const exit = await r.frame("s1", "the bridge's exit status", (f) => f.kind === "out" && (f.data ?? "").startsWith("bridge-exit="))
    expect(exit.data).toBe("bridge-exit=1")
  })
})

describe("no listening port", () => {
  test("the only listener in the daemon's process tree is the gateway on loopback, with an agent and its bridge running", async () => {
    // control: the check does see a listener, so an empty answer below means something
    const probe = createServer()
    await new Promise<void>((r) => probe.listen(0, "127.0.0.1", r))
    expect(listeningPorts([process.pid])).toContain("LISTEN")
    probe.close()

    const r = await start({ claude: sh("agentd-mcp --session s1") })
    await r.open("s1")
    await r.server.write("s1", JSON.stringify(INIT))
    await r.frame("s1", "bridge answers", (f) => f.kind === "out")
    const tree = processTree(r.daemon.pid)
    expect(tree.length).toBeGreaterThanOrEqual(2) // the daemon and what runs below it: the agent shell or the bridge
    const rows = listeningPorts(tree).split("\n").slice(1)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toContain(`${r.daemon.pid}`)
    expect(rows[0]).toContain("127.0.0.1:")
  })
})

describe("namespace /agentd", () => {
  const probe = (origin: string, ns: string, auth: Record<string, string>) =>
    new Promise<string>((resolve) => {
      const s = io(origin + ns, { transports: ["websocket"], auth, reconnection: false })
      s.on("connect", () => {
        s.disconnect()
        resolve("connected")
      })
      s.on("connect_error", (e) => {
        s.disconnect()
        resolve(e.message)
      })
    })

  test("accepts a good handshake, refuses a bad token, knows no other namespace", async () => {
    const r = await start({ claude: "sleep 60" })
    const auth = { role: "daemon", token: TOKEN }
    expect(await probe(r.server.origin, "/agentd", auth)).toBe("connected")
    expect(await probe(r.server.origin, "/agentd", { ...auth, token: "wrong" })).toBe("unauthorized")
    expect(await probe(r.server.origin, "/nope", auth)).toBe("Invalid namespace")
    expect((await r.server.status()).rejects).toBe(1)
  })

  test("a server that cannot check the token now is retried, not taken for a revoke", async () => {
    const r = await start({ claude: "sleep 60" })
    await r.server.unavailable(2)
    await r.server.kick(true)
    await r.server.waitHellos(2)
    expect((await r.server.status()).rejects).toBe(2)
    expect(r.daemon.out).toContain("(unavailable)")
    expect(r.daemon.out).not.toContain("stays stopped")
  })

  test("agentd apps remove, while the daemon runs, disconnects that app and leaves the daemon up", async () => {
    const r = await start({ claude: "sleep 60" })
    const cli = spawnSync("node", [(await build()).bundle, "apps", "remove", r.server.origin], { env: r.box.env(), encoding: "utf8" })
    expect(cli.status).toBe(0)
    await waitFor("the app's link to close", async () => !(await r.server.status()).connected)
    await sleep(1500)
    expect((await r.server.status()).hellos?.length).toBe(1)
    expect(await Promise.race([r.daemon.exited, sleep(100).then(() => "running")])).toBe("running")
  })

  test("a daemon with a refused token says so, exits 0 and is not retried", async () => {
    const r = await start({ claude: "sleep 60" })
    r.box.writeConfig(r.server.origin, "revoked")
    const d = await r.startDaemon({ claude: "sleep 60" })
    expect(await d.exited).toBe(0)
    expect(d.out).toContain("refused this daemon (unauthorized)")
    expect(d.out).toContain("stays stopped")
    expect((await r.server.status()).rejects).toBe(1)
  })
})

describe("agentd-mcp with two apps", () => {
  test("an agent reaches the server of the app that opened it, not the other", async () => {
    const a = await TestServer.start("token-a")
    const b = await TestServer.start("token-b")
    const box = new Sandbox()
    box.writeApps([{ server: a.origin, token: "token-a" }, { server: b.origin, token: "token-b" }])
    const daemon = await TestDaemon.start(box, [], { AGENTD_AGENT_CLAUDE: sh("agentd-mcp --session s1") })
    try {
      await a.waitHellos(1)
      await b.waitHellos(1)
      const opened = await b.openOk("s1")
      for (const m of [INIT, INITIALIZED, PING]) expect(await b.write("s1", JSON.stringify(m))).toEqual({ ok: true })
      await waitFor("the ping result on b", async () => (await b.frames("s1")).some((f) => f.kind === "out" && (f.data ?? "").includes('"id":2')))
      expect((await b.status()).connects).toEqual({ daemon: 1, mcp: 1 })
      expect((await a.status()).connects).toEqual({ daemon: 1 })
      process.kill(-opened.pid, "SIGKILL")
    } finally {
      await daemon.stop()
      a.stop()
      b.stop()
      box.remove()
    }
  })
})

describe("bridge subcommand", () => {
  const NODE = "node"
  /** Runs `node daemon.js <args>`; `talk` may use the process while it runs (its stdin is a pipe). */
  const run = async (r: Rig, args: string[], talk?: (p: ChildProcessWithoutNullStreams, out: () => string) => Promise<void>) => {
    const b = await build()
    const p = spawn(NODE, [b.bundle, ...args], { env: r.box.env(), stdio: ["pipe", "pipe", "pipe"] })
    let out = ""
    p.stdout.on("data", (d: Buffer) => (out += d.toString()))
    p.stderr.on("data", (d: Buffer) => (out += d.toString()))
    const exited = new Promise<number | null>((res) => p.once("exit", res))
    if (talk) await talk(p, () => out)
    return { code: await exited, out }
  }

  test("a refused token exits 1 with a message", async () => {
    const r = await start({ claude: "sleep 60" })
    const cfg = join(mkdtempSync(join(tmpdir(), "mc-cfg-")), "daemon.json")
    writeFileSync(cfg, JSON.stringify({ apps: [{ server: r.server.origin, token: "revoked" }], browsers: [] }))
    const res = await run(r, ["mcp", "--session", "s1", "--config", cfg])
    expect(res.code).toBe(1)
    expect(res.out).toContain("refused the bridge (unauthorized)")
  })

  test("a server that cannot check the token now is retried, then the bridge gets through", async () => {
    const r = await start({ claude: "sleep 60" })
    await r.server.unavailable(2)
    const res = await run(r, ["mcp", "--session", "s1"], async (p) => {
      await waitFor("the bridge's socket", async () => (await r.server.status()).connects?.mcp === 1)
      p.stdin.end()
    })
    expect(res.code).toBe(0)
    expect((await r.server.status()).rejects).toBe(2)
  })

  test("a server that stays unavailable ends the bridge after a few tries", async () => {
    const r = await start({ claude: "sleep 60" })
    await r.server.unavailable(1000)
    const res = await run(r, ["mcp", "--session", "s1"])
    expect(res.code).toBe(1)
    expect(res.out).toContain("(unavailable)")
  })

  test("a server that cannot be reached ends the bridge after a few tries", async () => {
    const r = await start({ claude: "sleep 60" })
    const cfg = join(mkdtempSync(join(tmpdir(), "mc-cfg-")), "daemon.json")
    writeFileSync(cfg, JSON.stringify({ apps: [{ server: "http://127.0.0.1:9", token: TOKEN }], browsers: [] }))
    const res = await run(r, ["mcp", "--session", "s1", "--config", cfg])
    expect(res.code).toBe(1)
    expect(res.out).toContain("cannot reach")
  })

  test("end of stdin, after an answer came back, closes the bridge with 0", async () => {
    const r = await start({ claude: "sleep 60" })
    const res = await run(r, ["mcp", "--session", "s1", "--config", r.box.configFile], async (p, out) => {
      p.stdin.write(`${JSON.stringify(INIT)}\n\n`) // the empty line is not forwarded
      await waitFor("the initialize answer on stdout", () => out().includes('"id":1'))
      p.stdin.end()
    })
    expect(res.code).toBe(0)
    expect(res.out).toContain('"serverInfo"')
    expect(res.out).not.toContain("connection ended")
    expect((await r.server.status()).mcpLines).toBe(1)
  })

  test("mcp without a session is a usage error", async () => {
    const r = await start({ claude: "sleep 60" })
    const res = await run(r, ["mcp"])
    expect(res.code).toBe(2)
    expect(res.out).toContain("mcp needs --session")
  })

  test("waitFor sanity: a frame predicate that never matches times out with what it waited for", async () => {
    await expect(waitFor("never", () => undefined, 200)).rejects.toThrow("timed out after 200 ms waiting for never")
  })
})
