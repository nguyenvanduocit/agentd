import { describe, expect, test } from "bun:test"
import {
  ADAPTER_RETRY,
  ADAPTERS,
  adaptersDisabled,
  adapterAgentDir,
  adapterDir,
  adapterKey,
  adapterSpec,
  binEntry,
  failedReason,
  installingReason,
  isPartial,
  markerText,
  npmArgs,
  npmTail,
  oldInstalls,
  packageDir,
  parseMarker,
  partialDir,
  specVariable,
} from "../../src/core/adapters.ts"
import { backoffDelay } from "../../src/core/backoff.ts"
import { layoutOf } from "../../src/core/layout.ts"

const l = layoutOf("/h/.agentd")

describe("the pinned adapters", () => {
  test("claude-agent-acp 0.86.0 and codex-acp 2.1.1, installed by their exact version", () => {
    expect(adapterSpec(ADAPTERS.claude, {})).toBe("@agentclientprotocol/claude-agent-acp@0.86.0")
    expect(adapterSpec(ADAPTERS.codex, {})).toBe("@agentclientprotocol/codex-acp@2.1.1")
    expect(adapterKey(ADAPTERS.claude, {})).toBe("0.86.0")
  })

  test("the seam replaces the spec, and gets a directory of its own", () => {
    expect(specVariable("codex")).toBe("AGENTD_ADAPTER_SPEC_CODEX")
    const env = { AGENTD_ADAPTER_SPEC_CODEX: "file:/x/fake.tgz" }
    expect(adapterSpec(ADAPTERS.codex, env)).toBe("file:/x/fake.tgz")
    expect(adapterKey(ADAPTERS.codex, env)).toMatch(/^spec-[0-9a-f]{12}$/)
    expect(adapterKey(ADAPTERS.codex, env)).not.toBe(adapterKey(ADAPTERS.codex, { AGENTD_ADAPTER_SPEC_CODEX: "file:/y.tgz" }))
    expect(adapterSpec(ADAPTERS.codex, { AGENTD_ADAPTER_SPEC_CODEX: "  " })).toBe("@agentclientprotocol/codex-acp@2.1.1")
  })
})

describe("the store's paths", () => {
  test("adapters/<agent>/<version>, a partial directory beside it", () => {
    expect(adapterAgentDir(l, "claude")).toBe("/h/.agentd/adapters/claude")
    expect(adapterDir(l, "claude", "0.86.0")).toBe("/h/.agentd/adapters/claude/0.86.0")
    expect(partialDir(l, "claude", "0.86.0", 42)).toBe("/h/.agentd/adapters/claude/.partial-0.86.0-42")
    expect(isPartial(".partial-0.86.0-42")).toBe(true)
    expect(isPartial("0.86.0")).toBe(false)
    expect(packageDir("/i", "@a/b")).toBe("/i/node_modules/@a/b")
  })

  test("npm gets one package into its own prefix, quietly", () => {
    expect(npmArgs("/p", "@a/b@1")).toEqual(["install", "--prefix", "/p", "--no-audit", "--no-fund", "--loglevel", "error", "@a/b@1"])
  })
})

describe("binEntry reads the bin of the installed package", () => {
  test("an object keyed like the package, a string, or the only entry", () => {
    expect(binEntry("@agentclientprotocol/codex-acp", { bin: { "codex-acp": "dist/index.js" } })).toEqual({ ok: true, bin: "dist/index.js" })
    expect(binEntry("pkg", { bin: "./cli.js" })).toEqual({ ok: true, bin: "cli.js" })
    expect(binEntry("pkg", { bin: { other: "x.js" } })).toEqual({ ok: true, bin: "x.js" })
    expect(binEntry("pkg", { bin: { a: "a.js", pkg: "p.js" } })).toEqual({ ok: true, bin: "p.js" })
  })

  test("nothing is guessed: no bin, several unrelated bins, or a path that leaves the package", () => {
    expect(binEntry("pkg", {}).ok).toBe(false)
    expect(binEntry("pkg", { bin: {} }).ok).toBe(false)
    expect(binEntry("pkg", { bin: { a: "a.js", b: "b.js" } }).ok).toBe(false)
    expect(binEntry("pkg", { bin: "../../evil.js" }).ok).toBe(false)
    expect(binEntry("pkg", { bin: "/etc/passwd" }).ok).toBe(false)
    expect(binEntry("pkg", null).ok).toBe(false)
  })
})

describe("the completion marker", () => {
  test("a marker names the executable relative to the install directory", () => {
    expect(parseMarker(markerText("@a/b@1", "node_modules/@a/b/dist/index.js"))).toBe("node_modules/@a/b/dist/index.js")
  })

  test("a missing, torn or hostile marker is not a complete install", () => {
    for (const bad of ["", "{", "{}", '{"bin":""}', '{"bin":"/abs"}', '{"bin":"../x"}', '{"bin":5}', "null"]) expect(parseMarker(bad)).toBeUndefined()
  })
})

describe("old versions", () => {
  test("all but the current and the previous (the newest other) are removed; partials are not listed here", () => {
    const entries = [
      { name: "0.1.0", mtimeMs: 1 },
      { name: "0.2.0", mtimeMs: 2 },
      { name: "0.3.0", mtimeMs: 3 },
      { name: "0.4.0", mtimeMs: 4 },
      { name: ".partial-x-1", mtimeMs: 9 },
    ]
    expect(oldInstalls(entries, "0.4.0")).toEqual(["0.2.0", "0.1.0"])
    expect(oldInstalls(entries, "0.1.0")).toEqual(["0.3.0", "0.2.0"])
    expect(oldInstalls([{ name: "0.4.0", mtimeMs: 4 }], "0.4.0")).toEqual([])
  })
})

test("AGENTD_ADAPTERS=off disables the install, nothing else does", () => {
  expect(adaptersDisabled({ AGENTD_ADAPTERS: "off" })).toBe(true)
  expect(adaptersDisabled({ AGENTD_ADAPTERS: "on" })).toBe(false)
  expect(adaptersDisabled({})).toBe(false)
})

describe("messages and retry", () => {
  test("the reasons say what is going on", () => {
    expect(installingReason(ADAPTERS.codex)).toBe("installing the Codex adapter (first run only, this can take several minutes)")
    expect(failedReason(ADAPTERS.claude, "ENOTFOUND registry.npmjs.org", 61_400)).toBe("cannot install the Claude adapter: ENOTFOUND registry.npmjs.org; retrying in 61 s")
  })

  test("npmTail keeps the last lines and a bounded length", () => {
    expect(npmTail("a\n\n  b \nc\nd\ne\nf\n")).toBe("c | d | e | f")
    expect(npmTail("x".repeat(1000)).length).toBe(400)
    expect(npmTail("")).toBe("")
  })

  test("retries start at 60 s and double up to 30 min", () => {
    const noJitter = { ...ADAPTER_RETRY, jitter: 0 }
    expect([0, 1, 2, 3, 4, 5, 6, 7, 20].map((n) => backoffDelay(n, noJitter) / 1000)).toEqual([60, 120, 240, 480, 960, 1800, 1800, 1800, 1800])
  })
})
