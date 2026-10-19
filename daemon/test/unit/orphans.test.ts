import { describe, expect, test } from "bun:test"
import { parsePs, parseState, sweep, type PsRow, type State } from "../../src/core/orphans.ts"

const row = (pid: number, pgid: number, lstart: string): PsRow => ({ pid, pgid, lstart })
const state = (groups: State["groups"], daemon = { pid: 100, lstart: "D" }): State => ({ daemon, bin: "/t/bin", groups })

describe("parsePs", () => {
  test("pid, pgid and the five-word start time", () => {
    const out = "    1     1 Sat Oct 11 17:16:30 2026\n  812   812 Sat Oct 11 17:20:01 2026\nnot a row\n\n"
    expect(parsePs(out)).toEqual([row(1, 1, "Sat Oct 11 17:16:30 2026"), row(812, 812, "Sat Oct 11 17:20:01 2026")])
  })
})

describe("parseState", () => {
  test("a valid record", () => {
    const text = JSON.stringify(state([{ pgid: 5, lstart: "x" }]))
    expect(parseState(text)).toEqual(state([{ pgid: 5, lstart: "x" }]))
  })

  test("damage is no state", () => {
    expect(parseState("not json")).toBeUndefined()
    expect(parseState("{}")).toBeUndefined()
    expect(parseState('{"daemon":{"pid":1,"lstart":""},"groups":"x"}')).toBeUndefined()
  })

  test("a group that cannot be a real process group is dropped (0, 1, negative, text)", () => {
    const text = JSON.stringify({ daemon: { pid: 9, lstart: "" }, groups: [{ pgid: 0, lstart: "" }, { pgid: 1, lstart: "" }, { pgid: -7, lstart: "" }, { pgid: "5", lstart: "" }, { pgid: 6, lstart: "" }] })
    expect(parseState(text)?.groups).toEqual([{ pgid: 6, lstart: "" }])
    expect(parseState(text)?.bin).toBe("")
  })
})

describe("sweep", () => {
  test("a group whose leader is gone but has members is an orphan", () => {
    const r = sweep(state([{ pgid: 500, lstart: "L" }]), [row(501, 500, "a"), row(502, 500, "b")], 200)
    expect(r).toEqual({ predecessorAlive: false, kill: [{ pgid: 500, members: 2 }] })
  })

  test("a leader with the recorded start time is ours, with another start time it is a recycled pid", () => {
    expect(sweep(state([{ pgid: 500, lstart: "L" }]), [row(500, 500, "L")], 200).kill).toHaveLength(1)
    expect(sweep(state([{ pgid: 500, lstart: "L" }]), [row(500, 500, "OTHER")], 200).kill).toHaveLength(0)
  })

  test("a start time that was never recorded is left alone while the leader lives", () => {
    expect(sweep(state([{ pgid: 500, lstart: "" }]), [row(500, 500, "L")], 200).kill).toHaveLength(0)
    expect(sweep(state([{ pgid: 500, lstart: "" }]), [row(501, 500, "L")], 200).kill).toHaveLength(1)
  })

  test("a group with no processes needs nothing", () => {
    expect(sweep(state([{ pgid: 500, lstart: "L" }]), [row(1, 1, "x")], 200).kill).toEqual([])
  })

  test("a predecessor that is still running owns its groups", () => {
    const r = sweep(state([{ pgid: 500, lstart: "L" }]), [row(100, 100, "D"), row(501, 500, "x")], 200)
    expect(r).toEqual({ predecessorAlive: true, kill: [] })
  })

  test("a recycled pid of the predecessor is not the predecessor", () => {
    const r = sweep(state([{ pgid: 500, lstart: "L" }]), [row(100, 100, "SOMEONE ELSE"), row(501, 500, "x")], 200)
    expect(r.predecessorAlive).toBe(false)
    expect(r.kill).toHaveLength(1)
  })

  test("a state written by this very process is never a live predecessor", () => {
    expect(sweep(state([], { pid: 200, lstart: "D" }), [row(200, 200, "D")], 200).predecessorAlive).toBe(false)
  })

  test("an unknown start time of the predecessor counts as alive while its pid exists", () => {
    expect(sweep(state([], { pid: 100, lstart: "" }), [row(100, 100, "anything")], 200).predecessorAlive).toBe(true)
  })
})
