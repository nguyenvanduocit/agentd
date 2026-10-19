export interface BackoffOptions {
  baseMs: number
  maxMs: number
  /** 0..1: the share of each delay that is random, so daemons that lost one server do not return in lockstep. */
  jitter: number
}

/** The delay before retry number `attempt` (0 is the first retry): exponential, capped, jittered downwards. */
export function backoffDelay(attempt: number, o: BackoffOptions, random: () => number = Math.random): number {
  const exp = Math.min(o.maxMs, o.baseMs * 2 ** Math.min(attempt, 30))
  return Math.round(exp * (1 - o.jitter * random()))
}
