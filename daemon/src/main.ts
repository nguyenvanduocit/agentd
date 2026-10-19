#!/usr/bin/env node
import { parseCli, USAGE } from "./core/cli.ts"
import { runApps } from "./apps-cli.ts"
import { runBridge, runPageBridge } from "./bridge.ts"
import { runDaemon } from "./daemon.ts"
import { exitOnFatal, say, warn } from "./log.ts"
import { runPair } from "./pair.ts"
import { runService } from "./service.ts"
import { VERSION } from "./version.ts"

async function dispatch(argv: string[]): Promise<number> {
  const parsed = parseCli(argv)
  if (!parsed.ok) {
    warn(`${parsed.error}\n\n${USAGE}`)
    return 2
  }
  const cmd = parsed.command
  switch (cmd.kind) {
    case "help":
      say(USAGE)
      return 0
    case "version":
      say(VERSION)
      return 0
    case "apps":
      return runApps(cmd)
    case "pair":
      return runPair(cmd)
    case "mcp":
      return runBridge(cmd)
    case "page-mcp":
      return runPageBridge(process.env)
    case "run":
      return runDaemon(cmd)
    case "service":
      return runService(cmd)
  }
}

exitOnFatal()
dispatch(process.argv.slice(2)).then(
  (code) => process.exit(code),
  (e: unknown) => {
    warn(e instanceof Error ? e.message : String(e))
    process.exit(1)
  },
)
