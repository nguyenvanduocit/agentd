import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { execFile } from "node:child_process"
import { mkdirSync, statSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { promisify } from "node:util"
import { build, Sandbox, VERSION } from "./harness.ts"

const exec = promisify(execFile)

beforeAll(async () => {
  await build()
}, 300_000)

const boxes: Sandbox[] = []
const servers: ReturnType<typeof Bun.serve>[] = []
afterEach(() => {
  for (const b of boxes.splice(0)) b.remove()
  for (const s of servers.splice(0)) void s.stop(true)
})

/** Runs the bundle with node; never throws on a non-zero exit. */
async function daemon(args: string[], box = new Sandbox()) {
  boxes.push(box)
  const b = await build()
  try {
    const running = exec("node", [b.bundle, ...args], { env: box.env(), cwd: box.work })
    running.child.stdin?.end() // a command that reads its input (pair) gets the end of it, not a wait
    const ok = await running
    return { code: 0, stdout: ok.stdout, stderr: ok.stderr, box }
  } catch (e) {
    const failed = e as { code: number; stdout: string; stderr: string }
    return { code: failed.code, stdout: failed.stdout, stderr: failed.stderr, box }
  }
}

function pairServer(handle: (body: Record<string, string>) => Response) {
  const s = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    async fetch(req) {
      const url = new URL(req.url)
      if (req.method !== "POST" || url.pathname !== "/agentd/pair") return new Response("not found", { status: 404 })
      return handle((await req.json()) as Record<string, string>)
    },
  })
  servers.push(s)
  return `http://127.0.0.1:${s.port}`
}

describe("version and usage", () => {
  test("version prints the version baked in at build time", async () => {
    const r = await daemon(["version"])
    expect(r.code).toBe(0)
    expect(r.stdout.trim()).toBe(VERSION)
  })

  test("--help prints the usage, exit 0; a mistake prints it on stderr, exit 2", async () => {
    expect((await daemon(["--help"])).stdout).toContain("agentd pair --app")
    const bad = await daemon(["frobnicate"])
    expect(bad.code).toBe(2)
    expect(bad.stderr).toContain('unknown command "frobnicate"')
    expect(bad.stderr).toContain("usage:")
  })

  test("running for an app that is not paired says how to pair, exit 1", async () => {
    const r = await daemon(["--app", "https://nowhere.example"])
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("not paired with https://nowhere.example")
    expect(r.stderr).toContain("agentd pair --app")
  })
})

describe("apps", () => {
  test("lists every app and browser grant, and removes one origin", async () => {
    const box = new Sandbox()
    boxes.push(box)
    {
      const reg = {
        apps: [{ server: "https://a.example", token: "ta" }, { server: "https://b.example", token: "tb" }],
        browsers: [{ origin: "https://c.example", grantedAt: "2026-10-19T00:00:00.000Z" }],
      }
      mkdirSync(join(box.config, "agentd"), { recursive: true })
      writeFileSync(box.configFile, JSON.stringify(reg))
      const run = (args: string[]) => daemon(args, box)
      const list = await run(["apps"])
      expect(list.code).toBe(0)
      expect(list.stdout).toContain("https://a.example  app")
      expect(list.stdout).toContain("https://b.example  app")
      expect(list.stdout).toContain("https://c.example  browser, granted 2026-10-19")
      expect(list.stdout).not.toContain("ta")
      expect((await run(["apps", "remove", "https://a.example"])).code).toBe(0)
      expect((JSON.parse(await Bun.file(box.configFile).text()) as { apps: unknown }).apps).toEqual([{ server: "https://b.example", token: "tb" }])
      const gone = await run(["apps", "remove", "https://a.example"])
      expect(gone.code).toBe(1)
      expect(gone.stderr).toContain("not connected to https://a.example")
    }
  })
})

describe("pair", () => {
  test("trades the code for a token and saves {server, token} with mode 0600", async () => {
    let got: Record<string, string> = {}
    const server = pairServer((body) => {
      got = body
      return Response.json({ token: "tok-123" })
    })
    const r = await daemon(["pair", "--app", server, "--code", "ABCD-1234"])
    expect(r.code).toBe(0)
    expect(r.stdout).toContain(`paired with ${server}`)
    expect(got).toMatchObject({ code: "ABCD-1234", os: process.platform, arch: process.arch })
    expect(got.hostname).toBeTruthy()
    const file = r.box.configFile
    expect(JSON.parse(await Bun.file(file).text())).toMatchObject({ apps: [{ server, token: "tok-123" }], browsers: [] })
    expect(statSync(file).mode & 0o777).toBe(0o600)
    expect(statSync(join(r.box.config, "agentd")).mode & 0o777).toBe(0o700)
  })

  test("a refused code: a message that says what to do, exit 1, nothing saved", async () => {
    const server = pairServer(() => new Response("code expired", { status: 410 }))
    const r = await daemon(["pair", "--app", server, "--code", "OLD"])
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("refused the pairing code (HTTP 410: code expired)")
    expect(r.stderr).toContain("copy a fresh command from the app's web UI")
    expect(await Bun.file(r.box.configFile).exists()).toBe(false)
  })

  test("a server that is not there: names it, exit 1", async () => {
    const r = await daemon(["pair", "--app", "http://127.0.0.1:9", "--code", "X"])
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("cannot reach http://127.0.0.1:9")
  })

  test("--app is required; the code comes from --code, AGENTD_PAIR_CODE or stdin, and without one pair says so", async () => {
    expect((await daemon(["pair", "--code", "X"])).stderr).toContain("pair needs --app")
    const r = await daemon(["pair", "--app", "https://x"])
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("no pairing code provided")
  })
})
