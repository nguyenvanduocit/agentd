import type { BackoffOptions } from "./backoff.ts"
import { decodeSignature } from "./signature.ts"
import { serverOrigin } from "./server.ts"

/** What `hello` answers: the version the server serves, where to fetch it, and the release key's signature of both. */
export interface Offer {
  version: string
  url: string
  sha256: string
  signature: string
}

/**
 * A version names a directory and is baked into the tarball's name: nothing else may reach a path. current, previous,
 * rejected and crashes name the daemon directory's own entries (in any case: macOS file names ignore it), so they are
 * not versions; launcher.sh's plain_version says the same.
 */
export const VERSION_PATTERN = /^(?!(?:current|previous|rejected|crashes)$)[0-9a-z][0-9a-z.-]{0,63}$/i
const SHA256_PATTERN = /^[0-9a-f]{64}$/
const RELEASE_PATTERN = /^(\d+)\.(\d+)\.(\d+)$/

/** True when both are plain x.y.z releases and `offered` is the lower one. Anything else (a dev build) is not ordered. */
function isOlder(offered: string, running: string): boolean {
  const a = RELEASE_PATTERN.exec(offered)
  const b = RELEASE_PATTERN.exec(running)
  if (!a || !b) return false
  for (let i = 1; i <= 3; i++) if (Number(a[i]) !== Number(b[i])) return Number(a[i]) < Number(b[i])
  return false
}

/** A failed update is retried from 30 s, doubling, up to 30 min. */
export const RETRY: BackoffOptions = { baseMs: 30_000, maxMs: 30 * 60_000, jitter: 0.2 }

/**
 * The path of a version's tarball under the releases base, as GitHub Releases lays it out
 * (https://github.com/<owner>/<repo>/releases/download/v<version>/agentd-<version>.tgz) and scripts/build-daemon.sh names it.
 */
export function tarballPath(version: string): string {
  return `/download/v${version}/agentd-${version}.tgz`
}

/** Where the offer a daemon checks is published: the latest release's latest.json asset. */
export function latestUrl(base: string): string {
  return `${base}/latest/download/latest.json`
}

/** latest.json as an offer, or undefined when it is not four strings. Whether the values are sound is `decide`'s to say. */
export function parseLatest(text: string): Offer | undefined {
  let v: Partial<Record<keyof Offer, unknown>> | null
  try {
    v = JSON.parse(text) as typeof v
  } catch {
    return undefined
  }
  if (v === null || typeof v !== "object" || Array.isArray(v)) return undefined
  const { version, url, sha256, signature } = v
  if (typeof version !== "string" || typeof url !== "string" || typeof sha256 !== "string" || typeof signature !== "string") return undefined
  return { version, url, sha256, signature }
}

/**
 * The releases page the daemon takes updates from: $AGENTD_RELEASE_URL when it is set (empty switches updates off), else
 * the one baked into the bundle at build time. The scheme rule is the server address's (https, or http on a loopback
 * host); the path of the page stays (https://github.com/<owner>/<repo>/releases), a query and a fragment are dropped,
 * and a path segment that is not a plain name is refused.
 */
export function releaseBase(env: Readonly<Record<string, string | undefined>>, baked: string | undefined): string | undefined {
  const raw = env.AGENTD_RELEASE_URL ?? baked
  if (raw === undefined || raw === "") return undefined
  const origin = serverOrigin(raw)
  // The raw path: URL parsing would resolve a ".." before it could be refused.
  const rawPath = (raw.replace(/^[A-Za-z][A-Za-z0-9+.-]*:\/\/[^/?#]*/, "").split(/[?#]/)[0] ?? "").replace(/%2e/gi, ".")
  const segments = rawPath.split("/").filter((x) => x !== "")
  if (!segments.every((x) => /^[A-Za-z0-9._-]+$/.test(x) && x !== "." && x !== "..")) throw new Error(`"${raw}": the path of the releases page has a segment that is not a plain name`)
  return segments.length === 0 ? origin : `${origin}/${segments.join("/")}`
}

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308])
export const isRedirect = (status: number): boolean => REDIRECT_STATUSES.has(status)

/**
 * Where a redirect from `from` goes, or a thrown error when the daemon must not follow it. GitHub sends a release asset
 * to another host (an https URL), so any https target is followed: what the daemon installs is checked against the signed
 * sha256, not against the host. Plain http is followed only inside the same origin (a development server).
 */
export function redirectTarget(from: string, location: string | null): string {
  if (location === null || location === "") throw new Error("the redirect has no Location")
  let to: URL
  try {
    to = new URL(location, from)
  } catch {
    throw new Error(`the redirect goes to "${location}", which is not a URL`)
  }
  if (to.protocol === "https:" || (to.protocol === "http:" && to.origin === new URL(from).origin)) return to.href
  throw new Error(`the redirect goes to ${to.protocol}//${to.host}, which is not https`)
}

export interface UpdateState {
  /** The version this process runs. */
  running: string
  /** A newer version already unpacked and pointed at by `current`, waiting for the daemon to be idle. */
  staged: string | undefined
  /** A version that crashed at start twice (the launcher wrote it); undefined when there is none. */
  rejected: string | undefined
}

export type Decision =
  | { kind: "none" }
  /** The server went back to the running version: `current` must point at it again. */
  | { kind: "unstage" }
  | { kind: "waiting" }
  | { kind: "rejected"; version: string }
  | { kind: "invalid"; why: string }
  | { kind: "update"; offer: Offer }

/** Why an offer cannot be installed, or undefined when its shape is sound. */
function offerProblem(running: string, reply: Offer): string | undefined {
  if (!VERSION_PATTERN.test(reply.version)) return `version "${reply.version}" is not a plain version`
  if (isOlder(reply.version, running)) return `version ${reply.version} is older than the running ${running}`
  if (reply.url !== tarballPath(reply.version)) return `url "${reply.url}" is not the tarball of ${reply.version}`
  if (!SHA256_PATTERN.test(reply.sha256)) return "sha256 is not 64 lower-case hex digits"
  if (!decodeSignature(reply.signature)) return "the signature is missing or is not 64 bytes of base64"
  return undefined
}

/**
 * What to do with the server's answer to hello. An empty version means the server has nothing to update to. The
 * server's url, sha256 and signature are checked against the shape build-daemon.sh produces, so a reply cannot name
 * another host, another path or a directory outside ~/.agentd/daemon. Whether the signature is the release key's
 * is checked by the updater (signatureProblem) before anything is downloaded. A release older than the running one is
 * refused, so a replayed old signed manifest cannot roll the daemon back.
 */
export function decide(s: UpdateState, reply: Offer): Decision {
  if (reply.version === "") return { kind: "none" }
  if (reply.version === s.running) return s.staged === undefined ? { kind: "none" } : { kind: "unstage" }
  if (reply.version === s.staged) return { kind: "waiting" }
  if (reply.version === s.rejected) return { kind: "rejected", version: reply.version }
  const why = offerProblem(s.running, reply)
  if (why !== undefined) return { kind: "invalid", why }
  return { kind: "update", offer: reply }
}

/** The one line of a rollback record (`previous`, `rejected`): a version, or undefined when it is not a plain one. */
export function parseVersionRecord(text: string): string | undefined {
  const v = text.trim()
  return VERSION_PATTERN.test(v) ? v : undefined
}
