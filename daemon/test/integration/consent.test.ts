import { afterAll, describe, expect, test } from "bun:test"
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { ask } from "../../src/consent.ts"

const dir = mkdtempSync(join(tmpdir(), "mc-consent-"))
afterAll(() => rmSync(dir, { recursive: true, force: true }))

const q = { origin: "https://app.example.com", server: undefined, agents: ["claude"] }
function script(name: string, body: string): string {
  const file = join(dir, name)
  writeFileSync(file, `#!/bin/sh\n${body}\n`)
  chmodSync(file, 0o755)
  return file
}

describe("ask with the AGENTD_CONSENT seam", () => {
  test("exit 0 allows, and the program gets the title and the body", async () => {
    const seen = join(dir, "seen")
    const allow = script("allow", `printf '%s\\n%s' "$1" "$2" > ${seen}\nexit 0`)
    expect(await ask(q, { AGENTD_CONSENT: allow })).toBe("allow")
    const [title, ...body] = readFileSync(seen, "utf8").split("\n")
    expect(title).toContain("https://app.example.com")
    expect(body.join("\n")).toContain("files")
  })

  test("a non-zero exit denies", async () => {
    expect(await ask(q, { AGENTD_CONSENT: script("deny", "exit 1") })).toBe("deny")
  })

  test("a program that is not there is unavailable, never allow", async () => {
    expect(await ask(q, { AGENTD_CONSENT: join(dir, "missing") })).toBe("unavailable")
  })

  test("a program that outlives the timeout is a timeout", async () => {
    // timeout + 5 s grace in run(): use a 1 s timeout so the test waits about 6 s
    expect(await ask(q, { AGENTD_CONSENT: script("hang", "sleep 30") }, 1)).toBe("timeout")
  }, 20_000)
})
