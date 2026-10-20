import { createLocalDaemon, desktopDownloads, installCommand, recommendInstall } from "@agentd/client"

interface DaemonInfo {
  id: string
  name: string
  state: string
  version: string
  agents: { name: string; ready: boolean }[]
}
interface List {
  daemons: DaemonInfo[]
  pairing: { code: string }
}

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T
const post = async (path: string, method = "POST"): Promise<unknown> => (await fetch(path, { method })).json()

const config = (await (await fetch("/api/config")).json()) as { agentdUrl: string }
let list: List = { daemons: [], pairing: { code: "" } }
const downloads = desktopDownloads({ agentd: config.agentdUrl })
if (downloads !== undefined) {
  $<HTMLAnchorElement>("download-arm64").href = downloads.appleSilicon
  $<HTMLAnchorElement>("download-amd64").href = downloads.intel
}

// The app's own calls, handed to the SDK: a one-time code from this app's server.
const local = createLocalDaemon({
  startPairing: async () => ((await post("/api/pairing")) as List).pairing.code,
  cancelPairing: () => post("/api/pairing", "DELETE"),
})

function render(): void {
  const s = local.state
  const online = list.daemons.filter((d) => d.state === "online")
  // The phase says where the connection stands: nothing is offered before the first look has ended, and a daemon this page cannot talk to is said so.
  const looking = s.phase === "detecting"
  const install = recommendInstall() // "dmg" on a Mac, "script" on Linux, "unsupported" elsewhere
  $("local").hidden = !s.canConnect
  $("local-error").textContent = s.error
  $("local-incompatible").textContent = s.incompatible ?? ""
  $<HTMLButtonElement>("connect-local").disabled = s.connecting
  $("local-note").textContent = s.connecting ? "Answer the dialog that opened on this computer." : "The daemon asks you to confirm before it connects."
  $("install").hidden = list.pairing.code === ""
  $("install-start").hidden = looking || install === "unsupported" || s.canConnect || list.pairing.code !== "" || online.length > 0
  $("unsupported").hidden = looking || install !== "unsupported" || s.canConnect || online.length > 0
  $("download").hidden = $("install-start").hidden || downloads === undefined || install !== "dmg"
  const command = list.pairing.code === "" ? undefined : installCommand({ agentd: config.agentdUrl, app: location.origin, code: list.pairing.code })
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
  list = (await (await fetch("/api/daemons")).json()) as List
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
// every few seconds while agentd is absent or not yet connected, so installing it and coming back needs no reload.
local.watch()
await refresh()
render()
setInterval(() => void refresh(), 1000)
