import { isRedirect, redirectTarget } from "./core/update.ts"

/** How many redirects one request follows: GitHub's release assets take two (the tag, then the storage host). */
export const MAX_REDIRECTS = 5

/**
 * GET `url`, following up to MAX_REDIRECTS redirects that `redirectTarget` allows. A redirect it refuses, or a chain that
 * does not end, throws. The last response is returned whatever its status.
 */
export async function fetchFollowing(fetchFn: typeof fetch, url: string, signal: AbortSignal): Promise<Response> {
  let at = url
  for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
    const res = await fetchFn(at, { redirect: "manual", signal })
    if (!isRedirect(res.status)) return res
    await res.body?.cancel()
    at = redirectTarget(at, res.headers.get("location"))
  }
  throw new Error(`more than ${MAX_REDIRECTS} redirects from ${url}`)
}
