import { createLocalDaemon, desktopDownloads, recommendInstall, type DaemonInfo, type DaemonList, type LocalState } from "@agentd/client"

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T
const post = async (path: string, method = "POST"): Promise<unknown> => (await fetch(path, { method })).json()

const config = (await (await fetch("/api/config")).json()) as { agentdUrl: string; ports?: number[] }
let list: DaemonList = { daemons: [], pairing: { code: "", expiresAt: 0 } }
const downloads = desktopDownloads({ agentd: config.agentdUrl })
if (downloads !== undefined) {
  $<HTMLAnchorElement>("download-arm64").href = downloads.appleSilicon
  $<HTMLAnchorElement>("download-amd64").href = downloads.intel
}

// The app's own calls, handed to the SDK: a one-time code from this app's server. `ports` pins where to look for a
// daemon (development and tests: a second daemon beside your own); without it the SDK looks on agentd's ten ports.
const local = createLocalDaemon({
  name: "Minimal app", // what the user reads in the consent window and the menu, beside this page's address
  startPairing: async () => ((await post("/api/pairing")) as DaemonList).pairing.code,
  cancelPairing: () => post("/api/pairing", "DELETE"),
  ...(config.ports === undefined || config.ports.length === 0 ? {} : { ports: config.ports }),
})

// What the page remembers between renders. The list is fetched again every second, so what a person is in the middle of (a
// removal that waits for their answer, an answer on screen) lives here and not in the DOM, which is drawn again only when
// what it shows has changed: a button the keyboard is on is then still there a second later.
let confirming = "" // the computer whose removal waits for an answer
let removing = "" // the computer being removed now
let removeError = ""
let answer = "" // the agent's answer on screen ...
let answerFrom = "" // ... and the computers that were online when it came: the answer goes when they do
let drawn = "" // what the list shows now
let wantFocus: string | undefined // the focus key to put the keyboard on after the next draw

const onlineKey = (): string => list.daemons.filter((d) => d.state === "online").map((d) => d.id).sort().join(",")

/** What the connection screen offers in each phase: one switch, no combination of flags. */
function offers(s: LocalState): { connect: boolean; install: boolean } {
  switch (s.phase) {
    case "detecting": // the first look has not ended: offer nothing yet
    case "connected":
      return { connect: false, install: false }
    case "absent": // no daemon answered: agentd is not installed, not running, or blocked
      return { connect: false, install: true }
    case "available": // a daemon is here; the user can connect it
    case "connecting": // ... and its dialog waits for an answer
      return { connect: true, install: false }
    case "refused": // the last connection did not happen: try again, or (a daemon this page cannot talk to) install a fitting one
      return { connect: s.canConnect, install: !s.canConnect }
  }
}

function render(): void {
  const s = local.state
  const online = list.daemons.filter((d) => d.state === "online")
  const how = recommendInstall() // "dmg" on a Mac, "unsupported" on Linux, Windows and phones
  const offer = offers(s)
  const install = offer.install && online.length === 0
  $("local").hidden = !offer.connect
  $("local-error").textContent = s.error
  $("local-incompatible").textContent = s.incompatible ?? ""
  $<HTMLButtonElement>("connect-local").disabled = s.phase === "connecting"
  $("local-note").textContent = s.phase === "connecting" ? "Answer the dialog that opened on this computer." : "agentd asks you to confirm before it connects."
  $("unsupported").hidden = !install || how !== "unsupported"
  $("download").hidden = !install || how !== "dmg" || downloads === undefined
  // An answer belongs to the computers that were online when it came; when they are gone it is no longer true.
  if (answer !== "" && onlineKey() !== answerFrom) answer = ""
  $("ping-result").textContent = answer
  $("daemons-error").textContent = removeError
  // An empty list says so, and what to do next; nothing until the first look at this computer has ended.
  $("daemons-empty").hidden = list.daemons.length > 0 || s.phase === "detecting"
  $("daemons-empty").textContent = offer.connect
    ? "No computer is connected yet. Connect this computer above, then it appears here."
    : offer.install
      ? "No computer is connected yet. Install agentd above, then it appears here."
      : "No computer is connected yet."
  drawComputers()
  $("ping").hidden = online.length === 0
}

/** A button of a computer's row. `name` is what a screen reader says: "Remove laptop", not "Remove" for every row. */
function button(testid: string, label: string, name: string, focusKey: string, onClick: () => void, disabled = false): HTMLButtonElement {
  const b = document.createElement("button")
  b.dataset.testid = `daemon-${testid}`
  b.dataset.focusKey = focusKey
  b.textContent = label
  b.setAttribute("aria-label", name)
  b.disabled = disabled
  b.addEventListener("click", onClick)
  return b
}

/** What can be done with a computer: Remove, which asks first (in words, with the safe answer in focus), then Remove or Keep. */
function actions(d: DaemonInfo): HTMLElement {
  const box = document.createElement("div")
  box.className = "computer-actions"
  if (confirming !== d.id) {
    box.append(button("remove", "Remove", `Remove ${d.name}`, `${d.id}:remove`, () => ask(d.id)))
    return box
  }
  const question = document.createElement("span")
  question.dataset.testid = "daemon-confirm"
  question.textContent = `Remove ${d.name}? It will have to be paired again.`
  box.append(
    question,
    button("remove-confirm", "Remove", `Remove ${d.name}`, `${d.id}:remove-confirm`, () => void remove(d), removing === d.id),
    button("keep", "Keep", `Keep ${d.name}`, `${d.id}:keep`, () => ask("", d.id), removing === d.id),
  )
  return box
}

/** One computer: its name and state first, the host name under it, muted and cut short (the whole name is its title). */
function row(d: DaemonInfo): HTMLElement {
  const li = document.createElement("li")
  li.className = "computer"
  li.dataset.testid = "daemon"
  const main = document.createElement("div")
  main.className = "computer-main"
  const line = document.createElement("span")
  line.className = "computer-line"
  const name = document.createElement("span")
  name.dataset.testid = "daemon-name"
  name.textContent = d.name
  line.append(name, `: ${d.state}${d.version === "" ? "" : ` (agentd ${d.version})`}`)
  main.append(line)
  if (d.hostname !== "" && d.hostname !== d.name) {
    const host = document.createElement("span")
    host.className = "host muted"
    host.dataset.testid = "daemon-host"
    host.textContent = d.hostname
    host.title = d.hostname
    main.append(host)
  }
  li.append(main, actions(d))
  return li
}

/** Draws the list again only when what it shows has changed, and puts the keyboard back where it was (or where it should go). */
function drawComputers(): void {
  const shown = JSON.stringify([list.daemons.map((d) => [d.id, d.name, d.hostname, d.state, d.version]), confirming, removing])
  if (shown === drawn) return
  drawn = shown
  const had = (document.activeElement as HTMLElement | null)?.dataset.focusKey
  $("daemons").replaceChildren(...list.daemons.map(row))
  const key = wantFocus ?? had
  wantFocus = undefined
  if (key !== undefined) document.querySelector<HTMLElement>(`[data-focus-key="${CSS.escape(key)}"]`)?.focus()
}

/** Asks whether to remove a computer (or, with an empty id, stops asking about `back`, and the keyboard goes back to its Remove). */
function ask(id: string, back?: string): void {
  confirming = id
  removeError = ""
  wantFocus = id === "" ? `${back}:remove` : `${id}:keep`
  render()
}

/** Removes a computer through the app's own route; a failure is told in words and the list is fetched again either way. */
async function remove(d: DaemonInfo): Promise<void> {
  removing = d.id
  render()
  const res = await fetch(`/api/daemons/${encodeURIComponent(d.id)}`, { method: "DELETE" })
  removing = ""
  confirming = ""
  if (res.ok) {
    removeError = ""
    list = (await res.json()) as DaemonList
    $("daemons-heading").focus() // the row the keyboard was on is gone
  } else {
    const said = ((await res.json().catch(() => ({}))) as { error?: string }).error ?? `status ${res.status}`
    // 404: another tab (or the computer's own owner) removed it first. The list is right after the refresh below.
    removeError = res.status === 404 ? `${d.name} is already gone. The list is up to date now.` : `Could not remove ${d.name}: ${said}`
  }
  await refresh()
}

async function refresh(): Promise<void> {
  list = (await (await fetch("/api/daemons")).json()) as DaemonList
  if (!list.daemons.some((d) => d.id === confirming)) confirming = "" // the computer a question was about is gone
  render()
}

local.subscribe(render)
$("connect-local").addEventListener("click", () => void local.connect().then(refresh))
$("ping").addEventListener("click", async () => {
  const from = onlineKey()
  const res = (await post("/api/ping")) as { line?: string; error?: string }
  answer = res.line ?? `error: ${res.error ?? "unknown"}`
  answerFrom = from
  render()
})
// This page is the connection screen itself, so it looks for agentd as it opens (an app with other content starts
// `watch` when its settings open: Chrome asks the user about the local network at the first request). It looks again
// every few seconds, so installing agentd and coming back needs no reload, and a daemon that left is noticed.
local.watch()
await refresh()
render()
setInterval(() => void refresh(), 1000)
