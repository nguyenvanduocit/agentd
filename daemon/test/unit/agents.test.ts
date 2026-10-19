import { describe, expect, test } from "bun:test"
import { ADAPTERS } from "../../src/core/adapters.ts"
import { AGENT_NAMES, agentCommand, agentStatus, allAgentStatuses, isAgentName, openRefusal, seamVariable, splitCommand } from "../../src/core/agents.ts"

const noPrograms = () => undefined
const everything = (p: string) => `/bin/${p}`

const noAdapter = () => undefined
const ready = (name: string) => () => ({ kind: "ready" as const, bin: `/mc/adapters/${name}/1/node_modules/x/dist/index.js` })

describe("agent table", () => {
  test("names the three agents", () => {
    expect(AGENT_NAMES).toEqual(["claude", "codex", "grok"])
    expect(isAgentName("claude")).toBe(true)
    expect(isAgentName("rm")).toBe(false)
    expect(isAgentName("toString")).toBe(false)
  })

  test("claude and codex run `node <installed adapter bin>`, grok runs from PATH", () => {
    expect(agentCommand("claude", {}, ready("claude"), "/usr/bin/node")).toEqual({
      command: "/usr/bin/node",
      args: ["/mc/adapters/claude/1/node_modules/x/dist/index.js"],
    })
    expect(agentCommand("grok", {}, noAdapter, "/usr/bin/node")).toEqual({ command: "grok", args: ["agent", "--always-approve", "stdio"] })
  })

  test("no adapter command exists before the adapter is installed; npx is never the command", () => {
    expect(() => agentCommand("codex", {}, noAdapter, "node")).toThrow("the codex adapter is not installed")
    expect(() => agentCommand("codex", {}, () => ({ kind: "installing" }), "node")).toThrow("not installed")
  })

  test("a call returns its own copy of grok's arguments", () => {
    agentCommand("grok", {}, noAdapter, "node").args.push("x")
    expect(agentCommand("grok", {}, noAdapter, "node").args).toHaveLength(3)
  })
})

describe("the pinned versions follow agent/package.json", () => {
  test("claude-agent-acp and codex-acp", async () => {
    const pkg = (await Bun.file(new URL("../../../agent/package.json", import.meta.url)).json()) as { dependencies: Record<string, string> }
    expect(pkg.dependencies[ADAPTERS.claude.pkg]).toBe(ADAPTERS.claude.version)
    expect(pkg.dependencies[ADAPTERS.codex.pkg]).toBe(ADAPTERS.codex.version)
  })
})

describe("readiness", () => {
  test("claude and codex are ready when installed, not ready with the install's reason otherwise", () => {
    expect(agentStatus("claude", {}, noPrograms, ready("claude"))).toEqual({ name: "claude", ready: true, reason: "" })
    expect(agentStatus("codex", {}, everything, noAdapter)).toEqual({
      name: "codex",
      ready: false,
      reason: "installing the Codex adapter (first run only, this can take several minutes)",
    })
    expect(agentStatus("codex", {}, everything, () => ({ kind: "failed", reason: "cannot install the Codex adapter: offline; retrying in 60 s" }))).toEqual({
      name: "codex",
      ready: false,
      reason: "cannot install the Codex adapter: offline; retrying in 60 s",
    })
  })

  test("grok needs grok", () => {
    expect(agentStatus("grok", {}, noPrograms, noAdapter)).toEqual({ name: "grok", ready: false, reason: "grok not found in PATH" })
    expect(agentStatus("grok", {}, (p) => (p === "grok" ? "/x/grok" : undefined), noAdapter).ready).toBe(true)
  })

  test("the seam makes an agent ready whatever the machine has, and installs nothing", () => {
    const env = { [seamVariable("claude")]: "/path/fake-agent" }
    expect(agentStatus("claude", env, noPrograms, noAdapter).ready).toBe(true)
    expect(allAgentStatuses(env, noPrograms, noAdapter).map((s) => s.ready)).toEqual([true, false, false])
  })

  test("an empty seam variable counts as unset", () => {
    expect(agentStatus("claude", { AGENTD_AGENT_CLAUDE: "  " }, noPrograms, noAdapter).ready).toBe(false)
    expect(() => agentCommand("claude", { AGENTD_AGENT_CLAUDE: "" }, noAdapter, "node")).toThrow("not installed")
  })
})

describe("openRefusal", () => {
  test("an adapter that is still installing says so, and nothing else", () => {
    expect(openRefusal("codex", {}, everything, noAdapter)).toBe("the codex adapter is still installing on this machine")
    expect(openRefusal("codex", {}, everything, () => ({ kind: "installing" }))).toBe("the codex adapter is still installing on this machine")
  })

  test("a failed install gives the reason; a ready agent gives none", () => {
    expect(openRefusal("claude", {}, everything, () => ({ kind: "failed", reason: "offline" }))).toBe("agent claude is not ready: offline")
    expect(openRefusal("claude", {}, everything, ready("claude"))).toBeUndefined()
    expect(openRefusal("grok", {}, noPrograms, noAdapter)).toBe("agent grok is not ready: grok not found in PATH")
  })

  test("the seam is never refused", () => {
    expect(openRefusal("codex", { AGENTD_AGENT_CODEX: "true" }, noPrograms, noAdapter)).toBeUndefined()
  })
})

describe("the test seam", () => {
  test("replaces the whole command line", () => {
    expect(agentCommand("claude", { AGENTD_AGENT_CLAUDE: "/path/fake-agent --flag" }, noAdapter, "node")).toEqual({
      command: "/path/fake-agent",
      args: ["--flag"],
    })
  })

  test("variable names are per agent, upper case", () => {
    expect(seamVariable("grok")).toBe("AGENTD_AGENT_GROK")
  })

  test("an empty quoted command stays an empty word: open then reports it as not found", () => {
    expect(agentCommand("claude", { AGENTD_AGENT_CLAUDE: "''" }, noAdapter, "node").command).toBe("")
  })
})

describe("splitCommand", () => {
  test.each([
    ["a b  c", ["a", "b", "c"]],
    ["  lead and trail  ", ["lead", "and", "trail"]],
    ["sh -c 'echo a b; sleep 1'", ["sh", "-c", "echo a b; sleep 1"]],
    [`node "/My Dir/index.js" --x`, ["node", "/My Dir/index.js", "--x"]],
    [`--flag="a b"`, ["--flag=a b"]],
    [`say "it's" 'a "b"'`, ["say", "it's", 'a "b"']],
    ["''", [""]],
    ["", []],
  ])("%j", (line, words) => {
    expect(splitCommand(line)).toEqual(words)
  })

  test.each([`echo "open`, `echo 'open`, `a "b' c`])("%j has an unterminated quote", (line) => {
    expect(() => splitCommand(line)).toThrow("unterminated quote")
  })
})
