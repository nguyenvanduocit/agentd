import { describe, expect, test } from "bun:test"
import { Cooldown, corsHeaders, gatewayPort, hostAllowed, Lockout, originOf, parseConnect } from "../../src/core/local.ts"

describe("hostAllowed (DNS rebinding)", () => {
  test("only the loopback names with the gateway's own port", () => {
    expect(hostAllowed("127.0.0.1:47615", 47615)).toBe(true)
    expect(hostAllowed("localhost:47615", 47615)).toBe(true)
    expect(hostAllowed("LOCALHOST:47615", 47615)).toBe(true)
    expect(hostAllowed("evil.example", 47615)).toBe(false)
    expect(hostAllowed("evil.example:47615", 47615)).toBe(false)
    expect(hostAllowed("127.0.0.1:1", 47615)).toBe(false)
    expect(hostAllowed("127.0.0.1", 47615)).toBe(false)
    expect(hostAllowed(undefined, 47615)).toBe(false)
  })
})

describe("originOf", () => {
  test("accepts https origins and http only on a loopback host", () => {
    expect(originOf("https://mind.aiocean.io")).toBe("https://mind.aiocean.io")
    expect(originOf("http://localhost:5173")).toBe("http://localhost:5173")
    expect(originOf("http://127.0.0.1:3000")).toBe("http://127.0.0.1:3000")
  })

  test("rejects a missing or null origin, a path, another scheme and plain http on the internet", () => {
    for (const bad of [undefined, "", "null", "https://a.b/x", "https://a.b/", "http://evil.example", "ftp://a.b", "chrome-extension://abc", "nonsense"]) {
      expect(originOf(bad)).toBeUndefined()
    }
  })
})

describe("corsHeaders", () => {
  test("reflects the origin and varies on it", () => {
    const h = corsHeaders("https://a.b", {})
    expect(h["Access-Control-Allow-Origin"]).toBe("https://a.b")
    expect(h.Vary).toBe("Origin")
    expect(h["Access-Control-Allow-Private-Network"]).toBeUndefined()
  })

  test("answers a Private Network Access preflight", () => {
    const h = corsHeaders("https://a.b", { "access-control-request-private-network": "true" })
    expect(h["Access-Control-Allow-Private-Network"]).toBe("true")
  })
})

describe("parseConnect", () => {
  test("server mode needs a code and names no server: the app is the page's own origin", () => {
    expect(parseConnect({ mode: "server", code: "ABCD" })).toEqual({ mode: "server", code: "ABCD" })
    expect(parseConnect({ mode: "server" })).toBeUndefined()
    expect(parseConnect({ mode: "server", code: "" })).toBeUndefined()
    expect(parseConnect({ mode: "server", code: 5 })).toBeUndefined()
  })

  test("a request that names a server is refused: a page cannot connect the daemon to an origin that is not its own", () => {
    expect(parseConnect({ mode: "server", code: "ABCD", server: "https://api.x" })).toBeUndefined()
    expect(parseConnect({ mode: "server", code: "ABCD", server: undefined })).toEqual({ mode: "server", code: "ABCD" })
  })

  test("browser mode takes nothing else; anything else is malformed", () => {
    expect(parseConnect({ mode: "browser" })).toEqual({ mode: "browser" })
    expect(parseConnect({ mode: "root" })).toBeUndefined()
    expect(parseConnect(null)).toBeUndefined()
    expect(parseConnect("x")).toBeUndefined()
  })
})

describe("Cooldown", () => {
  test("blocks an origin for the period after block(), and only that origin", () => {
    let now = 1000
    const c = new Cooldown(30_000, () => now)
    expect(c.blocked("https://a.b")).toBe(false)
    c.block("https://a.b")
    expect(c.blocked("https://a.b")).toBe(true)
    expect(c.blocked("https://c.d")).toBe(false)
    now += 29_999
    expect(c.blocked("https://a.b")).toBe(true)
    now += 1
    expect(c.blocked("https://a.b")).toBe(false)
  })
})

describe("gatewayPort", () => {
  test("defaults to 47615; AGENTD_LOCAL_PORT replaces it, 0 meaning any free port", () => {
    expect(gatewayPort({})).toBe(47615)
    expect(gatewayPort({ AGENTD_LOCAL_PORT: "" })).toBe(47615)
    expect(gatewayPort({ AGENTD_LOCAL_PORT: "0" })).toBe(0)
    expect(gatewayPort({ AGENTD_LOCAL_PORT: "53100" })).toBe(53100)
  })

  test("a value that is not a port, or leaves no room for the 10-port range, is an error", () => {
    for (const bad of ["abc", "-1", "70000", "65530", "1.5"]) {
      expect(() => gatewayPort({ AGENTD_LOCAL_PORT: bad })).toThrow("AGENTD_LOCAL_PORT")
    }
  })
})

describe("Lockout", () => {
  test("three refusals within ten minutes, from any origins, lock every dialog; an Allow clears them", () => {
    let now = 0
    const l = new Lockout(3, 10 * 60_000, () => now)
    l.strike()
    l.strike()
    expect(l.locked()).toBe(false)
    l.strike()
    expect(l.locked()).toBe(true)
    l.reset()
    expect(l.locked()).toBe(false)
    now = 1
    l.strike()
    l.strike()
    l.strike()
    expect(l.locked()).toBe(true)
    now += 10 * 60_000
    expect(l.locked()).toBe(false)
  })

  test("old refusals do not count", () => {
    let now = 0
    const l = new Lockout(3, 10 * 60_000, () => now)
    l.strike()
    l.strike()
    now = 10 * 60_000 + 1
    l.strike()
    expect(l.locked()).toBe(false)
  })
})
