import { createLocalDaemon, installCommand } from "@agentd/client"

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

// The app's own calls, handed to the SDK: a one-time code from this app's server.
const local = createLocalDaemon({
  startPairing: async () => ((await post("/api/pairing")) as List).pairing.code,
  cancelPairing: () => post("/api/pairing", "DELETE"),
})

function render(): void {
  const s = local.state
  const online = list.daemons.filter((d) => d.state === "online")
  $("local").hidden = !s.canConnect
  $("local-error").textContent = s.error
  $<HTMLButtonElement>("connect-local").disabled = s.connecting
  $("local-note").textContent = s.connecting ? "Answer the dialog that opened on this computer." : "The daemon asks you to confirm before it connects."
  $("install").hidden = list.pairing.code === ""
  $("install-start").hidden = s.canConnect || list.pairing.code !== "" || online.length > 0
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
window.addEventListener("focus", () => void local.detect())

await local.detect()
await refresh()
setInterval(() => void refresh(), 1000)
