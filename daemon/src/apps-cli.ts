import type { Command } from "./core/cli.ts"
import { loadRegistry, registryFile, saveRegistry } from "./config.ts"
import { currentHost } from "./host.ts"
import { say, warn } from "./log.ts"
import { hasApp, hasBrowser, removeOrigin } from "./core/registry.ts"
import { serverOrigin } from "./core/server.ts"

/** `apps [list]` and `apps remove <origin>`: resolves to the exit code. Tokens are never printed. */
export async function runApps(cmd: Extract<Command, { kind: "apps" }>): Promise<number> {
  const file = registryFile(cmd, currentHost())
  const registry = await loadRegistry(file)
  if (cmd.action === "list") {
    for (const a of registry.apps) say(`${a.server}  app`)
    for (const g of registry.browsers) say(`${g.origin}  browser, granted ${g.grantedAt.slice(0, 10)}`)
    return 0
  }
  const origin = serverOrigin(cmd.origin ?? "")
  if (!hasApp(registry, origin) && !hasBrowser(registry, origin)) {
    warn(`not connected to ${origin}`)
    return 1
  }
  await saveRegistry(file, removeOrigin(registry, origin))
  say(`disconnected ${origin}; a running daemon applies it after: agentd service restart`)
  return 0
}
