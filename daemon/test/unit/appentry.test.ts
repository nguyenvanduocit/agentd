import { describe, expect, test } from "bun:test"
import { appCandidates, appEnv, appPlace, BUNDLE_ID, desktopEntry, entryLauncher, infoPlist, isOurDesktopEntry, isOurPlist, macAppScript } from "../../src/core/appentry.ts"

const HOME = "/Users/ann"
const writable = (dirs: string[]) => (dir: string) => dirs.includes(dir)

describe("appPlace", () => {
  test("macOS: /Applications when this user may write it, else ~/Applications", () => {
    expect(appPlace("darwin", HOME, HOME, {}, writable(["/Applications"]))).toEqual({ kind: "mac", file: "/Applications/agentd.app" })
    expect(appPlace("darwin", HOME, HOME, {}, writable([]))).toEqual({ kind: "mac", file: "/Users/ann/Applications/agentd.app" })
  })

  test("Linux: a desktop entry in $XDG_DATA_HOME/applications, else ~/.local/share/applications", () => {
    expect(appPlace("linux", HOME, HOME, {}, writable([]))).toEqual({ kind: "linux", file: "/Users/ann/.local/share/applications/agentd.desktop" })
    expect(appPlace("linux", HOME, HOME, { XDG_DATA_HOME: "/d" }, writable([]))).toEqual({ kind: "linux", file: "/d/applications/agentd.desktop" })
  })

  test("an agentd whose home was moved makes no app, unless AGENTD_APPS_DIR says where", () => {
    expect(appPlace("darwin", HOME, HOME, { AGENTD_HOME: "/tmp/x" }, writable(["/Applications"]))).toBeUndefined()
    expect(appPlace("darwin", HOME, HOME, { AGENTD_HOME: "/tmp/x", AGENTD_APPS_DIR: "/tmp/apps" }, writable([]))).toEqual({ kind: "mac", file: "/tmp/apps/agentd.app" })
    expect(appPlace("linux", HOME, HOME, { AGENTD_HOME: "/tmp/x", AGENTD_APPS_DIR: "/tmp/apps" }, writable([]))).toEqual({ kind: "linux", file: "/tmp/apps/agentd.desktop" })
    expect(appPlace("darwin", HOME, HOME, { AGENTD_HOME: "" }, writable(["/Applications"]))?.file).toBe("/Applications/agentd.app")
  })

  test("a home that is not the account's own (a test's HOME) makes no app either, unless AGENTD_APPS_DIR says where", () => {
    expect(appPlace("darwin", "/tmp/e2e/machine", HOME, {}, writable(["/Applications"]))).toBeUndefined()
    expect(appCandidates("darwin", "/tmp/e2e/machine", HOME, {})).toEqual([])
    expect(appPlace("darwin", "/tmp/e2e/machine", HOME, { AGENTD_APPS_DIR: "/tmp/apps" }, writable([]))?.file).toBe("/tmp/apps/agentd.app")
  })

  test("no app on a platform without one", () => {
    expect(appPlace("win32", HOME, HOME, {}, writable([]))).toBeUndefined()
  })
})

describe("appCandidates", () => {
  test("uninstall looks in every place an app may have been made, and nowhere for a moved home", () => {
    expect(appCandidates("darwin", HOME, HOME, {})).toEqual(["/Applications/agentd.app", "/Users/ann/Applications/agentd.app"])
    expect(appCandidates("linux", HOME, HOME, {})).toEqual(["/Users/ann/.local/share/applications/agentd.desktop"])
    expect(appCandidates("darwin", HOME, HOME, { AGENTD_HOME: "/tmp/x" })).toEqual([])
    expect(appCandidates("darwin", HOME, HOME, { AGENTD_HOME: "/tmp/x", AGENTD_APPS_DIR: "/tmp/apps" })).toEqual(["/tmp/apps/agentd.app"])
  })
})

describe("appEnv", () => {
  test("the app runs the launcher in the service's environment and names the service it was made for", () => {
    const env = appEnv({ PATH: "/bin", HOME: "/h", SECRET: "x", AGENTD_HOME: "/a", AGENTD_SERVICE_LABEL: "com.x", AGENTD_SERVICE_DIR: "/p", AGENTD_RESTART: "loop" })
    expect(env).toEqual({ PATH: "/bin", AGENTD_HOME: "/a", AGENTD_SERVICE_LABEL: "com.x", AGENTD_SERVICE_DIR: "/p" })
  })
})

describe("the macOS app", () => {
  const plist = infoPlist("0.3.1")

  test("Info.plist names the bundle, its script and its icon, and keeps it out of the Dock", () => {
    for (const s of [
      `<key>CFBundleIdentifier</key>\n  <string>${BUNDLE_ID}</string>`,
      "<key>CFBundleExecutable</key>\n  <string>agentd</string>",
      "<key>CFBundleIconFile</key>\n  <string>agentd</string>",
      "<key>CFBundlePackageType</key>\n  <string>APPL</string>",
      "<key>CFBundleShortVersionString</key>\n  <string>0.3.1</string>",
      "<key>LSUIElement</key>\n  <true/>",
    ]) {
      expect(plist).toContain(s)
    }
    expect(isOurPlist(plist)).toBe(true)
    expect(isOurPlist(plist.replace(BUNDLE_ID, "com.example.other"))).toBe(false)
  })

  test("the script starts the service through the launcher, in the environment the service was installed with", () => {
    const s = macAppScript("/Users/ann/.agentd/bin/agentd", { PATH: "/opt/homebrew/bin:/usr/bin:/bin", AGENTD_SERVICE_LABEL: "com.x" })
    expect(s.startsWith("#!/bin/sh\n")).toBe(true)
    expect(s).toContain("export PATH='/opt/homebrew/bin:/usr/bin:/bin'")
    expect(s).toContain("export AGENTD_SERVICE_LABEL='com.x'")
    expect(s).toContain("L='/Users/ann/.agentd/bin/agentd'")
    expect(s).toContain('"$L" service status')
    expect(s).toContain('"$L" service start')
    expect(s).toContain("display notification")
    expect(s).toContain("display alert")
    expect(s).toContain("giving up after 60")
  })

  test("a quote in a path cannot end the shell string", () => {
    expect(macAppScript("/Users/o'neil/.agentd/bin/agentd", { PATH: "/bin" })).toContain("L='/Users/o'\\''neil/.agentd/bin/agentd'")
    expect(macAppScript("/l", { PATH: "/bin", "BAD-NAME": "x" })).not.toContain("BAD-NAME")
  })
})

describe("entryLauncher", () => {
  test("reads back the launcher a script or a desktop entry starts, quotes and all", () => {
    for (const l of ["/Users/ann/.agentd/bin/agentd", "/Users/o'neil/.agentd/bin/agentd", '/home/a"b/$x/agentd']) {
      expect(entryLauncher(macAppScript(l, { PATH: "/bin" }))).toBe(l)
      expect(entryLauncher(desktopEntry(l, { PATH: "/bin" }, "/i.png"))).toBe(l)
    }
    expect(entryLauncher("#!/bin/sh\necho hi\n")).toBeUndefined()
  })
})

describe("the Linux desktop entry", () => {
  test("starts the service in the installed environment, shows the icon, needs no terminal", () => {
    const d = desktopEntry("/home/ann/.agentd/bin/agentd", { PATH: "/usr/local/bin:/usr/bin" }, "/home/ann/.agentd/daemon/current/app/agentd.png")
    expect(d).toContain("[Desktop Entry]\nType=Application\nName=agentd\n")
    expect(d).toContain('Exec=env "PATH=/usr/local/bin:/usr/bin" "/home/ann/.agentd/bin/agentd" service start')
    expect(d).toContain("Icon=/home/ann/.agentd/daemon/current/app/agentd.png")
    expect(d).toContain("Terminal=false")
    expect(isOurDesktopEntry(d)).toBe(true)
    expect(isOurDesktopEntry("[Desktop Entry]\nName=agentd\n")).toBe(false)
  })

  test("characters the Exec key reserves are escaped inside its quotes", () => {
    expect(desktopEntry('/home/a"b/$x/agentd', { PATH: "/bin" }, "/i.png")).toContain('"/home/a\\"b/\\$x/agentd" service start')
  })
})
