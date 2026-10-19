import { afterEach, describe, expect, test } from "bun:test"
import type { Offer } from "../../src/core/update.ts"
import { ReleaseWatcher } from "../../src/releases.ts"
import { waitFor } from "./harness.ts"

const OFFER: Offer = { version: "2.0.0", url: "/download/v2.0.0/agentd-2.0.0.tgz", sha256: "a".repeat(64), signature: Buffer.alloc(64, 7).toString("base64") }

const stops: (() => void)[] = []
afterEach(() => {
  for (const s of stops.splice(0)) s()
})

function release(handle: (n: number) => Response | Promise<Response>) {
  let hits = 0
  const s = Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: (req) => (new URL(req.url).pathname === "/latest/download/latest.json" ? handle(++hits) : new Response("nope", { status: 404 })) })
  stops.push(() => void s.stop(true))
  return { base: `http://127.0.0.1:${s.port}`, hits: () => hits }
}

function watch(base: string, intervalMs = 60_000, timeoutMs = 2000) {
  const offers: Offer[] = []
  const w = new ReleaseWatcher({ base, intervalMs, timeoutMs, onOffer: (o) => offers.push(o) })
  stops.push(() => w.stop())
  w.start()
  return { w, offers }
}

describe("ReleaseWatcher", () => {
  test("asks at once, and hands the offer over", async () => {
    const r = release(() => Response.json(OFFER))
    const { offers } = watch(r.base)
    await waitFor("the first offer", () => offers[0])
    expect(offers).toEqual([OFFER])
  })

  test("asks again every interval", async () => {
    const r = release(() => Response.json(OFFER))
    const { offers } = watch(r.base, 100)
    await waitFor("three offers", () => offers.length >= 3)
    expect(r.hits()).toBeGreaterThanOrEqual(3)
  })

  test("a server that is down, a refusal, a redirect, a body that is not an offer and a hang each change nothing and are asked again", async () => {
    let mode = 0
    const r = release((n) => {
      mode = n
      if (n === 1) return new Response("down", { status: 503 })
      if (n === 2) return new Response(null, { status: 302, headers: { location: "http://127.0.0.1:1/x" } })
      if (n === 3) return new Response("not json")
      if (n === 4) return new Promise<Response>(() => undefined)
      return Response.json(OFFER)
    })
    const { offers } = watch(r.base, 80, 150)
    await waitFor("the offer after five answers", () => offers[0], 10_000)
    expect(mode).toBeGreaterThanOrEqual(5)
    expect(offers[0]).toEqual(OFFER)
  })

  test("a redirect inside the server's own origin is followed (GitHub sends a release asset on to another host)", async () => {
    let hits = 0
    const s = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch(req) {
        const path = new URL(req.url).pathname
        if (path === "/latest/download/latest.json") return new Response(null, { status: 302, headers: { location: "/download/v2.0.0/latest.json" } })
        if (path === "/download/v2.0.0/latest.json") return (hits++, Response.json(OFFER))
        return new Response("nope", { status: 404 })
      },
    })
    stops.push(() => void s.stop(true))
    const { offers } = watch(`http://127.0.0.1:${s.port}`)
    await waitFor("the offer behind the redirect", () => offers[0])
    expect(offers[0]).toEqual(OFFER)
    expect(hits).toBe(1)
  })

  test("a redirect loop ends: it is not followed forever", async () => {
    let hits = 0
    const s = Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: () => (hits++, new Response(null, { status: 302, headers: { location: "/latest/download/latest.json" } })) })
    stops.push(() => void s.stop(true))
    const { offers } = watch(`http://127.0.0.1:${s.port}`, 60_000, 2000)
    await waitFor("the hops to stop", () => hits >= 6, 5000)
    await new Promise((res) => setTimeout(res, 300))
    expect(hits).toBeLessThan(12)
    expect(offers).toEqual([])
  })

  test("stop ends the asking", async () => {
    const r = release(() => Response.json(OFFER))
    const { w, offers } = watch(r.base, 50)
    await waitFor("an offer", () => offers[0])
    w.stop()
    await new Promise((res) => setTimeout(res, 120)) // a request already on its way when stop() was called still lands
    const seen = r.hits()
    await new Promise((res) => setTimeout(res, 300))
    expect(r.hits()).toBe(seen)
  })
})
