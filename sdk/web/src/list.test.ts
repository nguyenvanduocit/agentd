import { expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import type { DaemonInfo, DaemonList, Pairing } from "./list.ts"
import type { AgentInfo } from "./client.ts"

// The JSON an app's server answers when it returns sdk/goserver's List. These types are written by hand, so this reads
// the Go struct tags and fails when a field is added or renamed on one side only.
const goTypes = readFileSync(new URL("../../goserver/types.go", import.meta.url), "utf8")

/** The JSON names of the fields of the Go struct `name`. */
function jsonNames(name: string): string[] {
  const body = new RegExp(`type ${name} struct \\{([^}]*)\\}`).exec(goTypes)?.[1]
  if (body === undefined) throw new Error(`sdk/goserver/types.go has no struct ${name}`)
  return [...body.matchAll(/`json:"([A-Za-z]+)(?:,omitempty)?"`/g)].map((m) => m[1] ?? "").sort()
}

const agent: Required<AgentInfo> = { name: "claude", title: "Claude Code", ready: true, reason: "" }
const info: Required<DaemonInfo> = {
  id: "d1",
  name: "Work Mac",
  hostname: "work.local",
  state: "online",
  version: "0.7.0",
  os: "darwin",
  agents: [agent],
  lastSeen: 1760000000000,
  sessions: 0,
  managed: true,
  incompatible: "",
}
const pairing: Required<Pairing> = { code: "K7QX-29PD", expiresAt: 1760000600000 }
const list: Required<DaemonList> = { daemons: [info], pairing }

test.each([
  ["List", list],
  ["Info", info],
  ["Agent", agent],
  ["Pairing", pairing],
])("the TypeScript %s has exactly the JSON fields of the Go one", (name, sample) => {
  expect(Object.keys(sample).sort()).toEqual(jsonNames(name))
})

test("a list as the Go server writes it fits the type (an incompatible daemon carries its sentence, others none)", () => {
  const wire = `{"daemons":[{"id":"d1","name":"Work Mac","hostname":"work.local","state":"offline","version":"0.7.0","os":"darwin","agents":[],"lastSeen":0,"sessions":0,"managed":false,"incompatible":"The agentd on this computer is too old for this app."}],"pairing":{"code":"","expiresAt":0}}`
  const parsed = JSON.parse(wire) as DaemonList
  expect(parsed.daemons[0]?.incompatible).toContain("too old")
  expect(parsed.pairing.code).toBe("")
})
