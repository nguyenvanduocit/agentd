import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { createHash } from "node:crypto"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { build, groupAlive, readSessions, Rig, sh, sleep, VERSION, waitFor } from "./harness.ts"

beforeAll(async () => {
  await build()
}, 300_000)

let rig: Rig | undefined
const temps: string[] = []
afterEach(async () => {
  await rig?.stop()
  rig = undefined
  for (const d of temps.splice(0)) rmSync(d, { recursive: true, force: true })
})
const start = async (...a: Parameters<typeof Rig.start>) => (rig = await Rig.start(...a))

const outs = async (r: Rig, session: string) => (await r.server.frames(session)).filter((f) => f.kind === "out")

describe("hello", () => {
  test("every connect says hello with the version, the machine and the agents", async () => {
    const r = await start({ claude: "sh -c 'sleep 60'" })
    const st = await r.server.status()
    const hello = st.hellos?.[0]
    expect(hello).toMatchObject({ version: VERSION, os: process.platform, arch: process.arch })
    expect(hello?.hostname).toBeTruthy()
    expect(hello?.agents.map((a) => a.name)).toEqual(["claude", "codex", "grok"])
    expect(hello?.agents[0]).toEqual({ name: "claude", title: "Claude Code", ready: true, reason: "" })
    expect(st.connects).toEqual({ daemon: 1 })
    await waitFor("the status line", () => r.daemon.out.includes(`connected to ${r.server.origin}`))
  })

  test("a machine without npm and grok reports why", async () => {
    const empty = mkdtempSync(join(tmpdir(), "mc-empty-path-"))
    temps.push(empty)
    const r = await start({ env: { PATH: empty, AGENTD_AGENT_CODEX: "" } })
    const agents = (await r.server.status()).hellos?.[0]?.agents
    expect(agents?.map((a) => a.name)).toEqual(["claude", "codex", "grok"])
    for (const a of agents?.slice(0, 2) ?? []) {
      expect(a.ready).toBe(false)
      const title = a.name === "claude" ? "Claude" : "Codex"
      expect(a.reason).toMatch(new RegExp(`^(installing the ${title} adapter|cannot install the ${title} adapter: npm not found: install Node.js \\(it comes with npm\\); retrying in \\d+ s)`))
    }
    expect(agents?.[2]).toEqual({ name: "grok", title: "Grok", ready: false, reason: "grok not found in PATH" })
    const opened = await r.server.open("s1", "grok")
    expect(opened).toEqual({ error: expect.stringContaining("agent grok is not ready: grok not found in PATH") })
  })
})

describe("open", () => {
  test("an unknown agent, an empty or repeated session id, a missing binary are RpcErrors", async () => {
    const r = await start({ claude: "sh -c 'sleep 60'" })
    expect(await r.server.open("s1", "rm -rf")).toEqual({ error: expect.stringContaining('unknown agent "rm -rf"') })
    expect(await r.server.open("", "claude")).toEqual({ error: expect.stringContaining("empty session id") })
    await r.open("s2")
    expect(await r.server.open("s2", "claude")).toEqual({ error: expect.stringContaining("session s2 exists") })
    await r.server.close("s2")
    await r.frame("s2", "exit of s2", (f) => f.kind === "exit")
    // the id is free again
    await r.open("s2")
  })

  test("a command that does not exist", async () => {
    const r = await start({ claude: "/nonexistent/agent" })
    expect(await r.server.open("s1", "claude")).toEqual({ error: expect.stringContaining('command "/nonexistent/agent" not found') })
    expect((await r.server.frames("s1")).length).toBe(0)
  })

  test("the working directory is where the daemon was started, --dir overrides", async () => {
    const r = await start({ claude: "sh -c 'pwd; sleep 60'" })
    const opened = await r.open("s1")
    expect(opened.cwd).toBe(realpathSync(r.box.work))
    const first = await r.frame("s1", "pwd", (f) => f.kind === "out")
    expect(realpathSync(first.data!)).toBe(realpathSync(r.box.work))

    const other = mkdtempSync(join(tmpdir(), "mc-dir-"))
    temps.push(other)
    await r.daemon.stop()
    await r.startDaemon({ claude: "sh -c 'pwd; sleep 60'", args: ["--dir", other] })
    await r.server.waitHellos(2)
    const o2 = await r.open("s2")
    expect(o2.cwd).toBe(other)
    const line = await r.frame("s2", "pwd of s2", (f) => f.kind === "out")
    expect(realpathSync(line.data!)).toBe(realpathSync(other))
  })

  test("--dir that is not a directory stops the daemon with a message", async () => {
    const r = await start({ claude: "sleep 60" })
    await r.daemon.stop()
    const d = await r.startDaemon({ args: ["--dir", "/definitely/not/here"] })
    expect(await d.exited).toBe(1)
    expect(d.out).toContain("is not a directory")
  })
})

describe("frames", () => {
  test("stdout lines arrive in order and the exit frame is last", async () => {
    const r = await start({ claude: sh("i=0; while [ $i -lt 3000 ]; do echo line$i; i=$((i+1)); done") })
    await r.open("s1")
    const exit = await r.frame("s1", "exit frame", (f) => f.kind === "exit")
    const frames = await r.server.frames("s1")
    expect(frames[frames.length - 1]).toEqual(exit)
    expect(exit.code).toBe(0)
    const lines = frames.filter((f) => f.kind === "out").map((f) => f.data)
    expect(lines).toHaveLength(3000)
    expect(lines).toEqual(Array.from({ length: 3000 }, (_, i) => `line${i}`))
    expect(frames.filter((f) => f.kind === "exit")).toHaveLength(1)
  })

  test("stderr arrives as err frames, the exit code is the agent's", async () => {
    const r = await start({ claude: sh("echo oops >&2; exit 7") })
    await r.open("s1")
    const exit = await r.frame("s1", "exit frame", (f) => f.kind === "exit")
    expect(exit.code).toBe(7)
    const err = (await r.server.frames("s1")).filter((f) => f.kind === "err")
    expect(err.map((f) => f.data).join("")).toBe("oops\n")
  })

  test("ACP lines go through the fake agent both ways", async () => {
    const b = await build()
    const r = await start({ claude: b.fakeAgent })
    await r.open("s1")
    const init = { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: 1 } }
    expect(await r.server.write("s1", JSON.stringify(init))).toEqual({ ok: true })
    const reply = await r.frame("s1", "initialize reply", (f) => f.kind === "out")
    expect(JSON.parse(reply.data!)).toMatchObject({ jsonrpc: "2.0", id: 1, result: { protocolVersion: 1 } })
  })

  test("a write to a session that does not exist is false, close of one is silent", async () => {
    const r = await start({ claude: "sleep 60" })
    expect(await r.server.write("nope", "x")).toEqual({ ok: false })
    expect(await r.server.close("nope")).toEqual({ ok: true })
  })

  test("close ends the agent with its whole group", async () => {
    const r = await start({ claude: sh("sleep 300 & sleep 300 & wait") })
    const { pid } = await r.open("s1")
    expect(groupAlive(pid)).toBe(true)
    await r.server.close("s1")
    const exit = await r.frame("s1", "exit frame", (f) => f.kind === "exit")
    expect(exit.code).toBe(128)
    await waitFor("the group to be gone", () => !groupAlive(pid))
  })

  test("an agent that ends leaves nothing behind in its group", async () => {
    const r = await start({ claude: sh("sleep 300 & echo started") })
    const { pid } = await r.open("s1")
    const exit = await r.frame("s1", "exit frame", (f) => f.kind === "exit")
    expect(exit.code).toBe(0)
    await waitFor("the leftover sleep to be killed", () => !groupAlive(pid))
    expect(groupAlive(pid)).toBe(false)
  })
})

describe("large lines", () => {
  test("12 MiB in each direction, byte for byte", async () => {
    const r = await start({ claude: "cat" })
    await r.open("s1")
    const line = JSON.stringify({ text: "đ\u{1F600}x".repeat(Math.ceil((12 * 1024 * 1024) / 7)) }).slice(0, 12 * 1024 * 1024)
    expect(Buffer.byteLength(line)).toBeGreaterThanOrEqual(12 * 1024 * 1024)
    expect(await r.server.write("s1", line)).toEqual({ ok: true })
    const echoed = await waitFor("the echoed line", async () => (await outs(r, "s1"))[0], 60_000)
    expect(echoed.len).toBe(Buffer.byteLength(line))
    expect(echoed.sha256).toBe(createHash("sha256").update(line).digest("hex"))
  })
})

describe("socket drop", () => {
  test.each([
    ["the server ends the socket", false],
    ["the connection breaks", true],
  ])("%s: every agent process of the group dies, nothing stale is sent after the reconnect, hello again", async (_, transport) => {
    const r = await start({ claude: sh("sleep 300 & sleep 300 & echo up; wait") })
    const { pid } = await r.open("s1")
    await r.frame("s1", "first line", (f) => f.kind === "out")
    expect(groupAlive(pid)).toBe(true)

    await r.server.kick(transport)

    await waitFor("the whole group to die", () => !groupAlive(pid))
    await r.server.waitHellos(2)
    await waitFor("the daemon to be connected again", async () => (await r.server.status()).connected)
    await sleep(500)
    const frames = await r.server.frames("s1")
    expect(frames.filter((f) => f.kind === "exit")).toHaveLength(0)
    expect((await r.server.status()).connects).toMatchObject({ daemon: 2 })
    expect(r.daemon.out).toContain("disconnected from")

    // the daemon serves new sessions after the reconnect
    const again = await r.open("s2")
    expect(groupAlive(again.pid)).toBe(true)
  })
})

describe("daemon stop", () => {
  test("SIGTERM ends the agents and the daemon, and removes the wrapper directory and the state", async () => {
    const r = await start({ claude: sh("sleep 300 & sleep 300 & wait") })
    const { pid } = await r.open("s1")
    const stateFile = join(r.box.state, "agentd", "sessions.json")
    expect(readSessions(stateFile).groups[0]?.pgid).toBe(pid)
    r.daemon.kill("SIGTERM")
    expect(await r.daemon.exited).toBe(0)
    expect(groupAlive(pid)).toBe(false)
    await expect(Bun.file(stateFile).exists()).resolves.toBe(false)
  })

  test("SIGKILL leaves orphans; the next start finds and ends them", async () => {
    const r = await start({ claude: sh("sleep 300 & sleep 300 & wait") })
    const { pid } = await r.open("s1")
    await waitFor("the leader's start time in the state file", () => {
      const g = readSessions(join(r.box.state, "agentd", "sessions.json")).groups[0]
      return g !== undefined && g.lstart !== ""
    })
    r.daemon.kill("SIGKILL")
    await r.daemon.exited
    expect(groupAlive(pid)).toBe(true) // the orphans: leader and two sleeps

    const second = await r.startDaemon({ claude: sh("sleep 300") })
    await waitFor("the orphans to be ended", () => !groupAlive(pid))
    await r.server.waitHellos(2)
    expect(second.out).toContain("reaped 1 orphan group(s), 3 process(es)")
  })
})
