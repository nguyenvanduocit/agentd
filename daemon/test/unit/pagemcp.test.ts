import { describe, expect, test } from "bun:test"
import { bridgeAnswer, bridgeHello, MCP_SOCKET_ENV, MCP_TOKEN_ENV, pageMcpServer, parseBridgeAnswer, parseBridgeHello } from "../../src/core/pagemcp.ts"

describe("pageMcpServer", () => {
  test("is the pages' agentd-mcp by its absolute path with no arguments: the socket and the token go in env, out of the process list", () => {
    expect(pageMcpServer("/tmp/agentd-abc123/bin/page/agentd-mcp", "/tmp/agentd-abc123/mcp.sock", "tok")).toEqual({
      command: "/tmp/agentd-abc123/bin/page/agentd-mcp",
      args: [],
      env: [
        { name: MCP_SOCKET_ENV, value: "/tmp/agentd-abc123/mcp.sock" },
        { name: MCP_TOKEN_ENV, value: "tok" },
      ],
    })
  })
})

describe("the bridge's handshake", () => {
  test("the hello carries the token, and only a non-empty string is one", () => {
    expect(parseBridgeHello(bridgeHello("tok"))).toBe("tok")
    for (const bad of ['{"token":""}', '{"token":5}', "{}", "null", "tok", ""]) expect(parseBridgeHello(bad)).toBeUndefined()
  })

  test("the answer is ok or a refusal with its reason; anything else is a refusal that says so", () => {
    expect(parseBridgeAnswer(bridgeAnswer(undefined))).toEqual({ ok: true })
    expect(parseBridgeAnswer(bridgeAnswer("no session has this token"))).toEqual({ ok: false, message: "no session has this token" })
    expect(parseBridgeAnswer('{"jsonrpc":"2.0"}')).toEqual({ ok: false, message: 'the daemon answered something else: {"jsonrpc":"2.0"}' })
    expect(parseBridgeAnswer("oops")).toEqual({ ok: false, message: "the daemon answered something else: oops" })
  })
})
