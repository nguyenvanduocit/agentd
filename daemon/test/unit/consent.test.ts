import { describe, expect, test } from "bun:test"
import { answerOf, consentText, dialogPlan } from "../../src/core/consent.ts"

const ORIGIN = "https://app.example.com"
const text = consentText({ origin: ORIGIN, agents: ["claude", "codex"] })
const has = (...found: string[]) => (cmd: string) => found.includes(cmd)

describe("consentText", () => {
  test("names the origin and what Allow means", () => {
    expect(text.title).toContain(ORIGIN)
    expect(text.body).toContain(ORIGIN)
    expect(text.body).toContain("claude, codex")
    expect(text.body).toMatch(/files/)
    expect(text.body).toMatch(/shell/)
  })

  test("says how to take the connection back", () => {
    expect(text.body).toContain("menu bar")
    expect(text.body).toContain(`agentd apps remove ${ORIGIN}`)
  })

  test("says when no agent is ready", () => {
    expect(consentText({ origin: ORIGIN, agents: [] }).body).toContain("no agent is ready yet")
  })
})

describe("dialogPlan", () => {
  test("macOS: osascript, the text only as arguments after the script", () => {
    const plan = dialogPlan("darwin", has("osascript"), text, 60)
    expect(plan?.file).toBe("osascript")
    const args = plan?.args ?? []
    const scripts = args.filter((_, i) => args[i - 1] === "-e")
    expect(scripts.length).toBeGreaterThan(0)
    for (const s of scripts) expect(s).not.toContain(ORIGIN)
    expect(scripts.join("\n")).toContain("giving up after 60")
    expect(scripts.join("\n")).toContain('default button "Deny"')
    expect(args.slice(-2)).toEqual([text.title, text.body])
  })

  test("Linux: zenity first, then kdialog, else nothing", () => {
    const z = dialogPlan("linux", has("zenity", "kdialog"), text, 60)
    expect(z?.file).toBe("zenity")
    expect(z?.args).toContain("--no-markup")
    expect(z?.args).toContain("--timeout=60")
    expect(dialogPlan("linux", has("kdialog"), text, 60)?.file).toBe("kdialog")
    expect(dialogPlan("linux", has(), text, 60)).toBeUndefined()
    expect(dialogPlan("win32", has("zenity"), text, 60)).toBeUndefined()
  })
})

describe("answerOf", () => {
  test("osascript: Allow, a give-up and everything else", () => {
    expect(answerOf("osascript", 0, "Allow,false\n")).toBe("allow")
    expect(answerOf("osascript", 0, "Deny,false\n")).toBe("deny")
    expect(answerOf("osascript", 0, ",true\n")).toBe("timeout")
    expect(answerOf("osascript", 1, "")).toBe("deny")
  })

  test("zenity: 0 allow, 1 deny, 5 timeout, other codes deny", () => {
    expect(answerOf("zenity", 0, "")).toBe("allow")
    expect(answerOf("zenity", 1, "")).toBe("deny")
    expect(answerOf("zenity", 5, "")).toBe("timeout")
    expect(answerOf("zenity", 255, "")).toBe("deny")
  })

  test("kdialog: only 0 allows", () => {
    expect(answerOf("kdialog", 0, "")).toBe("allow")
    expect(answerOf("kdialog", 1, "")).toBe("deny")
  })
})
