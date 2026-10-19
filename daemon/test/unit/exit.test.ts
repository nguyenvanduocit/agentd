import { describe, expect, test } from "bun:test"
import { classifyExit, controlOutcome, EXIT_RESTART, EXIT_STOPPED, isManaged } from "../../src/core/exit.ts"

describe("exit codes are the protocol with the service", () => {
  test("0 stays stopped, 75 restarts now, anything else is a crash", () => {
    expect(EXIT_STOPPED).toBe(0)
    expect(EXIT_RESTART).toBe(75)
    expect(classifyExit(0)).toBe("stopped")
    expect(classifyExit(75)).toBe("restart")
    for (const crash of [1, 2, 74, 76, 127, 137, 143, -1]) expect(classifyExit(crash)).toBe("crash")
  })
})

describe("controlOutcome", () => {
  test("Stop always exits 0", () => {
    expect(controlOutcome("stop", true)).toEqual({ kind: "exit", code: 0 })
    expect(controlOutcome("stop", false)).toEqual({ kind: "exit", code: 0 })
  })

  test("Restart exits 75 when a service restarts the daemon, and is ignored when none does", () => {
    expect(controlOutcome("restart", true)).toEqual({ kind: "exit", code: 75 })
    expect(controlOutcome("restart", false)).toEqual({ kind: "ignore", why: "not managed: restart ignored" })
  })
})

test("isManaged: only AGENTD_MANAGED=1", () => {
  expect(isManaged({ AGENTD_MANAGED: "1" })).toBe(true)
  expect(isManaged({ AGENTD_MANAGED: "0" })).toBe(false)
  expect(isManaged({})).toBe(false)
})
