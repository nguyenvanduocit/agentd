import { describe, expect, test } from "bun:test"
import { addApp, addBrowser, emptyRegistry, hasApp, hasBrowser, parseRegistry, removeOrigin, selectApp, selectApps, withdrawn } from "../../src/core/registry.ts"

const a = { server: "https://a.example", token: "ta" }
const b = { server: "https://b.example", token: "tb" }

describe("parseRegistry", () => {
  test("reads apps and browser grants", () => {
    const text = JSON.stringify({ apps: [a, b], browsers: [{ origin: "https://c.example", grantedAt: "2026-10-19T00:00:00.000Z" }] })
    expect(parseRegistry(text)).toEqual({ apps: [a, b], browsers: [{ origin: "https://c.example", grantedAt: "2026-10-19T00:00:00.000Z" }] })
  })

  test("a missing browsers list is empty", () => {
    expect(parseRegistry(JSON.stringify({ apps: [a] }))).toEqual({ apps: [a], browsers: [] })
  })

  test("broken JSON, an empty token and a missing server are named", () => {
    expect(() => parseRegistry("{oops")).toThrow("not valid JSON")
    expect(() => parseRegistry('{"server":"https://s","token":"t"}')).toThrow("has no apps list")
    expect(() => parseRegistry(JSON.stringify({ apps: [{ server: "https://s", token: "" }] }))).toThrow("has no apps list")
    expect(() => parseRegistry(JSON.stringify({ apps: [{ server: "https://s" }] }))).toThrow("has no apps list")
  })
})

describe("registry edits", () => {
  test("addApp twice for one server keeps one entry with the later token", () => {
    const r = addApp(addApp(emptyRegistry(), a), { server: a.server, token: "new" })
    expect(r.apps).toEqual([{ server: a.server, token: "new" }])
  })

  test("addApp keeps the order of first pairing", () => {
    expect(addApp(addApp(emptyRegistry(), a), b).apps).toEqual([a, b])
  })

  test("removeOrigin drops the app and the browser grant of that origin only", () => {
    let r = addBrowser(addApp(addApp(emptyRegistry(), a), b), a.server, "t0")
    r = addBrowser(r, "https://other.example", "t1")
    r = removeOrigin(r, a.server)
    expect(r.apps).toEqual([b])
    expect(r.browsers).toEqual([{ origin: "https://other.example", grantedAt: "t1" }])
  })

  test("hasApp and hasBrowser answer by origin", () => {
    const r = addBrowser(addApp(emptyRegistry(), a), "https://c.example", "t")
    expect(hasApp(r, a.server)).toBe(true)
    expect(hasApp(r, b.server)).toBe(false)
    expect(hasBrowser(r, "https://c.example")).toBe(true)
    expect(hasBrowser(r, a.server)).toBe(false)
  })

  test("withdrawn is what runs for an origin the registry no longer has: linked apps and page sockets", () => {
    const r = addBrowser(addApp(emptyRegistry(), a), "https://c.example", "t")
    expect(withdrawn(r, [a.server, b.server], ["https://c.example", "https://d.example"])).toEqual({ apps: [b.server], pages: ["https://d.example"] })
    expect(withdrawn(r, [a.server], ["https://c.example"])).toEqual({ apps: [], pages: [] })
  })

  test("a repeated browser grant is stored once", () => {
    const r = addBrowser(addBrowser(emptyRegistry(), "https://c.example", "t0"), "https://c.example", "t1")
    expect(r.browsers).toEqual([{ origin: "https://c.example", grantedAt: "t0" }])
  })
})

describe("selection", () => {
  const two = addApp(addApp(emptyRegistry(), a), b)

  test("selectApps without a server is every app, with one it is that app", () => {
    expect(selectApps(two, undefined)).toEqual([a, b])
    expect(selectApps(two, "https://b.example/")).toEqual([b])
  })

  test("selectApps for a server that is not saved says so", () => {
    expect(() => selectApps(two, "https://x.example")).toThrow("not paired with https://x.example")
  })

  test("selectApp takes the only app, or the one named", () => {
    expect(selectApp(addApp(emptyRegistry(), a), undefined)).toEqual(a)
    expect(selectApp(two, b.server)).toEqual(b)
  })

  test("selectApp with several apps and no server asks for --app; with none says not paired", () => {
    expect(() => selectApp(two, undefined)).toThrow("--app")
    expect(() => selectApp(emptyRegistry(), undefined)).toThrow("not paired")
  })
})
