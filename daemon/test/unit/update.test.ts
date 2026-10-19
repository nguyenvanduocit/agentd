import { describe, expect, test } from "bun:test"
import { decide, latestUrl, parseLatest, parseVersionRecord, releaseBase, tarballPath, type Offer, type UpdateState } from "../../src/core/update.ts"

const SHA = "a".repeat(64)
const SIG = Buffer.alloc(64, 7).toString("base64")
const offer = (version: string, o: Partial<Offer> = {}): Offer => ({ version, url: tarballPath(version), sha256: SHA, signature: SIG, ...o })
const state = (o: Partial<UpdateState> = {}): UpdateState => ({ running: "1.0.0", staged: undefined, rejected: undefined, ...o })

describe("decide", () => {
  test("an empty version: the server has nothing to update to", () => {
    expect(decide(state(), { version: "", url: "", sha256: "", signature: "" })).toEqual({ kind: "none" })
  })

  test("the running version needs nothing, whatever url and sha the server sends with it", () => {
    expect(decide(state(), { version: "1.0.0", url: "", sha256: "", signature: "" })).toEqual({ kind: "none" })
  })

  test("a different version is an update", () => {
    expect(decide(state(), offer("1.1.0"))).toEqual({ kind: "update", offer: offer("1.1.0") })
  })

  test("an older release than the running one is refused, a replayed signed manifest included", () => {
    expect(decide(state({ running: "1.2.0" }), offer("1.1.0")).kind).toBe("invalid")
    expect(decide(state({ running: "1.10.0" }), offer("1.9.0")).kind).toBe("invalid")
    expect(decide(state({ running: "1.2.0" }), offer("1.10.0")).kind).toBe("update")
  })

  test("versions that are not x.y.z (a dev build) are not ordered, so any signed offer may replace them", () => {
    expect(decide(state({ running: "dev" }), offer("1.0.0")).kind).toBe("update")
    expect(decide(state({ running: "1.2.0" }), offer("1.1.0-rc1")).kind).toBe("update")
  })

  test("a staged version waits; going back to the running version unstages", () => {
    expect(decide(state({ staged: "1.1.0" }), offer("1.1.0"))).toEqual({ kind: "waiting" })
    expect(decide(state({ staged: "1.1.0" }), offer("1.0.0"))).toEqual({ kind: "unstage" })
    expect(decide(state({ staged: "1.1.0" }), offer("1.2.0")).kind).toBe("update")
  })

  test("a rejected version is not tried again; another one is", () => {
    expect(decide(state({ rejected: "1.1.0" }), offer("1.1.0"))).toEqual({ kind: "rejected", version: "1.1.0" })
    expect(decide(state({ rejected: "1.1.0" }), offer("1.2.0")).kind).toBe("update")
  })

  test("a reply that could reach outside the daemon directory or another host is invalid", () => {
    for (const bad of [
      offer("../evil"),
      offer("1.1.0/../../x"),
      offer(".hidden"),
      offer("1.1.0", { url: "https://evil.example/releases/agentd-1.1.0.tgz" }),
      offer("1.1.0", { url: "/releases/agentd-1.2.0.tgz" }),
      offer("1.1.0", { url: `${tarballPath("1.1.0")}?x=1` }),
      offer("1.1.0", { sha256: "A".repeat(64) }),
      offer("1.1.0", { sha256: "abc" }),
    ]) {
      expect(decide(state(), bad).kind).toBe("invalid")
    }
  })

  test("an offer without a signature of the right shape is invalid", () => {
    const unsigned = { version: "1.1.0", url: tarballPath("1.1.0"), sha256: SHA } as Offer
    for (const bad of [
      offer("1.1.0", { signature: "" }),
      offer("1.1.0", { signature: SIG.slice(0, -2) }),
      offer("1.1.0", { signature: Buffer.alloc(63).toString("base64") }),
      offer("1.1.0", { signature: `${SIG}\n` }),
      unsigned, // a server older than signing sends no signature field
    ]) {
      expect(decide(state(), bad).kind).toBe("invalid")
    }
  })

  test("the names of the daemon directory's own entries are not versions, in any case", () => {
    for (const name of ["current", "previous", "rejected", "crashes", "Current", "CRASHES"]) {
      expect(decide(state(), offer(name)).kind).toBe("invalid")
    }
    expect(decide(state(), offer("currently")).kind).toBe("update")
    expect(decide(state(), offer("0.0.0-gcurrent")).kind).toBe("update")
  })
})

describe("parseVersionRecord", () => {
  test("a version with a newline, or nothing plain, is read as it is or as none", () => {
    expect(parseVersionRecord("1.2.3\n")).toBe("1.2.3")
    expect(parseVersionRecord("")).toBeUndefined()
    expect(parseVersionRecord("../x")).toBeUndefined()
    expect(parseVersionRecord("a b")).toBeUndefined()
    expect(parseVersionRecord("current\n")).toBeUndefined()
  })
})

describe("the release server", () => {
  test("a tarball is under /releases and the offer under /releases/latest.json", () => {
    expect(tarballPath("1.2.3")).toBe("/releases/agentd-1.2.3.tgz")
    expect(latestUrl("https://agentd.example.com")).toBe("https://agentd.example.com/releases/latest.json")
  })

  test("parseLatest reads the four fields of latest.json", () => {
    expect(parseLatest(JSON.stringify(offer("1.2.3")))).toEqual(offer("1.2.3"))
  })

  test("parseLatest refuses what is not an offer: not JSON, a missing or non-string field, an array", () => {
    for (const bad of ["", "oops", "[]", "null", JSON.stringify({ version: "1.0.0" }), JSON.stringify({ ...offer("1.0.0"), sha256: 5 }), JSON.stringify({ ...offer("1.0.0"), signature: undefined })]) {
      expect(parseLatest(bad)).toBeUndefined()
    }
  })

  test("releaseBase: the environment replaces what was baked in at build time, and empty means no updates", () => {
    expect(releaseBase({}, "https://baked.example.com")).toBe("https://baked.example.com")
    expect(releaseBase({ AGENTD_RELEASE_URL: "https://env.example.com/" }, "https://baked.example.com")).toBe("https://env.example.com")
    expect(releaseBase({ AGENTD_RELEASE_URL: "" }, "https://baked.example.com")).toBeUndefined()
    expect(releaseBase({}, undefined)).toBeUndefined()
  })

  test("releaseBase accepts https anywhere and http only on loopback: an update must not travel in the clear", () => {
    expect(releaseBase({ AGENTD_RELEASE_URL: "http://127.0.0.1:7100" }, undefined)).toBe("http://127.0.0.1:7100")
    expect(() => releaseBase({ AGENTD_RELEASE_URL: "http://agentd.example.com" }, undefined)).toThrow("https")
    expect(() => releaseBase({ AGENTD_RELEASE_URL: "nonsense" }, undefined)).toThrow("not a URL")
  })
})
