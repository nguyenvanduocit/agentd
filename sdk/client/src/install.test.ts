import { describe, expect, test } from "bun:test"
import { AGENTD_COMMANDS, installCommand } from "./install.ts"

const AGENTD = "https://agentd.example.com"

describe("installCommand", () => {
  test("is the curl line that installs, pairs and starts a daemon: from agentd's server, for this app, with this code", () => {
    expect(installCommand({ agentd: AGENTD, app: "https://mind.example.com", code: "K7QX-29PD" })).toBe(
      "curl -fsSL https://agentd.example.com/install.sh | bash -s -- --app https://mind.example.com --code K7QX-29PD",
    )
  })

  test("takes an origin with a port, an IPv6 literal and plain http on loopback", () => {
    expect(installCommand({ agentd: "http://127.0.0.1:7100", app: "http://localhost:5173", code: "ABCD-1234" })).toBe(
      "curl -fsSL http://127.0.0.1:7100/install.sh | bash -s -- --app http://localhost:5173 --code ABCD-1234",
    )
    expect(installCommand({ agentd: AGENTD, app: "http://[::1]:3000", code: "ABCD-1234" })).toContain("--app http://[::1]:3000")
  })

  test("is undefined when anything would not be safe to paste into a shell", () => {
    const ok = { agentd: AGENTD, app: "https://mind.example.com", code: "K7QX-29PD" }
    for (const bad of [
      { ...ok, app: "https://mind.example.com/path" },
      { ...ok, app: "https://mind.example.com'; rm -rf ~; '" },
      { ...ok, app: "mind.example.com" },
      { ...ok, app: "ftp://mind.example.com" },
      { ...ok, agentd: "https://agentd.example.com/" },
      { ...ok, agentd: "https://agentd example" },
      { ...ok, agentd: "" },
      { ...ok, code: "abc" },
      { ...ok, code: "K7QX-29PD; touch pwned" },
      { ...ok, code: "x".repeat(33) },
      { ...ok, code: "" },
    ]) {
      expect(installCommand(bad)).toBeUndefined()
    }
  })
})

describe("AGENTD_COMMANDS", () => {
  test("are what a user runs on the machine that has the daemon", () => {
    expect(AGENTD_COMMANDS).toEqual({
      status: "agentd service status",
      logs: "~/.agentd/bin/agentd service logs",
      start: "agentd service start",
    })
  })
})
