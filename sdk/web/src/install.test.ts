import { describe, expect, test } from "bun:test"
import { AGENTD_RELEASES, desktopDownloads, recommendInstall } from "./install.ts"

describe("desktopDownloads", () => {
  test("are the disk images of agentd.app in agentd's latest GitHub release, one per processor", () => {
    expect(AGENTD_RELEASES).toBe("https://github.com/nguyenvanduocit/agentd/releases")
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
  test.each<[string, string, "dmg" | "unsupported"]>([
    ["macOS by name", "macOS", "dmg"],
    ["macOS as Apple's userAgentData and navigator.platform write it", "MacIntel", "dmg"],
    ["darwin", "darwin", "dmg"],
    ["Linux", "Linux", "unsupported"],
    ["Linux as navigator.platform writes it", "Linux x86_64", "unsupported"],
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
    const as = (nav: object | undefined): "dmg" | "unsupported" => {
      Object.defineProperty(globalThis, "navigator", { value: nav, configurable: true, writable: true })
      return recommendInstall()
    }
    try {
      expect(as({ userAgentData: { platform: "macOS" }, platform: "Win32", userAgent: "" })).toBe("dmg")
      expect(as({ platform: "Linux x86_64", userAgent: "" })).toBe("unsupported")
      expect(as({ userAgent: "Mozilla/5.0 (X11; Linux x86_64)" })).toBe("unsupported")
      expect(as({ userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" })).toBe("unsupported")
      expect(as(undefined)).toBe("unsupported")
    } finally {
      if (real) Object.defineProperty(globalThis, "navigator", real)
    }
  })
})

