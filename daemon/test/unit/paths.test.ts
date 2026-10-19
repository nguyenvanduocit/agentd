import { describe, expect, test } from "bun:test"
import { configFile, sessionsFile, type Host } from "../../src/core/paths.ts"

const linux = (env: Host["env"] = {}): Host => ({ platform: "linux", env, home: "/home/ann" })
const windows = (env: Host["env"] = {}): Host => ({ platform: "win32", env, home: "C:\\Users\\ann" })

describe("configFile", () => {
  test("Linux and macOS: ~/.config by default", () => {
    expect(configFile(linux())).toBe("/home/ann/.config/agentd/daemon.json")
    expect(configFile({ ...linux(), platform: "darwin" })).toBe("/home/ann/.config/agentd/daemon.json")
  })

  test("$XDG_CONFIG_HOME wins; an empty one does not count", () => {
    expect(configFile(linux({ XDG_CONFIG_HOME: "/x/cfg" }))).toBe("/x/cfg/agentd/daemon.json")
    expect(configFile(linux({ XDG_CONFIG_HOME: "" }))).toBe("/home/ann/.config/agentd/daemon.json")
  })

  test("Windows: %APPDATA%", () => {
    expect(configFile(windows({ APPDATA: "C:\\Users\\ann\\AppData\\Roaming" }))).toBe("C:\\Users\\ann\\AppData\\Roaming\\agentd\\daemon.json")
    expect(configFile(windows())).toBe("C:\\Users\\ann\\AppData\\Roaming\\agentd\\daemon.json")
  })
})

describe("sessionsFile", () => {
  test("lives under the state directory, not the config directory", () => {
    expect(sessionsFile(linux())).toBe("/home/ann/.local/state/agentd/sessions.json")
    expect(sessionsFile(linux({ XDG_STATE_HOME: "/x/state" }))).toBe("/x/state/agentd/sessions.json")
    expect(sessionsFile(windows({ LOCALAPPDATA: "C:\\L" }))).toBe("C:\\L\\agentd\\sessions.json")
    expect(sessionsFile(windows())).toBe("C:\\Users\\ann\\AppData\\Local\\agentd\\sessions.json")
  })
})
