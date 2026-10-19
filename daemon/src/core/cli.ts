import { parseArgs } from "node:util"

export type ServiceAction = "install" | "uninstall" | "start" | "restart" | "status" | "logs"

const SERVICE_ACTIONS: readonly string[] = ["install", "uninstall", "start", "restart", "status", "logs"]
const DEFAULT_LOG_LINES = 100

export type Command =
  | { kind: "run"; app: string | undefined; dir: string | undefined; config: string | undefined }
  | { kind: "pair"; app: string; code: string | undefined; config: string | undefined }
  | { kind: "mcp"; session: string; app: string | undefined; config: string | undefined }
  | { kind: "apps"; action: "list" | "remove"; origin: string | undefined; config: string | undefined }
  | { kind: "service"; action: ServiceAction; lines: number; follow: boolean; dir: string | undefined }
  | { kind: "version" }
  | { kind: "help" }

export type Parsed = { ok: true; command: Command } | { ok: false; error: string }

export const USAGE = `agentd: runs your agents for the apps you allow

usage:
  agentd pair --app <url> [--code <code>]     pair this machine with an app; code is read from stdin if omitted
  agentd [run] [--app <url>] [--dir <dir>]    connect and serve agents (default command)
  agentd apps [list]                         the apps and web pages this machine is connected to
  agentd apps remove <origin>                disconnect one of them (service restart applies it)
  agentd service install [--dir <dir>]       start at login and restart when it dies (launchd, systemd)
  agentd service start|restart               start it again after a Stop from the web UI; restart it
  agentd service uninstall|status
  agentd service logs [-n <lines>] [-f]      the daemon's log
  agentd version

options:
  --app <url>      the app, by its origin (default: every app saved by pair)
  --dir <dir>      working directory of the agents (default: the current directory; service install: your home)
  --config <file>  the config file (default: <config dir>/agentd/daemon.json)
  -n, --lines <n>  service logs: how many lines (default 100)
  -f, --follow     service logs: keep printing
`

const OPTIONS = {
  app: { type: "string" },
  dir: { type: "string" },
  config: { type: "string" },
  code: { type: "string" },
  session: { type: "string" },
  lines: { type: "string", short: "n" },
  follow: { type: "boolean", short: "f" },
  help: { type: "boolean", short: "h" },
} as const

type Values = ReturnType<typeof parseArgs<{ options: typeof OPTIONS; allowPositionals: true }>>["values"]

function fail(error: string): Parsed {
  return { ok: false, error }
}

function service(action: string | undefined, v: Values): Parsed {
  if (action === undefined || !SERVICE_ACTIONS.includes(action)) return fail(`service needs one of: ${SERVICE_ACTIONS.join(", ")}`)
  const lines = v.lines === undefined ? DEFAULT_LOG_LINES : Number(v.lines)
  if (!Number.isInteger(lines) || lines < 1) return fail(`--lines "${String(v.lines)}" is not a positive number`)
  return { ok: true, command: { kind: "service", action: action as ServiceAction, lines, follow: v.follow === true, dir: v.dir } }
}

function pair(v: Values): Parsed {
  if (!v.app) return fail("pair needs --app <url>")
  return { ok: true, command: { kind: "pair", app: v.app, code: v.code, config: v.config } }
}

function mcp(v: Values): Parsed {
  if (!v.session) return fail("mcp needs --session <id>")
  return { ok: true, command: { kind: "mcp", session: v.session, app: v.app, config: v.config } }
}

function apps(action: string | undefined, origin: string | undefined, v: Values): Parsed {
  if (action === undefined || action === "list") return { ok: true, command: { kind: "apps", action: "list", origin: undefined, config: v.config } }
  if (action !== "remove") return fail("apps needs one of: list, remove")
  if (origin === undefined) return fail("apps remove needs <origin>, for example https://app.example.com")
  return { ok: true, command: { kind: "apps", action: "remove", origin, config: v.config } }
}

function build(name: string | undefined, rest: string | undefined, extra: string | undefined, v: Values): Parsed {
  if (name === "service") return service(rest, v)
  if (name === "apps") return apps(rest, extra, v)
  if (rest !== undefined) return fail(`unexpected argument "${rest}"`)
  switch (name) {
    case undefined:
    case "run":
      return { ok: true, command: { kind: "run", app: v.app, dir: v.dir, config: v.config } }
    case "version":
      return { ok: true, command: { kind: "version" } }
    case "pair":
      return pair(v)
    case "mcp":
      return mcp(v)
    default:
      return fail(`unknown command "${name}"`)
  }
}

export function parseCli(argv: readonly string[]): Parsed {
  let parsed: ReturnType<typeof parseArgs<{ options: typeof OPTIONS; allowPositionals: true }>>
  try {
    parsed = parseArgs({ args: [...argv], options: OPTIONS, allowPositionals: true, strict: true })
  } catch (e) {
    return fail(e instanceof Error ? e.message : String(e))
  }
  if (parsed.values.help) return { ok: true, command: { kind: "help" } }
  const [name, rest, extra, ...more] = parsed.positionals
  if (more.length > 0 || (extra !== undefined && name !== "apps")) return fail(`unexpected argument "${extra !== undefined && name !== "apps" ? extra : more[0]}"`)
  return build(name, rest, extra, parsed.values)
}
