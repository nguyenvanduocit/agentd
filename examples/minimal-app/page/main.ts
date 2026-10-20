import { createLocalDaemon, desktopDownloads, installCommand, recommendInstall, type DaemonList, type LocalState } from "@agentd/client"

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T
const post = async (path: string, method = "POST"): Promise<unknown> => (await fetch(path, { method })).json()

const config = (await (await fetch("/api/config")).json()) as { agentdUrl: string }
let list: DaemonList = { daemons: [], pairing: { code: "", expiresAt: 0 }, latestVersion: "" }
const downloads = desktopDownloads({ agentd: config.agentdUrl })
if (downloads !== undefined) {
  $<HTMLAnchorElement>("download-arm64").href = downloads.appleSilicon
  $<HTMLAnchorElement>("download-amd64").href = downloads.intel
}

// The app's own calls, handed to the SDK: a one-time code from this app's server.
const local = createLocalDaemon({
  startPairing: async () => ((await post("/api/pairing")) as DaemonList).pairing.code,
  cancelPairing: () => post("/api/pairing", "DELETE"),
})

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
  const pairing = list.pairing.code !== ""
  const how = recommendInstall() // "dmg" on a Mac, "script" on Linux, "unsupported" elsewhere
  const offer = offers(s)
  const install = offer.install && !pairing && online.length === 0
  $("local").hidden = !offer.connect
  $("local-error").textContent = s.error
  $("local-incompatible").textContent = s.incompatible ?? ""
  $<HTMLButtonElement>("connect-local").disabled = s.phase === "connecting"
  $("local-note").textContent = s.phase === "connecting" ? "Answer the dialog that opened on this computer." : "The daemon asks you to confirm before it connects."
  $("install").hidden = !pairing
  $("install-start").hidden = !install || how === "unsupported"
  $("unsupported").hidden = !install || how !== "unsupported"
  $("download").hidden = $("install-start").hidden || downloads === undefined || how !== "dmg"
  const command = pairing ? installCommand({ agentd: config.agentdUrl, app: location.origin, code: list.pairing.code }) : undefined
  $("install-command").textContent = command ?? ""
  $("daemons").replaceChildren(
    ...list.daemons.map((d) => {
      const li = document.createElement("li")
      li.dataset.testid = "daemon"
      li.textContent = `${d.name}: ${d.state}${d.version === "" ? "" : ` (agentd ${d.version})`}`
      return li
    }),
  )
  $("ping").hidden = online.length === 0
}

async function refresh(): Promise<void> {
  list = (await (await fetch("/api/daemons")).json()) as DaemonList
  render()
}

local.subscribe(render)
$("connect-local").addEventListener("click", () => void local.connect().then(refresh))
$("install-start").addEventListener("click", () => void post("/api/pairing").then(refresh))
$("ping").addEventListener("click", async () => {
  const res = (await post("/api/ping")) as { line?: string; error?: string }
  $("ping-result").textContent = res.line ?? `error: ${res.error ?? "unknown"}`
})
// This page is the connection screen itself, so it looks for agentd as it opens (an app with other content starts
// `watch` when its settings open: Chrome asks the user about the local network at the first request). It looks again
// every few seconds, so installing agentd and coming back needs no reload, and a daemon that left is noticed.
local.watch()
await refresh()
render()
setInterval(() => void refresh(), 1000)
