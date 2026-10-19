// The Updater against real files, a real tar and a local HTTP server: no daemon process.
import { afterEach, describe, expect, test } from "bun:test"
import { execFileSync } from "node:child_process"
import { createHash, generateKeyPairSync } from "node:crypto"
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { layoutOf, type Layout } from "../../src/core/layout.ts"
import { tarballPath, type Offer } from "../../src/core/update.ts"
import { Updater, type UpdaterOptions } from "../../src/updater.ts"
import { signRelease } from "../../../scripts/sign-daemon.ts"
import { sleep, TEST_RELEASE_KEY, testSign, waitFor } from "./harness.ts"

const cleanup: (() => void)[] = []
afterEach(() => {
  for (const f of cleanup.splice(0)) f()
})

function tmp(): string {
  const d = mkdtempSync(join(tmpdir(), "mc-updater-"))
  cleanup.push(() => rmSync(d, { recursive: true, force: true }))
  return d
}

/** A tarball in npm's layout holding version `version` (package.json says `claims`; daemon.js holds `code`). */
function tarball(version: string, o: { claims?: string; daemon?: boolean; code?: string } = {}): { bytes: Buffer; sha256: string } {
  const stage = tmp()
  mkdirSync(join(stage, "package"))
  if (o.daemon !== false) writeFileSync(join(stage, "package", "daemon.js"), o.code ?? `// ${version}\n`)
  writeFileSync(join(stage, "package", "package.json"), JSON.stringify({ version: o.claims ?? version }))
  const out = join(stage, "x.tgz")
  execFileSync("sh", ["-c", `tar -cf - -C "${stage}" package | gzip -n > "${out}"`])
  const bytes = readFileSync(out)
  return { bytes, sha256: createHash("sha256").update(bytes).digest("hex") }
}

interface Fixture {
  layout: Layout
  base: string
  origin: string
  hits: string[]
  restarts: string[]
  serve: Map<string, () => Response>
  updater: (o?: Partial<UpdaterOptions>) => Updater
  busy: { value: boolean }
}

function fixture(running = "1.0.0"): Fixture {
  const base = tmp()
  const layout = layoutOf(join(base, "mc"))
  mkdirSync(join(layout.daemonDir, running), { recursive: true })
  writeFileSync(join(layout.daemonDir, running, "daemon.js"), "")
  execFileSync("ln", ["-s", running, layout.currentLink])
  const hits: string[] = []
  const serve = new Map<string, () => Response>()
  const s = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch(req) {
      const path = new URL(req.url).pathname
      hits.push(path)
      return serve.get(path)?.() ?? new Response("nope", { status: 404 })
    },
  })
  cleanup.push(() => void s.stop(true))
  const f: Fixture = {
    layout, base, origin: `http://127.0.0.1:${s.port}`, hits, restarts: [], serve, busy: { value: false },
    updater: (o = {}) => {
      const u = new Updater({
        layout, server: f.origin, running, releaseKey: TEST_RELEASE_KEY, idle: () => !f.busy.value, restart: (v) => f.restarts.push(v), idleCheckMs: 40,
        retry: { baseMs: 60, maxMs: 200, jitter: 0 }, ...o,
      })
      cleanup.push(() => u.stop())
      return u
    },
  }
  return f
}

const offerFor = (version: string, sha256: string): Offer => ({ version, url: tarballPath(version), sha256, signature: testSign(version, sha256) })
const current = (l: Layout) => readlinkSync(l.currentLink)
const entries = (l: Layout) => readdirSync(l.daemonDir).sort((a, b) => a.localeCompare(b))

describe("Updater", () => {
  test("downloads, checks, unpacks, switches current, and asks for a restart once idle", async () => {
    const f = fixture()
    const t = tarball("2.0.0")
    f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
    f.updater().offer(offerFor("2.0.0", t.sha256))
    await waitFor("the restart", () => f.restarts.length > 0)
    expect(f.restarts).toEqual(["2.0.0"])
    expect(current(f.layout)).toBe("2.0.0")
    expect(readFileSync(f.layout.previousFile, "utf8").trim()).toBe("1.0.0")
    expect(entries(f.layout)).toEqual(["1.0.0", "2.0.0", "current", "previous"])
    expect(readFileSync(join(f.layout.daemonDir, "2.0.0", "daemon.js"), "utf8")).toBe("// 2.0.0\n")
  })

  test("a busy daemon is not restarted until it is idle", async () => {
    const f = fixture()
    f.busy.value = true
    const t = tarball("2.0.0")
    f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
    f.updater().offer(offerFor("2.0.0", t.sha256))
    await waitFor("current switched", () => current(f.layout) === "2.0.0")
    await sleep(300)
    expect(f.restarts).toEqual([])
    f.busy.value = false
    await waitFor("the restart", () => f.restarts.length > 0)
    expect(f.restarts).toEqual(["2.0.0"])
  })

  test("a wrong sha256 leaves everything as it was, and a retry that works succeeds", async () => {
    const f = fixture()
    const t = tarball("2.0.0")
    let good = false
    f.serve.set(tarballPath("2.0.0"), () => new Response(good ? t.bytes : Buffer.from("tampered")))
    f.updater().offer(offerFor("2.0.0", t.sha256))
    await waitFor("two attempts", () => f.hits.length >= 2)
    expect(current(f.layout)).toBe("1.0.0")
    expect(entries(f.layout)).toEqual(["1.0.0", "current"])
    expect(f.restarts).toEqual([])
    good = true
    await waitFor("the restart", () => f.restarts.length > 0)
    expect(current(f.layout)).toBe("2.0.0")
    expect(entries(f.layout).filter((n) => n.startsWith("."))).toEqual([])
  })

  test.each([
    ["a server error", () => new Response("down", { status: 503 })],
    ["a redirect (never followed)", () => new Response(null, { status: 302, headers: { location: "http://127.0.0.1:1/x" } })],
  ])("%s changes nothing", async (_, respond) => {
    const f = fixture()
    f.serve.set(tarballPath("2.0.0"), respond)
    f.updater({ retry: { baseMs: 60_000, maxMs: 60_000, jitter: 0 } }).offer(offerFor("2.0.0", "a".repeat(64)))
    await waitFor("an attempt", () => f.hits.length >= 1)
    await sleep(250)
    expect(current(f.layout)).toBe("1.0.0")
    expect(entries(f.layout).filter((n) => n !== "1.0.0" && n !== "current")).toEqual([])
  })

  test("a tarball without daemon.js, or whose package.json names another version, is not installed", async () => {
    for (const bad of [tarball("2.0.0", { daemon: false }), tarball("2.0.0", { claims: "9.9.9" })]) {
      const f = fixture()
      f.serve.set(tarballPath("2.0.0"), () => new Response(bad.bytes))
      f.updater({ retry: { baseMs: 60_000, maxMs: 60_000, jitter: 0 } }).offer(offerFor("2.0.0", bad.sha256))
      await waitFor("an attempt", () => f.hits.length >= 1)
      await sleep(250)
      expect(current(f.layout)).toBe("1.0.0")
      expect(entries(f.layout)).toEqual(["1.0.0", "current"])
    }
  })

  test("a version recorded as rejected is not downloaded; another one is", async () => {
    const f = fixture()
    writeFileSync(f.layout.rejectedFile, "2.0.0\n")
    const t = tarball("2.0.0")
    const t3 = tarball("3.0.0")
    f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
    f.serve.set(tarballPath("3.0.0"), () => new Response(t3.bytes))
    const u = f.updater()
    u.offer(offerFor("2.0.0", t.sha256))
    await sleep(200)
    expect(f.hits).toEqual([])
    u.offer(offerFor("3.0.0", t3.sha256))
    await waitFor("the restart", () => f.restarts.length > 0)
    expect(f.restarts).toEqual(["3.0.0"])
    expect(existsSync(f.layout.rejectedFile)).toBe(false)
  })

  test("an invalid offer (another host, a path outside the tarball names) is refused without a request", async () => {
    const f = fixture()
    const u = f.updater()
    u.offer({ ...offerFor("2.0.0", "a".repeat(64)), url: "http://evil.example/x.tgz" })
    u.offer(offerFor("../x", "a".repeat(64)))
    await sleep(200)
    expect(f.hits).toEqual([])
    expect(entries(f.layout)).toEqual(["1.0.0", "current"])
  })

  test("the server going back to the running version cancels a staged update", async () => {
    const f = fixture()
    f.busy.value = true
    const t = tarball("2.0.0")
    f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
    const u = f.updater()
    u.offer(offerFor("2.0.0", t.sha256))
    await waitFor("current switched", () => current(f.layout) === "2.0.0")
    u.offer({ version: "1.0.0", url: "", sha256: "", signature: "" })
    await waitFor("current back", () => current(f.layout) === "1.0.0")
    f.busy.value = false
    await sleep(200)
    expect(f.restarts).toEqual([])
  })

  test("an older version directory beyond the previous one is removed", async () => {
    const f = fixture()
    mkdirSync(join(f.layout.daemonDir, "0.5.0"))
    const t = tarball("2.0.0")
    f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
    f.updater().offer(offerFor("2.0.0", t.sha256))
    await waitFor("the restart", () => f.restarts.length > 0)
    expect(entries(f.layout)).toEqual(["1.0.0", "2.0.0", "current", "previous"])
  })

  describe("signature", () => {
    const otherKey = generateKeyPairSync("ed25519").privateKey.export({ type: "pkcs8", format: "pem" }).toString()
    /** Offers `offer` to a new updater; resolves once it has refused (nothing staged, no restart) and returns what was fetched. */
    async function refused(f: Fixture, offer: Offer, o: Partial<UpdaterOptions> = {}): Promise<string[]> {
      f.updater({ retry: { baseMs: 60_000, maxMs: 60_000, jitter: 0 }, ...o }).offer(offer)
      await sleep(300)
      expect(current(f.layout)).toBe("1.0.0")
      expect(entries(f.layout)).toEqual(["1.0.0", "current"])
      expect(f.restarts).toEqual([])
      return f.hits
    }

    test("a tampered tarball, with the sha256 the server changed to match it, is refused before it is downloaded", async () => {
      const f = fixture()
      const real = tarball("2.0.0")
      const evil = tarball("2.0.0", { code: "// evil\n" })
      f.serve.set(tarballPath("2.0.0"), () => new Response(evil.bytes))
      expect(await refused(f, { ...offerFor("2.0.0", real.sha256), sha256: evil.sha256 })).toEqual([])
    })

    test("a signature of another version is refused", async () => {
      const f = fixture()
      const t = tarball("2.0.1")
      f.serve.set(tarballPath("2.0.1"), () => new Response(t.bytes))
      const signedFor200 = testSign("2.0.0", t.sha256)
      expect(await refused(f, { ...offerFor("2.0.1", t.sha256), signature: signedFor200 })).toEqual([])
    })

    test("an offer without a signature is refused without a request", async () => {
      const f = fixture()
      const t = tarball("2.0.0")
      f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
      expect(await refused(f, { ...offerFor("2.0.0", t.sha256), signature: "" })).toEqual([])
    })

    test("a signature by another key is refused", async () => {
      const f = fixture()
      const t = tarball("2.0.0")
      f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
      expect(await refused(f, { ...offerFor("2.0.0", t.sha256), signature: signRelease(otherKey, "2.0.0", t.sha256) })).toEqual([])
    })

    test("a daemon without a release key refuses a correctly signed update", async () => {
      const f = fixture()
      const t = tarball("2.0.0")
      f.serve.set(tarballPath("2.0.0"), () => new Response(t.bytes))
      expect(await refused(f, offerFor("2.0.0", t.sha256), { releaseKey: undefined })).toEqual([])
    })

    test("a version already on disk is not made current from an unsigned offer", async () => {
      const f = fixture()
      mkdirSync(join(f.layout.daemonDir, "2.0.0"))
      writeFileSync(join(f.layout.daemonDir, "2.0.0", "daemon.js"), "")
      f.updater({ retry: { baseMs: 60_000, maxMs: 60_000, jitter: 0 } }).offer({ ...offerFor("2.0.0", "a".repeat(64)), signature: testSign("2.0.0", "b".repeat(64)) })
      await sleep(300)
      expect(current(f.layout)).toBe("1.0.0")
      expect(f.restarts).toEqual([])
    })
  })
})
