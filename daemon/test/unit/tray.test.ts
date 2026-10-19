import { describe, expect, test } from "bun:test"
import { openCommand, parseTrayLine, trayBinary, trayState, uiFd } from "../../src/core/tray.ts"

const claude = { name: "claude", title: "Claude Code", ready: true, reason: "" }
const facts = { version: "1", apps: [], browsers: [], sessions: 0, managed: false, gatewayPort: 47615, logFile: undefined, agents: [], updates: true, updatable: true }

describe("trayState", () => {
  test("is the state message the tray reads", () => {
    expect(trayState({ version: "1.2.3", apps: ["https://a"], browsers: ["http://localhost:5173"], sessions: 2, managed: true, gatewayPort: 47615, logFile: "/h/logs/daemon.log", agents: [claude], updates: false, updatable: true })).toEqual({
      t: "state",
      version: "1.2.3",
      apps: ["https://a"],
      pages: ["http://localhost:5173"],
      sessions: 2,
      managed: true,
      gatewayOff: false,
      log: true,
      agents: [claude],
      updates: false,
      updatable: true,
    })
  })

  test("says when no port was free for the gateway", () => {
    expect(trayState({ ...facts, gatewayPort: undefined }).gatewayOff).toBe(true)
  })

  test("says when there is no log file to open: the daemon writes to its terminal", () => {
    expect(trayState(facts).log).toBe(false)
  })

  test("carries only what the window shows of an agent", () => {
    expect(trayState({ ...facts, agents: [{ ...claude, command: "secret" } as typeof claude] }).agents).toEqual([claude])
  })
})

describe("parseTrayLine", () => {
  test("reads what a user can choose in the menu and the settings window", () => {
    expect(parseTrayLine('{"t":"action","id":"logs"}')).toEqual({ kind: "logs" })
    expect(parseTrayLine('{"t":"action","id":"restart"}')).toEqual({ kind: "restart" })
    expect(parseTrayLine('{"t":"action","id":"stop"}')).toEqual({ kind: "stop" })
    expect(parseTrayLine('{"t":"action","id":"disconnect:https://a.example"}')).toEqual({ kind: "disconnect", origin: "https://a.example" })
    expect(parseTrayLine('{"t":"action","id":"updates:on"}')).toEqual({ kind: "updates", on: true })
    expect(parseTrayLine('{"t":"action","id":"updates:off"}')).toEqual({ kind: "updates", on: false })
  })

  test("anything else is ignored: unknown ids, a disconnect without a valid origin, an updates choice that is neither, other messages, not JSON", () => {
    for (const bad of ['{"t":"action","id":"format-disk"}', '{"t":"action","id":"updates:"}', '{"t":"action","id":"updates:yes"}', '{"t":"action","id":"settings"}', '{"t":"action","id":"disconnect:"}', '{"t":"action","id":"disconnect:ftp://x"}', '{"t":"action","id":"disconnect:http://evil.example"}', '{"t":"state"}', '{"id":"stop"}', "oops", ""]) {
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

describe("uiFd", () => {
  test("is the descriptor an app that started the daemon talks to it on: none without AGENTD_UI_FD", () => {
    expect(uiFd({})).toBeUndefined()
    expect(uiFd({ AGENTD_UI_FD: "" })).toBeUndefined()
    expect(uiFd({ AGENTD_UI_FD: "3" })).toBe(3)
    expect(uiFd({ AGENTD_UI_FD: "7" })).toBe(7)
  })

  test("refuses what is not a descriptor of its own: the standard streams are the daemon's log and nothing", () => {
    for (const bad of ["0", "1", "2", "-3", "3.5", "x"]) expect(() => uiFd({ AGENTD_UI_FD: bad })).toThrow("AGENTD_UI_FD")
  })
})
