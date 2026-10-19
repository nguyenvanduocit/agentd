import { connectServer, detect, reasonText, type ConnectResult, type Daemon, type Detected } from "./client.js"

export interface LocalClient {
  detect: () => Promise<Detected>
  connectServer: (d: Daemon, a: { code: string }) => Promise<ConnectResult>
}

export interface LocalDaemonDeps {
  /** Starts a pairing on the app's server and answers its code; "" when the server gave none. */
  startPairing: () => Promise<string>
  cancelPairing: () => Promise<unknown>
  /** The calls to the daemon (default: this package's own). */
  client?: LocalClient
}

export interface LocalState {
  found: Detected
  /** A connection is waiting for the user's answer in the daemon's dialog. */
  connecting: boolean
  /** Why the last connection did not happen, in plain words; "" when it did or none was tried. */
  error: string
  /** A daemon is here and has not been allowed to serve this app yet. */
  canConnect: boolean
}

export interface LocalDaemon {
  /** The state now; a new object after every change. */
  readonly state: LocalState
  /** Calls `listener` with the new state after every change; returns what stops it. */
  subscribe: (listener: (state: LocalState) => void) => () => void
  /** Looks for a daemon (the settings tab opened, or the window got focus). */
  detect: () => Promise<void>
  /** Asks the user to let the daemon on this computer serve this app. True when it was allowed and now does. */
  connect: () => Promise<boolean>
}

const DEFAULT_CLIENT: LocalClient = { detect: () => detect(), connectServer }

function stateOf(found: Detected, connecting: boolean, error: string): LocalState {
  return { found, connecting, error, canConnect: found.found && !found.granted.server }
}

/**
 * The daemon that may be running on the computer this page is open on: whether it is there, and the connection the user
 * can ask it for. The daemon shows its own dialog; this only asks. Framework-agnostic: wrap `state` and `subscribe` in
 * the framework's own reactivity.
 */
export function createLocalDaemon({ startPairing, cancelPairing, client = DEFAULT_CLIENT }: LocalDaemonDeps): LocalDaemon {
  let state = stateOf({ found: false }, false, "")
  const listeners = new Set<(state: LocalState) => void>()

  const set = (found: Detected, connecting: boolean, error: string): void => {
    state = stateOf(found, connecting, error)
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
  async function look(): Promise<void> {
    if (state.connecting) return
    const found = await client.detect()
    if (state.connecting) return // a connection began while this look was in flight: its end looks again itself
    set(found, false, "")
  }

  async function connect(): Promise<boolean> {
    const daemon = state.found
    if (!daemon.found || state.connecting) return false
    set(daemon, true, "")
    const end = (error: string): false => {
      set(state.found, false, error)
      return false
    }
    try {
      const code = await startPairing()
      if (code === "") return end("The app could not start a connection. Try again.")
      const result = await client.connectServer(daemon, { code })
      if (!result.granted) {
        await cancelPairing()
        return end(reasonText(result.reason))
      }
    } catch (e) {
      return end(e instanceof Error ? e.message : String(e))
    }
    set(state.found, false, "")
    await look()
    return true
  }

  return {
    get state() {
      return state
    },
    subscribe(listener) {
      listeners.add(listener)
      return () => void listeners.delete(listener)
    },
    detect: look,
    connect,
  }
}
