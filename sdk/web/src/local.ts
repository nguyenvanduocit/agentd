import {
  SUPPORTED_PROTOCOLS,
  SessionError,
  connectBrowser,
  connectServer,
  detect,
  disconnect,
  openSession as openAgentSession,
  reasonText,
  type AbsentDaemon,
  type AgentSession,
  type ConnectResult,
  type Daemon,
  type Detected,
  type DetectOptions,
  type DisconnectResult,
  type FetchLike,
  type FoundDaemon,
  type Reason,
} from "./client.js"

export interface LocalClient {
  detect: () => Promise<Detected>
  connectServer: (d: Daemon, a: { code: string }) => Promise<ConnectResult>
  connectBrowser: (d: Daemon) => Promise<ConnectResult>
  /** Its answer is read when it is a `DisconnectResult` (a failure is then shown); no answer is taken as done. */
  disconnect: (d: Daemon) => Promise<DisconnectResult | void>
}

/** Runs `tick` every `everyMs` until the returned function is called. Replaces `setInterval` for `watch` (tests). */
export type Interval = (tick: () => void, everyMs: number) => () => void

/**
 * What both kinds of deps share. Without `client`, the calls to the daemon are this package's own, made with `fetch` on
 * `ports` (default: agentd's ten), so a test against a real daemon needs no hand-written client: hand it a `fetch` that
 * adds the Origin header a browser would, and the `WebSocket` of your runtime (since 0.7.0). A `client` replaces them all.
 */
export interface LocalCommonDeps {
  /**
   * What the app calls itself: the daemon's dialog and menu show it beside this page's origin. Required unless `client`
   * replaces the calls to the daemon (`createLocalDaemon` throws without it).
   */
  name?: string
  /** The calls to the daemon (default: this package's own, made with the options below). */
  client?: LocalClient
  /** The timer of `watch` (default: `setInterval`). */
  interval?: Interval
  /** The ports to look for the daemon on (default: `DEFAULT_PORTS`). Since 0.7.0. */
  ports?: readonly number[]
  /** The `fetch` for every call to the daemon (default: the global one). Since 0.7.0. */
  fetch?: FetchLike
  /** How long a look waits for a port to answer, in milliseconds (default: 800). Since 0.7.0. */
  timeoutMs?: number
  /** The `WebSocket` that `openSession` opens its socket with (default: the global one). Since 0.7.0. */
  WebSocket?: typeof WebSocket
}

/** The daemon connects to the app's server, which runs the agents (the default). */
export interface LocalServerDeps extends LocalCommonDeps {
  mode?: "server"
  /** Starts a pairing on the app's server and answers its code; "" when the server gave none. */
  startPairing: () => Promise<string>
  cancelPairing: () => Promise<unknown>
}

/** The page itself runs agents on this computer (`openSession`): no server takes part. */
export interface LocalBrowserDeps extends LocalCommonDeps {
  mode: "browser"
}

export type LocalDaemonDeps = LocalServerDeps | LocalBrowserDeps

/**
 * Where the connection stands, for one switch in the interface:
 * - `detecting`: the first look has not ended (until `detect()` or `watch()` is called, nothing has been looked for);
 * - `absent`: no daemon answered. agentd is not installed, not running, or the browser blocked the call: the page cannot tell which. A daemon that answered with a refusal of this page is still `absent`, and `reason` says which;
 * - `available`: a daemon is here and has not been allowed to serve this app (browser mode: this page) yet;
 * - `connecting`: a connection waits for the user's answer in the daemon's dialog;
 * - `connected`: the daemon serves this app (browser mode: this page);
 * - `refused`: the last connection did not happen (`reason`, `message`, `error`), or the daemon speaks a protocol this page does not (`reason: "incompatible"`).
 */
export type LocalPhase = "detecting" | "absent" | "available" | "connecting" | "connected" | "refused"

/** What every state has. */
interface LocalStateBase {
  found: Detected
  /** A connection is waiting for the user's answer in the daemon's dialog. */
  connecting: boolean
  /** Why the last connection did not happen, in plain words; "" when it did or none was tried. Never "" for a refused phase. */
  error: string
  /** A daemon is here, speaks a protocol this page does, and has not been allowed to serve this app (browser mode: this page) yet. */
  canConnect: boolean
  phase: LocalPhase
  /** Set with phase `refused`, and with `absent` when a daemon refused this page. May be a reason this SDK does not know (a newer daemon's): `error` has words for it too. */
  reason?: Reason
  /** The daemon's own words for the refusal, when it gave any. */
  message?: string
  /** A sentence when the daemon states a protocol this SDK does not speak (`SUPPORTED_PROTOCOLS`); the phase is `refused`. */
  incompatible?: string
  /** Seconds after which asking again can work, when the daemon said (`cooldown`, `locked`). Since 0.7.0. */
  retryAfter?: number
}

/**
 * The state of the connection. Read `phase` and the rest narrows: `connected` and `available` have a `found` daemon,
 * `absent` has none, `refused` has a `reason` (since 0.7.0 the type is one case per phase; the fields are the same).
 */
export type LocalState = LocalStateBase &
  (
    | { phase: "detecting" }
    | { phase: "absent"; found: AbsentDaemon; canConnect: false; connecting: false }
    | { phase: "available"; found: FoundDaemon; canConnect: true; connecting: false; error: "" }
    | { phase: "connecting"; found: FoundDaemon; connecting: true }
    | { phase: "connected"; found: FoundDaemon; canConnect: false; connecting: false; error: "" }
    | { phase: "refused"; reason: Reason; error: string }
  )

export interface LocalDaemon {
  /** The state now; a new object after every change. */
  readonly state: LocalState
  /**
   * Calls `listener` with the new state after every change; returns what stops it. With `{ immediate: true }` it is also
   * called once at once with the state now, so a screen draws without waiting for a change (since 0.7.0).
   */
  subscribe: (listener: (state: LocalState) => void, options?: { immediate?: boolean }) => () => void
  /** Looks for a daemon (the settings tab opened, or the window got focus). */
  detect: () => Promise<void>
  /**
   * Looks now and then every `everyMs` (default 3000), so a person who installs agentd and comes back to the page is
   * found without a reload, and a daemon that left or a grant that lapsed is noticed (a daemon that does not answer
   * once while connected is given a second look before the page says it left). It looks no more while a connection
   * waits, and while a refusal is on the screen (a look clears it before it is read). A listener hears of a change only.
   * Returns what stops it. Start it when the user shows interest, not at page load.
   */
  watch: (everyMs?: number) => () => void
  /** Asks the user to let the daemon on this computer serve this app. True when it was allowed and now does. */
  connect: () => Promise<boolean>
  /** Takes back what this page was allowed (its browser grant and the app at its origin), then looks again; a failure shows as a refusal. */
  disconnect: () => Promise<void>
  /**
   * Opens `agent` on the daemon that was found (browser mode), on the `WebSocket` of the deps. Rejects with a
   * `SessionError` while no daemon is found (since 0.7.0).
   */
  openSession: (agent: string) => Promise<AgentSession>
}

function defaultClient(deps: LocalCommonDeps): LocalClient {
  const look: DetectOptions = {
    ...(deps.ports === undefined ? {} : { ports: deps.ports }),
    ...(deps.fetch === undefined ? {} : { fetch: deps.fetch }),
    ...(deps.timeoutMs === undefined ? {} : { timeoutMs: deps.timeoutMs }),
  }
  const calls = deps.fetch === undefined ? {} : { fetch: deps.fetch }
  const name = deps.name ?? ""
  return {
    detect: () => detect(look),
    connectServer: (d, a) => connectServer(d, { name, code: a.code }, calls),
    connectBrowser: (d) => connectBrowser(d, { name }, calls),
    disconnect: (d) => disconnect(d, calls),
  }
}

const DEFAULT_INTERVAL: Interval = (tick, everyMs) => {
  const id = setInterval(tick, everyMs)
  return () => clearInterval(id)
}
const WATCH_EVERY_MS = 3000

/** Why a connection did not happen: the sentence, and what the daemon said. */
interface Refusal {
  error: string
  reason: Reason
  message?: string
  retryAfter?: number
}

function refusalOf(reason: Reason, message: string | undefined, retryAfter?: number): Refusal {
  return { error: reasonText(reason), reason, ...(message === undefined ? {} : { message }), ...(retryAfter === undefined ? {} : { retryAfter }) }
}

const messageOf = (e: unknown): string => (e instanceof Error ? e.message : String(e))

/** A call to the daemon that threw: nothing answered it. The exception is the message; the words are those of `unreachable`. */
const unreachable = (e: unknown): Refusal => refusalOf("unreachable", messageOf(e))

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
  // A daemon that answered with its own refusal of this page: the page is told why instead of "not installed".
  const turnedAway = looked && !found.found && found.refused !== undefined && refusal === undefined && !connecting
  const why: Refusal | undefined =
    refusal ??
    (incompatible === undefined ? undefined : { error: incompatible, reason: "incompatible" }) ??
    (turnedAway && !found.found && found.refused !== undefined ? refusalOf(found.refused, found.message) : undefined)
  const state: LocalStateBase = {
    found,
    connecting,
    error: why?.error ?? "",
    canConnect: found.found && !connected && incompatible === undefined,
    phase,
    ...(why === undefined ? {} : { reason: why.reason }),
    ...(why?.message === undefined ? {} : { message: why.message }),
    ...(incompatible === undefined ? {} : { incompatible }),
    ...(why?.retryAfter === undefined ? {} : { retryAfter: why.retryAfter }),
  }
  return state as LocalState
}

/** Asks the daemon to connect to the app's server with a fresh pairing code: undefined when it does, or why not. */
async function askServer(deps: LocalServerDeps, client: LocalClient, d: Daemon): Promise<Refusal | undefined> {
  let code: string
  try {
    code = await deps.startPairing()
  } catch (e) {
    return { error: messageOf(e), reason: "error", message: messageOf(e) } // the app's own call failed: its words are the app's
  }
  if (code === "") return { error: "The app could not start a connection. Try again.", reason: "error" }
  const result = await client.connectServer(d, { code })
  if (result.granted) return undefined
  await Promise.resolve(deps.cancelPairing()).catch(() => undefined)
  return refusalOf(result.reason, result.message, result.retryAfter)
}

/** Asks the daemon to let this page run agents: undefined when it does, or why not. */
async function askBrowser(client: LocalClient, d: Daemon): Promise<Refusal | undefined> {
  const result = await client.connectBrowser(d)
  return result.granted ? undefined : refusalOf(result.reason, result.message, result.retryAfter)
}

/** What a client's answer to `disconnect` says went wrong: a `DisconnectResult` that is not a success; anything else is nothing. */
function disconnectFailure(answer: unknown): Refusal | undefined {
  if (typeof answer !== "object" || answer === null) return undefined
  const { disconnected, reason, message } = answer as { disconnected?: unknown; reason?: unknown; message?: unknown }
  if (disconnected !== false || typeof reason !== "string") return undefined
  return refusalOf(reason, typeof message === "string" ? message : undefined)
}

/**
 * The daemon that may be running on the computer this page is open on: whether it is there, and the connection the user
 * can ask it for. The daemon shows its own dialog; this only asks. Framework-agnostic: wrap `state` and `subscribe` in
 * the framework's own reactivity.
 */
export function createLocalDaemon(deps: LocalDaemonDeps): LocalDaemon {
  if (deps.client === undefined && (deps.name ?? "").trim() === "") {
    throw new Error("createLocalDaemon: name is required: the user is asked to let this app in by its name")
  }
  const client = deps.client ?? defaultClient(deps)
  const interval = deps.interval ?? DEFAULT_INTERVAL
  const mode = deps.mode ?? "server"
  const ask = (d: Daemon): Promise<Refusal | undefined> => (deps.mode === "browser" ? askBrowser(client, d) : askServer(deps, client, d))
  let looked = false // the first look has ended
  let missed = false // a look found no daemon while connected: the next one decides
  let state = stateOf(mode, { found: false }, false, looked, undefined)
  const listeners = new Set<(state: LocalState) => void>()

  const tell = (listener: (state: LocalState) => void): void => {
    try {
      listener(state)
    } catch {
      // One listener's failure is not another's.
    }
  }

  // quiet: a repeated look that finds what is already shown changes nothing and tells nobody.
  const set = (found: Detected, connecting: boolean, refusal: Refusal | undefined, quiet = false): void => {
    const next = stateOf(mode, found, connecting, looked, refusal)
    if (quiet && JSON.stringify(next) === JSON.stringify(state)) return
    state = next
    for (const l of listeners) tell(l)
  }

  // A look that threw found nobody.
  const seen = async (): Promise<Detected> => {
    try {
      return await client.detect()
    } catch {
      return { found: false }
    }
  }

  // Looks again. The dialog of a waiting connection returns the focus that triggers this, so a look while connecting is
  // skipped; the connection looks again itself when it ends. The last refusal described an earlier attempt: a look clears it.
  // While connected, a look that finds nobody is not believed until a second one does: one slow answer is not a departure.
  async function look(quiet = false, doubtAbsence = false): Promise<void> {
    if (state.connecting) return
    const found = await seen()
    if (state.connecting) return // a connection began while this look was in flight: its end looks again itself
    if (doubtAbsence && state.phase === "connected" && !found.found && !missed) {
      missed = true
      return
    }
    missed = false
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
      return end(unreachable(e))
    }
    // The connection ends with what the daemon says now, in one step: a state in between would show 'available'.
    const found = await seen()
    looked = true
    missed = false
    set(found, false, undefined)
    return true
  }

  async function disconnectPage(): Promise<void> {
    const daemon = state.found
    let failure: Refusal | undefined
    if (daemon.found) {
      try {
        failure = disconnectFailure(await client.disconnect(daemon))
      } catch (e) {
        failure = unreachable(e)
      }
    }
    await look()
    if (failure !== undefined) set(state.found, false, failure)
  }

  // A look is out at most once at a time, and a refusal the person has not read yet is left on the screen.
  function watch(everyMs = WATCH_EVERY_MS): () => void {
    let out = false
    const tick = (): void => {
      const held = state.connecting || (state.phase === "refused" && state.incompatible === undefined)
      if (out || held) return
      out = true
      void look(true, true).finally(() => (out = false))
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
    subscribe(listener, options) {
      listeners.add(listener)
      if (options?.immediate === true) tell(listener)
      return () => void listeners.delete(listener)
    },
    detect: () => look(),
    watch,
    connect,
    disconnect: disconnectPage,
    openSession(agent) {
      const daemon = state.found
      if (!daemon.found) {
        return Promise.reject(new SessionError("no agentd was found on this computer: call detect() or watch(), and wait until the phase is not 'detecting'", "unreachable"))
      }
      return openAgentSession(daemon, agent, deps.WebSocket === undefined ? {} : { WebSocket: deps.WebSocket })
    },
  }
}
