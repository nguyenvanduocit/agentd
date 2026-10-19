import { latestUrl, parseLatest, type Offer } from "./core/update.ts"
import { fetchFollowing } from "./http.ts"
import { warn } from "./log.ts"

export interface ReleaseWatcherOptions {
  /** The releases page the offer is read from (see releaseBase). */
  base: string
  /** How often to ask again after the first time, in ms. */
  intervalMs: number
  timeoutMs?: number
  onOffer: (offer: Offer) => void
  fetchFn?: typeof fetch
}

const DEFAULT_TIMEOUT_MS = 15_000

/**
 * Asks the releases page for latest.json at start and every interval, and hands each answer to `onOffer`. An answer
 * that cannot be had (the host is down, refuses, redirects where it must not, sends something that is not an offer, or does not answer)
 * changes nothing: it is asked again at the next interval, and said once per distinct reason in the log.
 */
export class ReleaseWatcher {
  private timer: NodeJS.Timeout | undefined
  private stopped = false
  private lastProblem = ""

  constructor(private readonly o: ReleaseWatcherOptions) {}

  start(): void {
    void this.check()
    this.timer = setInterval(() => void this.check(), this.o.intervalMs)
    this.timer.unref()
  }

  stop(): void {
    this.stopped = true
    clearInterval(this.timer)
  }

  private async check(): Promise<void> {
    try {
      const res = await fetchFollowing(this.o.fetchFn ?? fetch, latestUrl(this.o.base), AbortSignal.timeout(this.o.timeoutMs ?? DEFAULT_TIMEOUT_MS))
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const offer = parseLatest(await res.text())
      if (offer === undefined) throw new Error("latest.json is not an offer")
      this.lastProblem = ""
      if (!this.stopped) this.o.onOffer(offer)
    } catch (e) {
      const why = e instanceof Error ? e.message : String(e)
      if (why !== this.lastProblem) warn(`cannot read the release offer from ${this.o.base}: ${why}; asking again in ${Math.round(this.o.intervalMs / 1000)} s`)
      this.lastProblem = why
    }
  }
}
