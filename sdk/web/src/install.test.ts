import { describe, expect, test } from "bun:test"
import { AGENTD_COMMANDS, AGENTD_RELEASES, desktopDownloads, installCommand, recommendInstall } from "./install.ts"

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

  test("without an app and a code it only installs and starts: a page in browser mode pairs with nothing", () => {
    expect(installCommand({})).toBe("curl -fsSL https://github.com/nguyenvanduocit/agentd/releases/latest/download/install.sh | bash")
    expect(installCommand({ agentd: AGENTD })).toBe("curl -fsSL https://agentd.example.com/releases/latest/download/install.sh | bash")
  })

  test("an app without its code, or a code without its app, is not a command: the installer refuses either", () => {
    expect(installCommand({ app: "https://mind.example.com" })).toBeUndefined()
    expect(installCommand({ code: "K7QX-29PD" })).toBeUndefined()
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

describe("recommendInstall", () => {
  test.each<[string, string, "dmg" | "script" | "unsupported"]>([
    ["macOS by name", "macOS", "dmg"],
    ["macOS as Apple's userAgentData and navigator.platform write it", "MacIntel", "dmg"],
    ["darwin", "darwin", "dmg"],
    ["Linux", "Linux", "script"],
    ["Linux as navigator.platform writes it", "Linux x86_64", "script"],
    ["Windows by name", "Windows", "unsupported"],
    ["Windows as navigator.platform writes it", "Win32", "unsupported"],
    ["Android, whose user agent says Linux", "Linux; Android 14", "unsupported"],
    ["an iPhone, whose user agent says Mac OS X", "iPhone; CPU iPhone OS 17_0 like Mac OS X", "unsupported"],
    ["ChromeOS", "CrOS", "unsupported"],
    ["nothing", "", "unsupported"],
  ])("%s is %s", (_name, os, want) => {
    expect(recommendInstall({ os })).toBe(want)
  })

  test("a platform that says nothing is unsupported", () => {
    expect(recommendInstall({})).toBe("unsupported")
    expect(recommendInstall({ arch: "arm64" })).toBe("unsupported")
  })

  test("the processor does not change the answer: both disk images are offered", () => {
    expect(recommendInstall({ os: "macOS", arch: "arm64" })).toBe("dmg")
    expect(recommendInstall({ os: "macOS", arch: "x86" })).toBe("dmg")
  })

  test("without an argument it reads the browser: userAgentData first, then platform, then the user agent", () => {
    const real = Object.getOwnPropertyDescriptor(globalThis, "navigator")
    const as = (nav: object | undefined): "dmg" | "script" | "unsupported" => {
      Object.defineProperty(globalThis, "navigator", { value: nav, configurable: true, writable: true })
      return recommendInstall()
    }
    try {
      expect(as({ userAgentData: { platform: "macOS" }, platform: "Win32", userAgent: "" })).toBe("dmg")
      expect(as({ platform: "Linux x86_64", userAgent: "" })).toBe("script")
      expect(as({ userAgent: "Mozilla/5.0 (X11; Linux x86_64)" })).toBe("script")
      expect(as({ userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" })).toBe("unsupported")
      expect(as(undefined)).toBe("unsupported")
    } finally {
      if (real) Object.defineProperty(globalThis, "navigator", real)
    }
  })
})

describe("AGENTD_COMMANDS", () => {
  test("are what a user runs on the machine that has the daemon", () => {
    expect(AGENTD_COMMANDS).toEqual({
      status: "~/.agentd/bin/agentd service status",
      logs: "~/.agentd/bin/agentd service logs",
      start: "~/.agentd/bin/agentd service start",
    })
  })

  test("name the launcher the installer writes, because neither install puts agentd on PATH", () => {
    for (const command of Object.values(AGENTD_COMMANDS)) expect(command.startsWith("~/.agentd/bin/agentd ")).toBe(true)
  })
})
