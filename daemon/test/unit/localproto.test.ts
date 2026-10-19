import { describe, expect, test } from "bun:test"
import { parseToDaemon } from "../../src/core/localproto.ts"

describe("parseToDaemon", () => {
  test("reads open, write and close", () => {
    expect(parseToDaemon('{"t":"open","id":"s1","agent":"claude"}')).toEqual({ t: "open", id: "s1", agent: "claude" })
    expect(parseToDaemon('{"t":"write","id":"s1","line":"{\\"a\\":1}"}')).toEqual({ t: "write", id: "s1", line: '{"a":1}' })
    expect(parseToDaemon('{"t":"close","id":"s1"}')).toEqual({ t: "close", id: "s1" })
  })

  test("reads mcp: a line of the page's MCP server, for the session's bridge", () => {
    expect(parseToDaemon('{"t":"mcp","id":"s1","line":"{\\"id\\":1}"}')).toEqual({ t: "mcp", id: "s1", line: '{"id":1}' })
  })

  test("anything else is undefined: unknown kind, missing or empty id, wrong types, not JSON", () => {
    const mcp = ['{"t":"mcp","id":"s1"}', '{"t":"mcp","id":"s1","line":{}}', '{"t":"mcp","line":"x"}']
    for (const bad of ['{"t":"exec","id":"s1"}', '{"t":"open","agent":"claude"}', '{"t":"open","id":"","agent":"claude"}', '{"t":"open","id":"s1"}', '{"t":"write","id":"s1","line":5}', '{"t":"close","id":7}', ...mcp, "[]", "null", "{oops", ""]) {
      expect(parseToDaemon(bad)).toBeUndefined()
    }
  })
})
