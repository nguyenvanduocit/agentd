import { expect, spyOn, test } from "bun:test"
import { signalGroup } from "../../src/procgroup.ts"

test.skipIf(process.platform === "win32")("a pid that leads no group of its own is never signalled; a real group is", () => {
  const kill = spyOn(process, "kill").mockImplementation(() => true)
  try {
    for (const pgid of [-1, 0, 1]) signalGroup(pgid, true)
    expect(kill).not.toHaveBeenCalled()
    signalGroup(4242, false)
    expect(kill).toHaveBeenCalledWith(-4242, "SIGTERM")
  } finally {
    kill.mockRestore()
  }
})
