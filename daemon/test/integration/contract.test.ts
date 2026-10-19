// The daemon's handlers must type-check against the contract. This test copies the contract and the daemon sources
// into a temporary tree, renames one method of the contract there, regenerates the client with socketrpc-gen, and
// runs tsc: the real files stay untouched.
import { afterAll, describe, expect, test } from "bun:test"
import { execFile } from "node:child_process"
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { promisify } from "node:util"

const exec = promisify(execFile)
const DAEMON = resolve(import.meta.dir, "../..")
const CONTRACT = resolve(DAEMON, "../contract/define.acp.ts")

const roots: string[] = []
afterAll(() => {
  for (const r of roots) rmSync(r, { recursive: true, force: true })
})

/** A tree <root>/contract + <root>/daemon with the sources, the contract rewritten by `edit`, the client regenerated. */
async function tree(edit: (contract: string) => string): Promise<string> {
  const root = mkdtempSync(join(tmpdir(), "mc-contract-"))
  roots.push(root)
  mkdirSync(join(root, "contract"))
  writeFileSync(join(root, "contract", "define.acp.ts"), edit(readFileSync(CONTRACT, "utf8")))
  await exec("bunx", ["socketrpc-gen", "./define.acp.ts", "--client", "typescript"], { cwd: join(root, "contract"), env: process.env })
  mkdirSync(join(root, "daemon"))
  cpSync(join(DAEMON, "src"), join(root, "daemon", "src"), { recursive: true })
  cpSync(join(DAEMON, "tsconfig.json"), join(root, "daemon", "tsconfig.json"))
  symlinkSync(join(DAEMON, "node_modules"), join(root, "daemon", "node_modules"))
  return join(root, "daemon")
}

async function tsc(cwd: string): Promise<{ code: number; out: string }> {
  try {
    const r = await exec(join(DAEMON, "node_modules/.bin/tsc"), ["--noEmit"], { cwd })
    return { code: 0, out: r.stdout }
  } catch (e) {
    const failed = e as { code: number; stdout: string }
    return { code: failed.code, out: failed.stdout }
  }
}

describe("contract", () => {
  test("the unchanged contract type-checks (control)", async () => {
    const r = await tsc(await tree((c) => c))
    expect(r).toEqual({ code: 0, out: "" })
  }, 120_000)

  test.each([
    ["write", "writeLine"],
    ["frame", "sendFrame"],
  ])("renaming %s to %s in the contract makes the daemon fail to type-check", async (from, to) => {
    const r = await tsc(await tree((c) => c.replace(new RegExp(`^(\\s+)${from}: `, "m"), `$1${to}: `)))
    expect(r.code).not.toBe(0)
    expect(r.out).toContain(`'${from}' does not exist on type`)
  }, 120_000)

  test("a changed parameter type fails too: open takes a number instead of an agent name", async () => {
    const r = await tsc(await tree((c) => c.replace("open: (sessionId: string, agent: string) => Opened", "open: (sessionId: string, agent: number) => Opened")))
    expect(r.code).not.toBe(0)
  }, 120_000)
})
