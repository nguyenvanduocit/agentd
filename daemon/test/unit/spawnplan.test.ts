import { describe, expect, test } from "bun:test"
import { spawnPlan, taskkillArgs } from "../../src/core/spawnplan.ts"

describe("spawnPlan", () => {
  test("POSIX: no shell, own process group", () => {
    expect(spawnPlan("/usr/bin/npx", ["--yes", "x@1"], "linux")).toEqual({ file: "/usr/bin/npx", args: ["--yes", "x@1"], shell: false, detached: true })
  })

  test("Windows: an .exe runs directly, not detached", () => {
    expect(spawnPlan("C:\\n\\grok.exe", ["agent"], "win32")).toEqual({ file: "C:\\n\\grok.exe", args: ["agent"], shell: false, detached: false })
  })

  test("Windows: .cmd and .bat go through the shell with quoted arguments", () => {
    const p = spawnPlan("C:\\Program Files\\nodejs\\npx.cmd", ["--yes", "a b", ""], "win32")
    expect(p).toEqual({ file: '"C:\\Program Files\\nodejs\\npx.cmd"', args: ["--yes", '"a b"', '""'], shell: true, detached: false })
    expect(spawnPlan("C:\\x.BAT", [], "win32").shell).toBe(true)
  })

  test("taskkill ends the tree", () => {
    expect(taskkillArgs(42)).toEqual(["/pid", "42", "/T", "/F"])
  })
})
