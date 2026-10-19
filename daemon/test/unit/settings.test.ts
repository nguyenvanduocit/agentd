import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { DEFAULT_SETTINGS, parseSettings, settingsFile } from "../../src/core/settings.ts"
import { SettingsStore } from "../../src/settings.ts"

let dir: string
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "mc-settings-unit-"))
})
afterEach(() => rmSync(dir, { recursive: true, force: true }))

describe("parseSettings", () => {
  test("reads what was saved", () => {
    expect(parseSettings('{"updates":false}')).toEqual({ updates: false })
  })

  test("a setting that is missing or not of its type keeps its default", () => {
    for (const text of ["{}", '{"updates":"no"}', '{"updates":null}', "[]", "null", "3"]) expect(parseSettings(text)).toEqual(DEFAULT_SETTINGS)
  })

  test("text that is not JSON throws", () => {
    expect(() => parseSettings("{")).toThrow()
  })

  test("updates are on by default", () => {
    expect(DEFAULT_SETTINGS.updates).toBe(true)
  })
})

describe("settingsFile", () => {
  test("sits beside the registry file, so --config moves both", () => {
    expect(settingsFile(join("/c", "agentd", "daemon.json"))).toBe(join("/c", "agentd", "settings.json"))
  })
})

describe("SettingsStore", () => {
  const file = () => join(dir, "agentd", "settings.json")

  test("a missing file is the defaults, and nothing is written until a change", async () => {
    const s = await SettingsStore.open(file())
    expect(s.get()).toEqual(DEFAULT_SETTINGS)
    expect(readdirSync(dir)).toEqual([])
  })

  test("a file that is not JSON is the defaults", async () => {
    mkdirSync(join(dir, "agentd"))
    writeFileSync(file(), "{")
    expect((await SettingsStore.open(file())).get()).toEqual(DEFAULT_SETTINGS)
  })

  test("a change is saved, whole, and read back by the next daemon", async () => {
    const s = await SettingsStore.open(file())
    expect(await s.change({ updates: false })).toEqual({ updates: false })
    expect(s.get()).toEqual({ updates: false })
    expect(JSON.parse(readFileSync(file(), "utf8"))).toEqual({ updates: false })
    expect(readdirSync(join(dir, "agentd"))).toEqual(["settings.json"])
    expect((await SettingsStore.open(file())).get()).toEqual({ updates: false })
  })

  test("changes are saved in the order they were made", async () => {
    const s = await SettingsStore.open(file())
    await Promise.all([s.change({ updates: false }), s.change({ updates: true }), s.change({ updates: false })])
    expect(s.get()).toEqual({ updates: false })
    expect(JSON.parse(readFileSync(file(), "utf8"))).toEqual({ updates: false })
  })

  test("a change that cannot be saved is refused and leaves the settings as they were", async () => {
    writeFileSync(join(dir, "agentd"), "a file where the directory should be")
    const s = await SettingsStore.open(file())
    await expect(s.change({ updates: false })).rejects.toThrow()
    await s.change({ updates: true }).catch(() => undefined)
    expect(s.get()).toEqual(DEFAULT_SETTINGS)
  })
})
