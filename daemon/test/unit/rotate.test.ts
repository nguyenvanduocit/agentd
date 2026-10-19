import { describe, expect, test } from "bun:test"
import { needsRotation, rotateSteps } from "../../src/core/rotate.ts"

describe("rotateSteps", () => {
  test("keeping 3 files: .2 goes, .1 becomes .2, the log is copied to .1 and truncated in place", () => {
    expect(rotateSteps("/l/daemon.log", 3)).toEqual([
      { op: "remove", file: "/l/daemon.log.2" },
      { op: "rename", from: "/l/daemon.log.1", to: "/l/daemon.log.2" },
      { op: "copy", from: "/l/daemon.log", to: "/l/daemon.log.1" },
      { op: "truncate", file: "/l/daemon.log" },
    ])
  })

  test("keeping 4 files shifts every older one", () => {
    expect(rotateSteps("f", 4).map((s) => s.op)).toEqual(["remove", "rename", "rename", "copy", "truncate"])
  })

  test("keeping 1 file only truncates", () => {
    expect(rotateSteps("f", 1)).toEqual([{ op: "truncate", file: "f" }])
  })
})

test("needsRotation: larger than the limit", () => {
  expect(needsRotation(10, 10)).toBe(false)
  expect(needsRotation(11, 10)).toBe(true)
})
