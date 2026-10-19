import { describe, expect, test } from "bun:test"
import { AGENTD_COMMANDS, AGENTD_RELEASES, desktopDownloads, installCommand } from "./install.ts"

const AGENTD = "https://agentd.example.com/releases"

describe("installCommand", () => {
  test("is the curl line that installs, pairs and starts a daemon: from agentd's latest GitHub release, for this app, with this code", () => {
    expect(installCommand({ app: "https://mind.example.com", code: "K7QX-29PD" })).toBe(
      "curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash -s -- --app https://mind.example.com --code K7QX-29PD",
    )
    expect(AGENTD_RELEASES).toBe("https://github.com/nguyenvanduocit/agentd/releases")
  })

  test("takes the releases page of another build (a fork, or a development server) instead", () => {
    expect(installCommand({ agentd: AGENTD, app: "https://mind.example.com", code: "K7QX-29PD" })).toBe(
      "curl -fsSL https://agentd.example.com/releases/latest/download/install.sh | bash -s -- --app https://mind.example.com --code K7QX-29PD",
    )
  })

  test("takes an origin with a port, an IPv6 literal and plain http on loopback", () => {
    expect(installCommand({ agentd: "http://127.0.0.1:7100", app: "http://localhost:5173", code: "ABCD-1234" })).toBe(
      "curl -fsSL http://127.0.0.1:7100/latest/download/install.sh | bash -s -- --app http://localhost:5173 --code ABCD-1234",
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
      { ...ok, agentd: "https://agentd.example.com/a/../b" },
      { ...ok, agentd: "https://agentd.example.com/a?x=1" },
      { ...ok, agentd: "https://agentd.example.com/a'b" },
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

describe("desktopDownloads", () => {
  test("are the disk images of agentd.app in agentd's latest GitHub release, one per processor", () => {
    expect(desktopDownloads()).toEqual({
      appleSilicon: "https://github.com/nguyenvanduocit/agentd/releases/latest/download/agentd-darwin-arm64.dmg",
      intel: "https://github.com/nguyenvanduocit/agentd/releases/latest/download/agentd-darwin-amd64.dmg",
    })
  })

  test("come from the releases page of another build instead", () => {
    expect(desktopDownloads({ agentd: "http://127.0.0.1:7100" })?.intel).toBe("http://127.0.0.1:7100/latest/download/agentd-darwin-amd64.dmg")
  })

  test("are undefined for a releases page that is not a plain http(s) URL", () => {
    for (const agentd of ["javascript:alert(1)", "https://agentd.example.com/a/../b", "https://agentd.example.com/a?x=1", ""]) {
      expect(desktopDownloads({ agentd })).toBeUndefined()
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
