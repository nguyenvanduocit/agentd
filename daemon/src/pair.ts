import { createInterface } from "node:readline"
import { hostname } from "node:os"
import { pairUrl, serverOrigin } from "./core/server.ts"
import type { Command } from "./core/cli.ts"
import { registryFile, updateRegistry } from "./config.ts"
import { addApp, type App } from "./core/registry.ts"
import { currentHost } from "./host.ts"
import { say } from "./log.ts"

/** Reads one non-empty line from stdin, used when --code is not on the command line. */
async function readCodeFromStdin(): Promise<string> {
  process.stderr.write("Pairing code: ")
  const rl = createInterface({ input: process.stdin, terminal: false })
  for await (const line of rl) {
    const code = line.trim()
    if (code !== "") { rl.close(); return code }
  }
  throw new PairError("no pairing code provided (pass --code <code> or pipe it to stdin)")
}

const TIMEOUT_MS = 15_000
const REFUSED = new Set([400, 401, 403, 404, 410])

export class PairError extends Error {}

export type Fetch = (url: string, init: RequestInit) => Promise<Response>

async function detail(res: Response): Promise<string> {
  const text = (await res.text().catch(() => "")).trim()
  return text === "" ? "" : `: ${text.slice(0, 200)}`
}

/** fetch hides the useful part (ECONNREFUSED, ENOTFOUND, a bad port) in `cause`. */
function reason(e: unknown): string {
  if (!(e instanceof Error)) return String(e)
  return e.cause instanceof Error ? e.cause.message : e.message
}

async function post(origin: string, code: string, fetchFn: Fetch): Promise<Response> {
  const body = JSON.stringify({ code, hostname: hostname(), os: process.platform, arch: process.arch })
  try {
    return await fetchFn(pairUrl(origin), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
      signal: AbortSignal.timeout(TIMEOUT_MS),
    })
  } catch (e) {
    throw new PairError(`cannot reach ${origin} (${reason(e)}). Check the address and your network, then run the command again.`)
  }
}

/** Trades the single-use code for a token and saves the app to the registry in `file`. Resolves to the saved app. */
export async function pair(server: string, code: string, file: string, fetchFn: Fetch = fetch): Promise<App> {
  const origin = serverOrigin(server)
  const res = await post(origin, code, fetchFn)
  if (!res.ok) {
    if (REFUSED.has(res.status)) {
      throw new PairError(
        `${origin} refused the pairing code (HTTP ${res.status}${await detail(res)}). ` +
          "A code works once and expires after 10 minutes: copy a fresh command from the app's web UI.",
      )
    }
    throw new PairError(`${origin} failed to pair (HTTP ${res.status}${await detail(res)}). Try again in a minute.`)
  }
  const reply = (await res.json().catch(() => undefined)) as { token?: unknown } | undefined
  const token = reply?.token
  if (typeof token !== "string" || token === "") throw new PairError(`${origin} answered without a token: is this an app that serves agentd?`)
  const app = { server: origin, token }
  await updateRegistry(file, (r) => addApp(r, app))
  return app
}

/** `pair --app <url> [--code <code>]`: resolves to the exit code. */
export async function runPair(cmd: Extract<Command, { kind: "pair" }>): Promise<number> {
  const code = cmd.code ?? process.env.AGENTD_PAIR_CODE ?? (await readCodeFromStdin())
  const file = registryFile(cmd, currentHost())
  const app = await pair(cmd.app, code, file)
  say(`paired with ${app.server}; saved ${file}. Run agentd to connect.`)
  return 0
}
