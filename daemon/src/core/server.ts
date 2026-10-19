const SECURE: Record<string, string> = { "https:": "https:", "wss:": "https:" }
const PLAINTEXT: Record<string, string> = { "http:": "http:", "ws:": "http:" }
// Only a name that cannot leave this machine may be reached without TLS (a development server, an end-to-end test).
const LOOPBACK = /^(localhost|127(\.\d{1,3}){3}|\[::1\])$/i

/**
 * The server's origin as an http(s) URL without a trailing slash. Accepts https and wss for any host, and http and ws
 * for a loopback host only: anywhere else they would send the auth token over a plaintext connection.
 * A path, query or fragment is dropped because the daemon talks to fixed paths.
 */
export function serverOrigin(raw: string): string {
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    throw new Error(`"${raw}" is not a URL: use the server address, for example https://app.example.com`)
  }
  const secure = SECURE[url.protocol]
  if (secure !== undefined) return `${secure}//${url.host}`
  const plaintext = PLAINTEXT[url.protocol]
  if (plaintext === undefined) throw new Error(`"${raw}": the server address must start with https://`)
  if (!LOOPBACK.test(url.hostname)) {
    throw new Error(`"${raw}": the server address must use https:// — the auth token must not travel over a plaintext connection`)
  }
  return `${plaintext}//${url.host}`
}

/** The Socket.IO namespace daemons and their MCP bridges connect to. */
export function namespaceUrl(origin: string): string {
  return `${origin}/agentd`
}

export function pairUrl(origin: string): string {
  return `${origin}/agentd/pair`
}
