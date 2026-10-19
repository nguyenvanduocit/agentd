/** What the daemon asks the user before it lets an origin run agents. */
export interface Question {
  /** The page's origin, from the browser's Origin header. */
  origin: string
  /** The agents that are ready now. */
  agents: string[]
}

export interface Text {
  title: string
  body: string
}

export type Answer = "allow" | "deny" | "timeout" | "unavailable"

export function consentText(q: Question): Text {
  const agents = q.agents.length > 0 ? q.agents.join(", ") : "no agent is ready yet"
  return {
    title: `Allow ${q.origin} to use your agents?`,
    body:
      `${q.origin} wants to connect to this computer.\n\n` +
      `If you allow it, that app can run your agents (${agents}) here, with access to your files and shell, until you disconnect it ` +
      `(from the daemon's icon in the menu bar, or with: agentd apps remove ${q.origin}).\n\nOnly allow apps you trust.`,
  }
}

export type DialogKind = "osascript" | "zenity" | "kdialog"

export interface DialogPlan {
  kind: DialogKind
  file: string
  args: string[]
}

// The text travels as arguments (`argv` for osascript), never inside the script: an origin cannot change the script.
const OSASCRIPT = [
  "on run argv",
  'set r to display dialog (item 2 of argv) with title (item 1 of argv) buttons {"Deny", "Allow"} default button "Deny" cancel button "Deny" with icon caution giving up after TIMEOUT',
  'return (button returned of r) & "," & (gave up of r)',
  "end run",
]

/** The command that shows the dialog on `platform`, or undefined when the machine has no tool to show one. */
export function dialogPlan(platform: NodeJS.Platform, has: (cmd: string) => boolean, t: Text, timeoutS: number): DialogPlan | undefined {
  if (platform === "darwin" && has("osascript")) {
    const script = OSASCRIPT.map((l) => l.replace("TIMEOUT", String(timeoutS)))
    return { kind: "osascript", file: "osascript", args: [...script.flatMap((l) => ["-e", l]), t.title, t.body] }
  }
  if (platform !== "linux") return undefined
  if (has("zenity")) {
    return {
      kind: "zenity",
      file: "zenity",
      args: ["--question", "--no-markup", `--title=${t.title}`, `--text=${t.body}`, "--ok-label=Allow", "--cancel-label=Deny", `--timeout=${timeoutS}`],
    }
  }
  if (has("kdialog")) return { kind: "kdialog", file: "kdialog", args: ["--title", t.title, "--yes-label", "Allow", "--no-label", "Deny", "--yesno", t.body] }
  return undefined
}

/** What the user did, from the dialog tool's exit code and output. Anything unexpected is a Deny. */
export function answerOf(kind: DialogKind, code: number, stdout: string): Answer {
  if (kind === "osascript") {
    if (code !== 0) return "deny"
    const [button, gaveUp] = stdout.trim().split(",")
    if (gaveUp === "true") return "timeout"
    return button === "Allow" ? "allow" : "deny"
  }
  if (code === 0) return "allow"
  return kind === "zenity" && code === 5 ? "timeout" : "deny"
}
