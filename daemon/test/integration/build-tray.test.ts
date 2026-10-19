import { describe, expect, test } from "bun:test"
import { execFileSync } from "node:child_process"
import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"

const ROOT = resolve(import.meta.dir, "../../..")

function run(script: string, args: string[], env: Record<string, string>): { out: string; stderr: string } {
  const r = Bun.spawnSync(["bash", join(ROOT, "scripts", script), ...args], { env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" })
  if (r.exitCode !== 0) throw new Error(`${script} exited ${r.exitCode}: ${r.stderr.toString()}`)
  return { out: r.stdout.toString(), stderr: r.stderr.toString() }
}

const tarList = (tgz: string) => execFileSync("tar", ["-tvzf", tgz], { encoding: "utf8" })

describe("scripts/build-tray.sh", () => {
  test("builds one program per platform, named for Go's os-arch, with .exe on Windows", () => {
    const out = mkdtempSync(join(tmpdir(), "mc-tray-build-"))
    try {
      run("build-tray.sh", [out], { TRAY_TARGETS: "linux/amd64 windows/amd64" })
      expect(existsSync(join(out, "linux-amd64", "agentd-tray"))).toBe(true)
      expect(existsSync(join(out, "windows-amd64", "agentd-tray.exe"))).toBe(true)
      expect(existsSync(join(out, "darwin-arm64"))).toBe(false)
      expect(statSync(join(out, "linux-amd64", "agentd-tray")).mode & 0o111).not.toBe(0)
    } finally {
      rmSync(out, { recursive: true, force: true })
    }
  }, 180_000)
})

describe("scripts/build-daemon.sh with a tray", () => {
  test("packs TRAY_DIR into the tarball as tray/, executables staying executable", () => {
    const trays = mkdtempSync(join(tmpdir(), "mc-trays-"))
    const out = mkdtempSync(join(tmpdir(), "mc-daemon-out-"))
    try {
      mkdirSync(join(trays, "darwin-arm64"))
      writeFileSync(join(trays, "darwin-arm64", "agentd-tray"), "#!/bin/sh\n")
      chmodSync(join(trays, "darwin-arm64", "agentd-tray"), 0o755)
      run("build-daemon.sh", [out], { TRAY_DIR: trays })
      const tgz = execFileSync("sh", ["-c", `ls ${out}/download/*/*.tgz`], { encoding: "utf8" }).trim()
      const listing = tarList(tgz)
      expect(listing).toMatch(/-rwxr-xr-x.* package\/tray\/darwin-arm64\/agentd-tray/)
      expect(listing).toContain("package/daemon.js")
    } finally {
      rmSync(trays, { recursive: true, force: true })
      rmSync(out, { recursive: true, force: true })
    }
  }, 120_000)

  test("a TRAY_DIR that is not a directory stops the build", () => {
    const out = mkdtempSync(join(tmpdir(), "mc-daemon-out-"))
    try {
      expect(() => run("build-daemon.sh", [out], { TRAY_DIR: join(tmpdir(), "mc-no-such-trays") })).toThrow("is not a directory")
    } finally {
      rmSync(out, { recursive: true, force: true })
    }
  }, 120_000)
})
