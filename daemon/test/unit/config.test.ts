import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { chmodSync, mkdtempSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { loadRegistry, registryFile, saveRegistry, updateRegistry } from "../../src/config.ts"
import type { Host } from "../../src/core/paths.ts"
import { addApp, emptyRegistry } from "../../src/core/registry.ts"
import { pair, PairError, type Fetch } from "../../src/pair.ts"

let dir: string
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "mc-daemon-unit-"))
})
afterEach(() => rmSync(dir, { recursive: true, force: true }))

const host = (): Host => ({ platform: "linux", env: { XDG_CONFIG_HOME: dir }, home: "/nowhere" })
const mode = (f: string) => statSync(f).mode & 0o777
const app = (token: string) => addApp(emptyRegistry(), { server: "https://s", token })

describe("config file", () => {
  test("is written with mode 0600 in a 0700 directory, also over a file that was looser", async () => {
    const file = join(dir, "a", "b", "daemon.json")
    await saveRegistry(file, app("t"))
    expect(mode(file)).toBe(0o600)
    expect(mode(join(dir, "a", "b"))).toBe(0o700)
    chmodSync(file, 0o644)
    await saveRegistry(file, app("t2"))
    expect(mode(file)).toBe(0o600)
    expect(readdirSync(join(dir, "a", "b"))).toEqual(["daemon.json"])
  })

  test("registryFile is --config, or the default location", () => {
    expect(registryFile({ config: "/c.json" }, host())).toBe("/c.json")
    expect(registryFile({ config: undefined }, host())).toBe(join(dir, "agentd", "daemon.json"))
  })

  test("a missing file is an empty registry: the daemon waits for apps to connect", async () => {
    expect(await loadRegistry(join(dir, "none.json"))).toEqual(emptyRegistry())
  })

  test("a broken or incomplete file names itself and says how to pair", async () => {
    const f = join(dir, "c.json")
    writeFileSync(f, "{oops")
    await expect(loadRegistry(f)).rejects.toThrow(`${f} is not valid JSON`)
    writeFileSync(f, '{"server":"https://s"}')
    await expect(loadRegistry(f)).rejects.toThrow(`${f} has no apps list`)
  })
})

describe("updateRegistry", () => {
  test("changes made at the same time all land: no lost update, no failed rename", async () => {
    const file = join(dir, "q", "daemon.json")
    await Promise.all(Array.from({ length: 20 }, (_, i) => updateRegistry(file, (r) => addApp(r, { server: `https://app${i}.example`, token: `t${i}` }))))
    const saved = await loadRegistry(file)
    const servers = saved.apps.map((a) => a.server)
    expect(servers).toHaveLength(20)
    for (let i = 0; i < 20; i++) expect(servers).toContain(`https://app${i}.example`)
    expect(readdirSync(join(dir, "q"))).toEqual(["daemon.json"])
  })

  test("the function sees the registry as the previous change left it, and its answer is what is saved", async () => {
    const file = join(dir, "r.json")
    await saveRegistry(file, app("t1"))
    const seen = await updateRegistry(file, (r) => ({ ...r, browsers: [{ origin: "https://p.example", grantedAt: "t" }] }))
    expect(seen.apps).toHaveLength(1)
    expect((await loadRegistry(file)).browsers).toEqual([{ origin: "https://p.example", grantedAt: "t" }])
  })
})

const reply = (status: number, body: string, json = false) => new Response(body, { status, headers: json ? { "content-type": "application/json" } : {} })

describe("pair", () => {
  test("posts the code with this machine's facts, saves the token", async () => {
    const seen: { url: string; body: Record<string, string> }[] = []
    const fetchFn: Fetch = (url, init) => {
      seen.push({ url, body: JSON.parse(init.body as string) as Record<string, string> })
      return Promise.resolve(reply(200, '{"token":"secret"}', true))
    }
    const file = join(dir, "daemon.json")
    expect(await pair("https://mc.example.com/", "CODE-1", file, fetchFn)).toEqual({ server: "https://mc.example.com", token: "secret" })
    expect(seen[0]?.url).toBe("https://mc.example.com/agentd/pair")
    expect(seen[0]?.body).toMatchObject({ code: "CODE-1", os: process.platform, arch: process.arch })
    expect(seen[0]?.body.hostname).toBeTruthy()
    expect(JSON.parse(await Bun.file(file).text())).toEqual({ apps: [{ server: "https://mc.example.com", token: "secret" }], browsers: [] })
    expect(mode(file)).toBe(0o600)
  })

  test.each([400, 401, 403, 404, 410])("HTTP %i tells the user to copy a fresh command", async (status) => {
    const err = await pair("https://s", "bad", join(dir, "d.json"), () => Promise.resolve(reply(status, "expired"))).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(PairError)
    expect((err as Error).message).toContain(`HTTP ${status}: expired`)
    expect((err as Error).message).toContain("fresh command")
  })

  test("a server error is a different message", async () => {
    await expect(pair("https://s", "c", join(dir, "d.json"), () => Promise.resolve(reply(503, "")))).rejects.toThrow("failed to pair (HTTP 503)")
  })

  test("a network failure names the address and the cause", async () => {
    const fetchFn: Fetch = () => Promise.reject(new TypeError("fetch failed", { cause: new Error("connect ECONNREFUSED 127.0.0.1:9") }))
    await expect(pair("http://127.0.0.1:9", "c", join(dir, "d.json"), fetchFn)).rejects.toThrow("cannot reach http://127.0.0.1:9 (connect ECONNREFUSED 127.0.0.1:9)")
    await expect(pair("http://127.0.0.1:9", "c", join(dir, "d.json"), () => Promise.reject(new Error("boom")))).rejects.toThrow("(boom)")
    await expect(pair("http://127.0.0.1:9", "c", join(dir, "d.json"), () => Promise.reject("odd"))).rejects.toThrow("(odd)")
  })

  test("an answer without a token is not an app that serves agentd, and nothing is saved", async () => {
    const file = join(dir, "d.json")
    await expect(pair("https://s", "c", file, () => Promise.resolve(reply(200, "<html>", false)))).rejects.toThrow("without a token")
    expect(await Bun.file(file).exists()).toBe(false)
  })

  test("a bad server address fails before any request", async () => {
    let called = false
    await expect(pair("nonsense", "c", join(dir, "d.json"), () => ((called = true), Promise.resolve(reply(200, "{}"))))).rejects.toThrow("is not a URL")
    expect(called).toBe(false)
  })
})
