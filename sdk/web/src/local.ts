import { SUPPORTED_PROTOCOLS, connectBrowser, connectServer, detect, disconnect, reasonText, type ConnectResult, type Daemon, type Detected, type Reason } from "./client.js"

export interface LocalClient {
  detect: () => Promise<Detected>
  connectServer: (d: Daemon, a: { code: string }) => Promise<ConnectResult>
  connectBrowser: (d: Daemon) => Promise<ConnectResult>
  /** Its answer is read when it is a `DisconnectResult` (a failure is then shown); anything else is taken as done. */
  disconnect: (d: Daemon) => Promise<unknown>
}

/** Runs `tick` every `everyMs` until the returned function is called. Replaces `setInterval` for `watch` (tests). */
export type Interval = (tick: () => void, everyMs: number) => () => void

/** The daemon connects to the app's server, which runs the agents (the default). */
export interface LocalServerDeps {
  mode?: "server"
  /** Starts a pairing on the app's server and answers its code; "" when the server gave none. */
  startPairing: () => Promise<string>
  cancelPairing: () => Promise<unknown>
  /** The calls to the daemon (default: this package's own). */
  client?: LocalClient
  /** The timer of `watch` (default: `setInterval`). */
  interval?: Interval
}

/** The page itself runs agents on this computer (`openSession`): no server takes part. */
export interface LocalBrowserDeps {
  mode: "browser"
  /** The calls to the daemon (default: this package's own). */
  client?: LocalClient
  /** The timer of `watch` (default: `setInterval`). */
  interval?: Interval
}

export type LocalDaemonDeps = LocalServerDeps | LocalBrowserDeps

/**
 * Where the connection stands, for one switch in the interface:
 * - `detecting`: the first look has not ended (until `detect()` or `watch()` is called, nothing has been looked for);
 * - `absent`: no daemon answered. agentd is not installed, not running, or the browser blocked the call: the page cannot tell which;
 * - `available`: a daemon is here and has not been allowed to serve this app (browser mode: this page) yet;
 * - `connecting`: a connection waits for the user's answer in the daemon's dialog;
 * - `connected`: the daemon serves this app (browser mode: this page);
 * - `refused`: the last connection did not happen (`reason`, `message`, `error`), or the daemon speaks a protocol this page does not (`incompatible`).
 */
export type LocalPhase = "detecting" | "absent" | "available" | "connecting" | "connected" | "refused"

export interface LocalState {
  found: Detected
  /** A connection is waiting for the user's answer in the daemon's dialog. */
  connecting: boolean
  /** Why the last connection did not happen, in plain words; "" when it did or none was tried. Never "" for a refused phase. */
  error: string
  /** A daemon is here, speaks a protocol this page does, and has not been allowed to serve this app (browser mode: this page) yet. */
  canConnect: boolean
  phase: LocalPhase
  /** Set with phase `refused` when the daemon gave a reason (possibly one this SDK does not know: `error` has words for it too). */
  reason?: Reason
  /** The daemon's own words for the refusal, when it gave any. */
  message?: string
  /** A sentence when the daemon states a protocol this SDK does not speak (`SUPPORTED_PROTOCOLS`); the phase is `refused`. */
  incompatible?: string
}

export interface LocalDaemon {
  /** The state now; a new object after every change. */
  readonly state: LocalState
  /** Calls `listener` with the new state after every change; returns what stops it. */
  subscribe: (listener: (state: LocalState) => void) => () => void
  /** Looks for a daemon (the settings tab opened, or the window got focus). */
  detect: () => Promise<void>
  /**
   * Looks now and then every `everyMs` (default 3000) while the phase is `detecting`, `absent`, `available` or `refused`
   * for a protocol mismatch, so a person who installs agentd and comes back to the page is found without a reload. It
   * looks no more while a connection waits, while one is made, and while a refusal is on the screen (a look clears it).
   * A listener hears of a change only. Returns what stops it. Start it when the user shows interest, not at page load.
   */
  watch: (everyMs?: number) => () => void
  /** Asks the user to let the daemon on this computer serve this app. True when it was allowed and now does. */
  connect: () => Promise<boolean>
  /** Takes back what this page was allowed (its browser grant and the app at its origin), then looks again; a failure shows as a refusal. */
  disconnect: () => Promise<void>
}

const DEFAULT_CLIENT: LocalClient = { detect: () => detect(), connectServer, connectBrowser, disconnect }
const DEFAULT_INTERVAL: Interval = (tick, everyMs) => {
  const id = setInterval(tick, everyMs)
  return () => clearInterval(id)
}
const WATCH_EVERY_MS = 3000

/** Why a connection did not happen: the sentence, and what the daemon said. */
interface Refusal {
  error: string
  reason?: Reason
  message?: string
}

function refusalOf(reason: Reason, message: string | undefined): Refusal {
  return { error: reasonText(reason), reason, ...(message === undefined ? {} : { message }) }
}

/** A sentence when the daemon states a protocol this SDK does not speak. */
function incompatibility(found: Detected): string | undefined {
  if (!found.found || found.protocol === undefined || SUPPORTED_PROTOCOLS.includes(found.protocol)) return undefined
  return found.protocol < Math.min(...SUPPORTED_PROTOCOLS)
    ? "This agentd is too old for this page. Update agentd."
    : "This agentd is newer than this page understands. Reload the page, or come back after the app updates."
}

function stateOf(mode: "server" | "browser", found: Detected, connecting: boolean, looked: boolean, refusal: Refusal | undefined): LocalState {
  const incompatible = incompatibility(found)
  const connected = found.found && found.granted[mode]
  const phase: LocalPhase = connecting
    ? "connecting"
    : refusal !== undefined || incompatible !== undefined
      ? "refused"
      : !looked
        ? "detecting"
        : !found.found
          ? "absent"
          : connected
            ? "connected"
            : "available"
  return {
    found,
    connecting,
    error: refusal?.error ?? incompatible ?? "",
    canConnect: found.found && !connected && incompatible === undefined,
    phase,
    ...(refusal?.reason === undefined ? {} : { reason: refusal.reason }),
    ...(refusal?.message === undefined ? {} : { message: refusal.message }),
    ...(incompatible === undefined ? {} : { incompatible }),
  }
}

/** Asks the daemon to connect to the app's server with a fresh pairing code: undefined when it does, or why not. */
async function askServer(deps: LocalServerDeps, client: LocalClient, d: Daemon): Promise<Refusal | undefined> {
  const code = await deps.startPairing()
  if (code === "") return { error: "The app could not start a connection. Try again." }
  const result = await client.connectServer(d, { code })
  if (result.granted) return undefined
  await deps.cancelPairing()
  return refusalOf(result.reason, result.message)
}

/** Asks the daemon to let this page run agents: undefined when it does, or why not. */
async function askBrowser(client: LocalClient, d: Daemon): Promise<Refusal | undefined> {
  const result = await client.connectBrowser(d)
  return result.granted ? undefined : refusalOf(result.reason, result.message)
}

/** What a client's answer to `disconnect` says went wrong: a `DisconnectResult` that is not a success; anything else is nothing. */
function disconnectFailure(answer: unknown): Refusal | undefined {
  if (typeof answer !== "object" || answer === null) return undefined
  const { disconnected, reason, message } = answer as { disconnected?: unknown; reason?: unknown; message?: unknown }
  if (disconnected !== false || typeof reason !== "string") return undefined
  return refusalOf(reason as Reason, typeof message === "string" ? message : undefined)
}

/**
 * The daemon that may be running on the computer this page is open on: whether it is there, and the connection the user
 * can ask it for. The daemon shows its own dialog; this only asks. Framework-agnostic: wrap `state` and `subscribe` in
 * the framework's own reactivity.
 */
export function createLocalDaemon(deps: LocalDaemonDeps): LocalDaemon {
  const client = deps.client ?? DEFAULT_CLIENT
  const interval = deps.interval ?? DEFAULT_INTERVAL
  const mode = deps.mode ?? "server"
  const ask = (d: Daemon): Promise<Refusal | undefined> => (deps.mode === "browser" ? askBrowser(client, d) : askServer(deps, client, d))
  let looked = false // the first look has ended
  let state = stateOf(mode, { found: false }, false, looked, undefined)
  const listeners = new Set<(state: LocalState) => void>()

  // quiet: a repeated look that finds what is already shown changes nothing and tells nobody.
  const set = (found: Detected, connecting: boolean, refusal: Refusal | undefined, quiet = false): void => {
    const next = stateOf(mode, found, connecting, looked, refusal)
    if (quiet && JSON.stringify(next) === JSON.stringify(state)) return
    state = next
    for (const l of listeners) {
      try {
        l(state)
      } catch {
        // One listener's failure is not another's.
      }
    }
  }

  // Looks again. The dialog of a waiting connection returns the focus that triggers this, so a look while connecting is
  // skipped; the connection looks again itself when it ends. The last refusal described an earlier attempt: a look clears it.
  async function look(quiet = false): Promise<void> {
    if (state.connecting) return
    const found = await client.detect()
    if (state.connecting) return // a connection began while this look was in flight: its end looks again itself
    looked = true
    set(found, false, undefined, quiet)
  }

  async function connect(): Promise<boolean> {
    const daemon = state.found
    if (!daemon.found || state.connecting || state.incompatible !== undefined) return false
    set(daemon, true, undefined)
    const end = (refusal: Refusal): false => {
      set(state.found, false, refusal)
      return false
    }
    try {
      const refusal = await ask(daemon)
      if (refusal !== undefined) return end(refusal)
    } catch (e) {
      return end({ error: e instanceof Error ? e.message : String(e) })
    }
    set(state.found, false, undefined)
    await look()
    return true
  }

  async function disconnectPage(): Promise<void> {
    const daemon = state.found
    const failure = daemon.found ? disconnectFailure(await client.disconnect(daemon)) : undefined
    await look()
    if (failure !== undefined) set(state.found, false, failure)
  }

  // A look is out at most once at a time, and a refusal the person has not read yet is left on the screen.
  function watch(everyMs = WATCH_EVERY_MS): () => void {
    let out = false
    const tick = (): void => {
      const held = state.connecting || state.phase === "connected" || (state.phase === "refused" && state.incompatible === undefined)
      if (out || held) return
      out = true
      void look(true).finally(() => (out = false))
    }
    tick()
    let stop: (() => void) | undefined = interval(tick, everyMs)
    return () => {
      stop?.()
      stop = undefined
    }
  }

  return {
    get state() {
      return state
    },
    subscribe(listener) {
      listeners.add(listener)
      return () => void listeners.delete(listener)
    },
    detect: () => look(),
    watch,
    connect,
    disconnect: disconnectPage,
  }
}
