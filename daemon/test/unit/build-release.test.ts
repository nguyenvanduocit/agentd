import { describe, expect, test } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"

const ROOT = resolve(import.meta.dir, "../../..")

/** Runs scripts/build-daemon.sh with `env` on top of a clean RELEASE environment; every case here must stop before it builds. */
async function releaseBuild(env: Record<string, string>): Promise<{ code: number; stderr: string }> {
  const out = mkdtempSync(join(tmpdir(), "mc-release-"))
  try {
    const base: Record<string, string | undefined> = { ...process.env, RELEASE: "1", VERSION: "1.0.0" }
    delete base.DAEMON_SIGNING_KEY
    const p = Bun.spawn(["bash", join(ROOT, "scripts/build-daemon.sh"), out], { env: { ...base, ...env }, stdout: "ignore", stderr: "pipe" })
    const stderr = await new Response(p.stderr).text()
    return { code: await p.exited, stderr }
  } finally {
    rmSync(out, { recursive: true, force: true })
  }
}

describe("a RELEASE=1 build of the daemon", () => {
  test("needs a version someone chose: x.y.z", async () => {
    for (const VERSION of ["", "1.2", "1.0.0-rc1", "0.0.0-dev"]) {
      const r = await releaseBuild({ VERSION })
      expect(r.code).toBe(1)
      expect(r.stderr).toContain("needs VERSION=x.y.z")
    }
  })

  test("refuses a version the daemon or the release server would not accept", async () => {
    for (const VERSION of ["1.0_0", "../1", "current", "-1"]) {
      const r = await releaseBuild({ VERSION })
      expect(r.code).toBe(1)
      expect(r.stderr).toContain("is not a plain version")
    }
  })

  test("needs the release private key", async () => {
    const r = await releaseBuild({})
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("needs DAEMON_SIGNING_KEY")
  })

  test("refuses a key file that does not exist", async () => {
    const r = await releaseBuild({ DAEMON_SIGNING_KEY: join(tmpdir(), "mc-no-such-key") })
    expect(r.code).toBe(1)
    expect(r.stderr).toContain("is not a readable file")
  })

  test("refuses a key whose public half is not daemon/release.pub", async () => {
    const dir = mkdtempSync(join(tmpdir(), "mc-wrong-key-"))
    try {
      const key = join(dir, "key")
      await Bun.spawn(["bun", join(ROOT, "scripts/sign-daemon.ts"), "keygen", key]).exited
      const r = await releaseBuild({ DAEMON_SIGNING_KEY: key })
      expect(r.code).toBe(1)
      expect(r.stderr).toContain("is not daemon/release.pub")
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
