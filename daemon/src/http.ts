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

const tooLarge = (max: number) => new Error(`the body is larger than ${max} bytes`)

/**
 * The body of `res`, refused once it is larger than `max` bytes: a Content-Length over it before a byte is read, and
 * any body as soon as it passes it, so at most `max` bytes (and one chunk) are ever held.
 */
export async function readCapped(res: Response, max: number): Promise<Buffer> {
  if (Number(res.headers.get("content-length") ?? 0) > max) {
    await res.body?.cancel()
    throw tooLarge(max)
  }
  if (res.body === null) return Buffer.alloc(0)
  const chunks: Uint8Array[] = []
  let size = 0
  // Leaving the loop by the throw cancels the stream: nothing more is downloaded.
  for await (const chunk of res.body as AsyncIterable<Uint8Array>) {
    size += chunk.length
    if (size > max) throw tooLarge(max)
    chunks.push(chunk)
  }
  return Buffer.concat(chunks)
}
