// scripts/build-daemon.sh sets AGENTD_VERSION, AGENTD_RELEASE_KEY and AGENTD_RELEASE_URL at bundle time (bun build --define); a
// source run reports "dev", has no release key (so it refuses every update) and no release server.
declare const AGENTD_VERSION: string | undefined
declare const AGENTD_RELEASE_KEY: string | undefined
declare const AGENTD_RELEASE_URL: string | undefined

export const VERSION: string = typeof AGENTD_VERSION === "string" ? AGENTD_VERSION : "dev"

/** The release public key (base64 of its SPKI DER) an update must be signed with. */
export const RELEASE_KEY: string | undefined = typeof AGENTD_RELEASE_KEY === "string" ? AGENTD_RELEASE_KEY : undefined

/** The release server (its origin) the daemon takes updates from, unless $AGENTD_RELEASE_URL says another. */
export const RELEASE_URL: string | undefined = typeof AGENTD_RELEASE_URL === "string" ? AGENTD_RELEASE_URL : undefined

/** The version of the daemon contract this daemon speaks: an app's `hello` reply must name the same. */
export const PROTOCOL = 1
