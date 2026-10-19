import { describe, expect, test } from "bun:test"
import { parseCli, USAGE } from "../../src/core/cli.ts"

const ok = (argv: string[]) => {
  const r = parseCli(argv)
  if (!r.ok) throw new Error(r.error)
  return r.command
}
const err = (argv: string[]) => {
  const r = parseCli(argv)
  if (r.ok) throw new Error("parsed")
  return r.error
}

describe("parseCli", () => {
  test("no arguments runs the daemon", () => {
    expect(ok([])).toEqual({ kind: "run", app: undefined, dir: undefined, config: undefined })
  })

  test("run takes --app, --dir and --config", () => {
    expect(ok(["--app", "https://s", "--dir", "/w", "--config", "/c.json"])).toEqual({
      kind: "run",
      app: "https://s",
      dir: "/w",
      config: "/c.json",
    })
    expect(ok(["--app=https://s"])).toMatchObject({ kind: "run", app: "https://s" })
  })

  test("run is the explicit name of the default command", () => {
    expect(ok(["run"])).toEqual({ kind: "run", app: undefined, dir: undefined, config: undefined })
    expect(ok(["run", "--dir", "/w"])).toMatchObject({ kind: "run", dir: "/w" })
    expect(err(["run", "extra"])).toContain('unexpected argument "extra"')
  })

  test("service takes install, uninstall, status or logs", () => {
    for (const action of ["install", "uninstall", "start", "restart", "status"] as const) {
      expect(ok(["service", action])).toEqual({ kind: "service", action, lines: 100, follow: false, dir: undefined })
    }
    expect(ok(["service", "logs", "-n", "20", "-f"])).toEqual({ kind: "service", action: "logs", lines: 20, follow: true, dir: undefined })
    expect(ok(["service", "logs", "--lines=5"])).toMatchObject({ lines: 5, follow: false })
    expect(ok(["service", "install", "--dir", "/work"])).toMatchObject({ action: "install", dir: "/work" })
    expect(err(["service"])).toContain("install, uninstall, start, restart, status, logs")
    expect(err(["service", "reboot"])).toContain("install, uninstall, start, restart, status, logs")
    expect(err(["service", "logs", "-n", "0"])).toContain("positive")
    expect(err(["service", "logs", "-n", "x"])).toContain("positive")
    expect(err(["service", "install", "extra"])).toContain('unexpected argument "extra"')
  })

  test("apps lists, or removes one origin", () => {
    expect(ok(["apps"])).toEqual({ kind: "apps", action: "list", origin: undefined, config: undefined })
    expect(ok(["apps", "list", "--config", "/c.json"])).toEqual({ kind: "apps", action: "list", origin: undefined, config: "/c.json" })
    expect(ok(["apps", "remove", "https://a"])).toEqual({ kind: "apps", action: "remove", origin: "https://a", config: undefined })
    expect(err(["apps", "remove"])).toContain("<origin>")
    expect(err(["apps", "frob"])).toContain("list, remove")
    expect(USAGE).toContain("apps remove")
  })

  test("version and help", () => {
    expect(ok(["version"])).toEqual({ kind: "version" })
    expect(ok(["--help"])).toEqual({ kind: "help" })
    expect(ok(["-h"])).toEqual({ kind: "help" })
    expect(USAGE).toContain("pair --app")
    expect(USAGE).toContain("service install")
  })

  test("pair needs a server; the code may come from the environment or stdin", () => {
    expect(ok(["pair", "--app", "https://s", "--code", "abc"])).toEqual({
      kind: "pair",
      app: "https://s",
      code: "abc",
      config: undefined,
    })
    expect(err(["pair", "--code", "abc"])).toContain("--app")
    // Without --code the code comes from AGENTD_PAIR_CODE or stdin, so it never shows in the process list.
    expect(ok(["pair", "--app", "https://s"])).toEqual({ kind: "pair", app: "https://s", code: undefined, config: undefined })
  })

  test("mcp needs a session", () => {
    expect(ok(["mcp", "--session", "s1", "--config", "/c.json"])).toEqual({
      kind: "mcp",
      session: "s1",
      app: undefined,
      config: "/c.json",
    })
    expect(err(["mcp"])).toContain("--session")
  })

  test("mistakes are errors, not guesses", () => {
    expect(err(["frobnicate"])).toContain('unknown command "frobnicate"')
    expect(err(["--nope"])).toContain("--nope")
    expect(err(["version", "extra"])).toContain('unexpected argument "extra"')
    expect(err(["--app"])).toContain("--app")
  })
})
