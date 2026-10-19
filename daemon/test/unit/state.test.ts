import { afterEach, expect, test } from "bun:test"
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { SessionState } from "../../src/state.ts"

const dirs: string[] = []
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

function binIn(parent: string, prefix: string): string {
  const dir = mkdtempSync(join(parent, prefix))
  dirs.push(dir)
  mkdirSync(join(dir, "bin"))
  return join(dir, "bin")
}

/** A state file left by a dead daemon (no process has that pid) that names `bin`; then a new daemon starts. */
async function startAfter(bin: string): Promise<void> {
  const box = mkdtempSync(join(tmpdir(), "mc-state-test-"))
  dirs.push(box)
  const file = join(box, "sessions.json")
  writeFileSync(file, JSON.stringify({ daemon: { pid: 2 ** 30, lstart: "x" }, bin, groups: [] }))
  await new SessionState(file, "").start()
}

test.skipIf(process.platform === "win32")("the next start removes the wrapper directory a daemon made, and no other path the file names", async () => {
  const own = binIn(tmpdir(), "agentd-")
  await startAfter(own)
  expect(existsSync(dirname(own))).toBe(false)

  const notOurs = binIn(tmpdir(), "mc-state-test-docs-")
  const notInTmp = binIn(mkdtempSync(join(tmpdir(), "mc-state-test-home-")), "agentd-")
  dirs.push(dirname(dirname(notInTmp)))
  for (const bin of [notOurs, notInTmp]) {
    await startAfter(bin)
    expect(existsSync(bin)).toBe(true)
  }
})
