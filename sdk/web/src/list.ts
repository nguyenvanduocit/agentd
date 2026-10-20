import type { AgentInfo } from "./client.js"

// The types of what an app's server answers to its page when it returns sdk/goserver's `List` (Go: `goserver.List`,
// `Info`, `Pairing`). The SDK does not fetch it, because the route is the app's own; it types the JSON so that a page
// does not write the shape again. list.test.ts compares these fields with the Go struct tags.

/** Whether a daemon is connected to the app's server now. A newer server may say more than these two. */
export type DaemonState = "online" | "offline" | (string & {})

/** One daemon the user paired with the app. */
export interface DaemonInfo {
  id: string
  /** What the user calls it. */
  name: string
  hostname: string
  state: DaemonState
  /** The agentd version it runs; "" until it has connected once. */
  version: string
  os: string
  /** The agents it can start, in the order to show them. */
  agents: AgentInfo[]
  /** Unix milliseconds of the last time it was seen connected. */
  lastSeen: number
  /** Agent sessions running on it now. */
  sessions: number
  /** True when a service restarts it (it was not started by hand). */
  managed: boolean
  /** A sentence for a person when the daemon connected but it and this app speak protocols that do not meet; it is then `offline`. Absent otherwise. */
  incompatible?: string
}

/** The one pending pairing code of the user; `code` is "" when there is none. */
export interface Pairing {
  code: string
  /** Unix milliseconds. */
  expiresAt: number
}

/** The user's daemons as an app's page shows them. */
export interface DaemonList {
  daemons: DaemonInfo[]
  pairing: Pairing
  /** The newest agentd release the app knows of; "" when it does not say. A daemon is behind when its `version` is older. */
  latestVersion: string
}
