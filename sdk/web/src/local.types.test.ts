import { client, ndJsonStream } from "@agentclientprotocol/sdk"
import { expect, test } from "bun:test"
import type { AgentSession, DisconnectResult, FoundDaemon, KnownReason, Reason } from "./client.ts"
import type { LocalClient, LocalState } from "./local.ts"

// These functions are never called: tsc (bun run typecheck) is the test. A state narrows by its phase, so an interface
// reads what it needs without a check for undefined, and an @ts-expect-error line that stopped being an error fails.
function readStates(s: LocalState): string {
  switch (s.phase) {
    case "connected": {
      const found: FoundDaemon = s.found
      return `connected to agentd ${found.version} on port ${found.port}`
    }
    case "available": {
      const connectable: true = s.canConnect
      return `${connectable} ${s.found.port}`
    }
    case "connecting": {
      const waiting: true = s.connecting
      return `${waiting} ${s.found.port}`
    }
    case "refused": {
      const reason: Reason = s.reason
      const words: string = s.error
      return `${reason}: ${words}`
    }
    case "absent": {
      const nothing: false = s.found.found
      const refusedBy: Reason | undefined = s.found.refused
      return `${nothing} ${refusedBy}`
    }
    case "detecting":
      return "looking"
  }
}

function narrowingIsReal(s: LocalState): void {
  // @ts-expect-error a state that is not connected has no daemon to read the port of
  void s.found.port
  if (s.phase === "absent") {
    // @ts-expect-error no daemon was found, so there is no port
    void s.found.port
  }
  if (s.phase === "available") {
    // @ts-expect-error available is not refused: there is no reason to read
    const reason: Reason = s.reason
    void reason
  }
}

function reasonsAreOpen(r: Reason, known: KnownReason): void {
  const sent: Reason = "a-reason-from-a-newer-daemon" // legal: the daemon may say more than this SDK knows
  const narrowed: Reason = known
  void [r, sent, narrowed]
  // @ts-expect-error the known reasons are a closed list; an unknown string is not one of them
  const closed: KnownReason = "a-reason-from-a-newer-daemon"
  void closed
}

// Clients written before the result of disconnect was typed (Promise<void>) and those that answer a DisconnectResult both fit.
const oldClient: Pick<LocalClient, "disconnect"> = { disconnect: () => Promise.resolve() }
const newClient: Pick<LocalClient, "disconnect"> = { disconnect: (): Promise<DisconnectResult> => Promise.resolve({ disconnected: true }) }
// @ts-expect-error anything else is not an answer to disconnect
const badClient: Pick<LocalClient, "disconnect"> = { disconnect: () => Promise.resolve("whatever") }

// The page's MCP server goes into the official SDK's session/new as it is: no cast, and the answer keeps its type
// (an argument that does not fit makes the SDK's request() fall back to an untyped answer: sessionId would be unknown).
async function acpTakesThePagesMcpServer(session: AgentSession): Promise<string> {
  const acp = client({ name: "page" }).connect(ndJsonStream(session.writable, session.readable))
  const { sessionId } = await acp.agent.request("session/new", { cwd: session.cwd, mcpServers: [session.mcpServer("page")] })
  return sessionId
}

test("the states narrow by phase (checked by tsc, see this file)", () => {
  expect(typeof acpTakesThePagesMcpServer).toBe("function")
  expect(typeof readStates).toBe("function")
  expect(typeof narrowingIsReal).toBe("function")
  expect(typeof reasonsAreOpen).toBe("function")
  expect([oldClient, newClient, badClient]).toHaveLength(3)
})
