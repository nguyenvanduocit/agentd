import { afterEach, describe, expect, test } from "bun:test"
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { tmpdir, userInfo } from "node:os"
import { dirname, join } from "node:path"
import { writeApp } from "../../src/appentry.ts"
import { macAppScript } from "../../src/core/appentry.ts"
import { layoutOf } from "../../src/core/layout.ts"
import type { Host } from "../../src/core/paths.ts"

const ROOT = join(import.meta.dir, "..", "..", "..")
const dirs: string[] = []
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

/** An installed version with its app icons, and a directory the app goes in (never the real Applications). */
function install() {
  const root = mkdtempSync(join(tmpdir(), "agentd-app-"))
  dirs.push(root)
  const l = layoutOf(join(root, "agentd"))
  mkdirSync(join(l.daemonDir, "0.3.2", "app"), { recursive: true })
  mkdirSync(dirname(l.launcher), { recursive: true })
  writeFileSync(l.launcher, "#!/bin/sh\n")
  for (const icon of ["agentd.icns", "agentd.png"]) copyFileSync(join(ROOT, "daemon", "app", icon), join(l.daemonDir, "0.3.2", "app", icon))
  symlinkSync("0.3.2", l.currentLink)
  const apps = join(root, "Applications")
  const host = (env: Record<string, string> = {}): Host => ({ platform: "darwin", home: userInfo().homedir, env: { AGENTD_APPS_DIR: apps, ...env } })
  return { l, app: join(apps, "agentd.app"), host }
}

const script = (app: string) => readFileSync(join(app, "Contents", "MacOS", "agentd"), "utf8")

describe("writeApp", () => {
  test("refresh replaces a script that starts another install's launcher, such as a test's that is gone", () => {
    const { l, app, host } = install()
    writeApp(l, host(), "0.3.2", "install")
    const stray = macAppScript("/tmp/gone/.agentd/bin/agentd", { PATH: "/bin" })
    writeFileSync(join(app, "Contents", "MacOS", "agentd"), stray)
    expect(script(app)).toBe(stray)
    writeApp(l, host(), "0.3.2", "refresh")
    expect(script(app)).toContain(`L='${l.launcher}'`)
  })

  test("refresh keeps the script of its own install: the service name service install gave it stays", () => {
    const { l, app, host } = install()
    writeApp(l, host({ AGENTD_SERVICE_LABEL: "com.agentd.daemon.test" }), "0.3.2", "install")
    writeApp(l, host(), "0.3.2", "refresh")
    expect(script(app)).toContain("export AGENTD_SERVICE_LABEL='com.agentd.daemon.test'")
    expect(existsSync(join(app, "Contents", "Resources", "agentd.icns"))).toBe(true)
  })

  test("refresh keeps the script when its launcher is the same file by another path (/var and /private/var on macOS)", () => {
    const { l, app, host } = install()
    const alias = mkdtempSync(join(tmpdir(), "agentd-alias-"))
    dirs.push(alias)
    rmSync(alias, { recursive: true })
    symlinkSync(l.home, alias)
    writeApp(layoutOf(alias), host({ AGENTD_SERVICE_LABEL: "com.agentd.daemon.test" }), "0.3.2", "install")
    writeApp(l, host(), "0.3.2", "refresh")
    expect(script(app)).toContain("export AGENTD_SERVICE_LABEL='com.agentd.daemon.test'")
  })

  test("an install under a HOME that is not the account's own makes no app", () => {
    const { l } = install()
    expect(writeApp(l, { platform: "darwin", home: "/tmp/e2e-machine", env: {} }, "0.3.2", "install")).toBeUndefined()
    expect(writeApp(l, { platform: "darwin", home: "/tmp/e2e-machine", env: {} }, "0.3.2", "refresh")).toBeUndefined()
  })
})
