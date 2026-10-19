import { describe, expect, test } from "bun:test"
import { installedVersion, layoutOf, versionDir } from "../../src/core/layout.ts"
import { agentdHome, type Host } from "../../src/core/paths.ts"

const host = (env: Host["env"] = {}): Host => ({ platform: "linux", env, home: "/home/ann" })

describe("agentdHome", () => {
  test("~/.agentd, or $AGENTD_HOME when it is set and not empty", () => {
    expect(agentdHome(host())).toBe("/home/ann/.agentd")
    expect(agentdHome(host({ AGENTD_HOME: "/x/mc" }))).toBe("/x/mc")
    expect(agentdHome(host({ AGENTD_HOME: "" }))).toBe("/home/ann/.agentd")
  })
})

describe("layoutOf", () => {
  test("every path hangs off the home", () => {
    const l = layoutOf("/h/.agentd")
    expect(l).toEqual({
      home: "/h/.agentd",
      nodeDir: "/h/.agentd/node",
      daemonDir: "/h/.agentd/daemon",
      currentLink: "/h/.agentd/daemon/current",
      previousFile: "/h/.agentd/daemon/previous",
      rejectedFile: "/h/.agentd/daemon/rejected",
      crashesFile: "/h/.agentd/daemon/crashes",
      launcher: "/h/.agentd/bin/agentd",
      logFile: "/h/.agentd/logs/daemon.log",
      loopPidFile: "/h/.agentd/run/loop.pid",
      adaptersDir: "/h/.agentd/adapters",
      lastExitFile: "/h/.agentd/run/last-exit",
    })
    expect(versionDir(l, "1.2.3")).toBe("/h/.agentd/daemon/1.2.3")
  })
})

describe("installedVersion", () => {
  const l = layoutOf("/h/.agentd")
  test("a daemon.js directly inside <home>/daemon/<version>", () => {
    expect(installedVersion("/h/.agentd/daemon/0.0.0-g1a2b/daemon.js", l)).toBe("0.0.0-g1a2b")
  })
  test("a source run, a test bundle, another home or another file name is not installed", () => {
    expect(installedVersion("/repo/daemon/src/main.ts", l)).toBeUndefined()
    expect(installedVersion("/tmp/build/daemon.js", l)).toBeUndefined()
    expect(installedVersion("/other/.agentd/daemon/1.0.0/daemon.js", l)).toBeUndefined()
    expect(installedVersion("/h/.agentd/daemon/1.0.0/main.js", l)).toBeUndefined()
    expect(installedVersion("/h/.agentd/daemon/1.0.0/sub/daemon.js", l)).toBeUndefined()
  })
})
