import { describe, expect, test } from "bun:test"
import { openCommand, parseTrayLine, trayBinary, trayState } from "../../src/core/tray.ts"

describe("trayState", () => {
  test("is the state message the tray reads", () => {
    expect(trayState({ version: "1.2.3", apps: ["https://a"], browsers: ["http://localhost:5173"], sessions: 2, managed: true, gatewayPort: 47615 })).toEqual({
      t: "state",
      version: "1.2.3",
      apps: ["https://a"],
      pages: ["http://localhost:5173"],
      sessions: 2,
      managed: true,
      gatewayOff: false,
    })
  })

  test("says when no port was free for the gateway", () => {
    expect(trayState({ version: "1", apps: [], browsers: [], sessions: 0, managed: false, gatewayPort: undefined }).gatewayOff).toBe(true)
  })
})

describe("parseTrayLine", () => {
  test("reads the four things a user can choose", () => {
    expect(parseTrayLine('{"t":"action","id":"logs"}')).toEqual({ kind: "logs" })
    expect(parseTrayLine('{"t":"action","id":"restart"}')).toEqual({ kind: "restart" })
    expect(parseTrayLine('{"t":"action","id":"stop"}')).toEqual({ kind: "stop" })
    expect(parseTrayLine('{"t":"action","id":"disconnect:https://a.example"}')).toEqual({ kind: "disconnect", origin: "https://a.example" })
  })

  test("anything else is ignored: unknown ids, a disconnect without a valid origin, other messages, not JSON", () => {
    for (const bad of ['{"t":"action","id":"format-disk"}', '{"t":"action","id":"disconnect:"}', '{"t":"action","id":"disconnect:ftp://x"}', '{"t":"action","id":"disconnect:http://evil.example"}', '{"t":"state"}', '{"id":"stop"}', "oops", ""]) {
      expect(parseTrayLine(bad)).toBeUndefined()
    }
  })
})

describe("trayBinary", () => {
  test("is under tray/<os>-<arch> beside daemon.js, with .exe on Windows", () => {
    expect(trayBinary("/h/daemon/1.0.0", "darwin", "arm64")).toBe("/h/daemon/1.0.0/tray/darwin-arm64/agentd-tray")
    expect(trayBinary("/h/daemon/1.0.0", "darwin", "x64")).toBe("/h/daemon/1.0.0/tray/darwin-amd64/agentd-tray")
    expect(trayBinary("/h/daemon/1.0.0", "linux", "x64")).toBe("/h/daemon/1.0.0/tray/linux-amd64/agentd-tray")
    expect(trayBinary("/h/daemon/1.0.0", "linux", "arm64")).toBe("/h/daemon/1.0.0/tray/linux-arm64/agentd-tray")
    expect(trayBinary("C:\\h\\daemon\\1.0.0", "win32", "x64")).toBe("C:\\h\\daemon\\1.0.0\\tray\\windows-amd64\\agentd-tray.exe")
  })

  test("a platform without a build has no tray", () => {
    expect(trayBinary("/d", "freebsd", "x64")).toBeUndefined()
    expect(trayBinary("/d", "linux", "ia32")).toBeUndefined()
    expect(trayBinary("C:\\d", "win32", "arm64")).toBeUndefined()
  })
})

describe("openCommand", () => {
  test("opens a file with the platform's own opener", () => {
    expect(openCommand("darwin", "/l/daemon.log")).toEqual({ file: "open", args: ["/l/daemon.log"] })
    expect(openCommand("linux", "/l/daemon.log")).toEqual({ file: "xdg-open", args: ["/l/daemon.log"] })
    expect(openCommand("win32", "C:\\l\\daemon.log")).toEqual({ file: "cmd", args: ["/c", "start", "", "C:\\l\\daemon.log"] })
  })
})
