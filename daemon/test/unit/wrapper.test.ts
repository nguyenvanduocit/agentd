import { describe, expect, test } from "bun:test"
import { agentEnv, wrapperFileName, wrapperScript } from "../../src/core/wrapper.ts"

describe("wrapperScript", () => {
  test("POSIX: an sh script that execs node on the daemon with the baked arguments", () => {
    const s = wrapperScript("linux", { node: "/usr/bin/node", script: "/d/daemon.js", args: ["--config", "/c/daemon.json"] })
    expect(s).toBe(`#!/bin/sh\nexec '/usr/bin/node' '/d/daemon.js' 'mcp' '--config' '/c/daemon.json' "$@"\n`)
  })

  test("POSIX: a quote in a path cannot end the quoting", () => {
    const s = wrapperScript("linux", { node: "/it's/node", script: "/d.js", args: [] })
    expect(s).toContain(`'/it'\\''s/node'`)
  })

  test("POSIX: the script keeps shell metacharacters of a path inert", () => {
    const s = wrapperScript("linux", { node: "/a b/$(touch x)/node", script: "/d.js", args: [] })
    expect(s).toContain(`'/a b/$(touch x)/node'`)
  })

  test("Windows: a .cmd that forwards its arguments", () => {
    const s = wrapperScript("win32", { node: "C:\\n\\node.exe", script: "C:\\d\\daemon.js", args: ["--config", "C:\\c\\100%.json"] })
    expect(s).toBe('@echo off\r\n"C:\\n\\node.exe" "C:\\d\\daemon.js" "mcp" "--config" "C:\\c\\100%%.json" %*\r\n')
  })

  test("the file name is agentd-mcp, with .cmd on Windows", () => {
    expect(wrapperFileName("linux")).toBe("agentd-mcp")
    expect(wrapperFileName("darwin")).toBe("agentd-mcp")
    expect(wrapperFileName("win32")).toBe("agentd-mcp.cmd")
  })
})

describe("agentEnv", () => {
  test("puts the wrapper directory first on PATH and keeps the rest", () => {
    const env = agentEnv({ PATH: "/usr/bin:/bin", HOME: "/h", GONE: undefined }, "/tmp/bin", "linux")
    expect(env.PATH).toBe("/tmp/bin:/usr/bin:/bin")
    expect(env.HOME).toBe("/h")
    expect("GONE" in env).toBe(false)
  })

  test("an empty PATH is just the wrapper directory", () => {
    expect(agentEnv({}, "/tmp/bin", "linux").PATH).toBe("/tmp/bin")
  })

  test("does not change the environment it was given", () => {
    const base = { PATH: "/usr/bin" }
    agentEnv(base, "/tmp/bin", "linux")
    expect(base.PATH).toBe("/usr/bin")
  })

  test("Windows: keeps the variable's own spelling and the ; delimiter", () => {
    const env = agentEnv({ Path: "C:\\Windows" }, "C:\\bin", "win32")
    expect(env.Path).toBe("C:\\bin;C:\\Windows")
    expect("PATH" in env).toBe(false)
  })
})
