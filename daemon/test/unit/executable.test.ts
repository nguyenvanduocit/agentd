import { describe, expect, test } from "bun:test"
import { pathDirs, pathKey, resolveExecutable } from "../../src/core/executable.ts"

const files = (...present: string[]) => (f: string) => present.includes(f)

describe("resolveExecutable", () => {
  test("takes the first directory of PATH that has the file", () => {
    const env = { PATH: "/a:/b:/c" }
    expect(resolveExecutable("npx", env, "linux", files("/b/npx", "/c/npx"))).toBe("/b/npx")
  })

  test("not found", () => {
    expect(resolveExecutable("npx", { PATH: "/a" }, "linux", files())).toBeUndefined()
    expect(resolveExecutable("npx", {}, "linux", files("/npx"))).toBeUndefined()
  })

  test("empty PATH entries are skipped, not read as the current directory", () => {
    expect(pathDirs({ PATH: ":/a::" }, "linux")).toEqual(["/a"])
  })

  test("a name with a directory part is checked as given", () => {
    expect(resolveExecutable("/opt/x/agent", { PATH: "/a" }, "linux", files("/opt/x/agent"))).toBe("/opt/x/agent")
    expect(resolveExecutable("./agent", { PATH: "/a" }, "linux", files("/a/agent"))).toBeUndefined()
  })

  test("Windows: Path in any case, PATHEXT tried in order", () => {
    const env = { Path: "C:\\n;C:\\m", PATHEXT: ".EXE;.CMD" }
    expect(pathKey(env, "win32")).toBe("Path")
    expect(pathKey({}, "win32")).toBe("Path")
    expect(pathKey({ PATH: "x" }, "linux")).toBe("PATH")
    expect(resolveExecutable("npx", env, "win32", files("C:\\m\\npx.CMD", "C:\\n\\npx.CMD"))).toBe("C:\\n\\npx.CMD")
    expect(resolveExecutable("node.exe", env, "win32", files("C:\\m\\node.exe"))).toBe("C:\\m\\node.exe")
    expect(resolveExecutable("C:\\x\\a", env, "win32", files("C:\\x\\a.EXE"))).toBe("C:\\x\\a.EXE")
  })
})
