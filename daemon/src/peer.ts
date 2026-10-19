import { readFile } from "node:fs/promises"
import type { Socket } from "node:net"
import { lsofArgs, lsofOwners, peerAllowed, procNetOwners, type Connection } from "./core/peer.ts"
import { warn } from "./log.ts"
import { exec } from "./proc.ts"

/** The uids of the processes behind the peer's end of a connection. Rejects when the lookup itself cannot run. */
export type PeerOwners = (c: Connection) => Promise<number[]>

const LSOF = "/usr/sbin/lsof"

/** macOS: lsof lists the processes holding a socket. It exits 1 and says nothing when no socket matches. */
async function lsofLookup(c: Connection): Promise<number[]> {
  const r = await exec(LSOF, lsofArgs(c.peerPort))
  if (r.code === 0 || (r.code === 1 && r.stderr === "")) return lsofOwners(r.stdout, c)
  throw new Error(`${LSOF} exited ${r.code}: ${r.stderr.trim()}`)
}

/** /proc/net/tcp6 is missing on a kernel without IPv6. */
function readProc(file: string): Promise<string> {
  return readFile(file, "utf8").catch((e: unknown) => {
    if ((e as NodeJS.ErrnoException).code === "ENOENT" && file.endsWith("6")) return ""
    throw e
  })
}

/** Linux: the kernel's socket tables carry the uid of each socket. */
async function procLookup(c: Connection): Promise<number[]> {
  const [v4, v6] = await Promise.all([readProc("/proc/net/tcp"), readProc("/proc/net/tcp6")])
  return procNetOwners(`${v4}\n${v6}`, c)
}

export const systemOwners: PeerOwners = process.platform === "linux" ? procLookup : lsofLookup

function connectionOf(socket: Socket): Connection | undefined {
  const { remoteAddress, remotePort, localAddress, localPort } = socket
  if (remoteAddress === undefined || remotePort === undefined || localAddress === undefined || localPort === undefined) return undefined
  return { peerAddress: remoteAddress, peerPort: remotePort, gatewayAddress: localAddress, gatewayPort: localPort }
}

/**
 * Whether a connection comes from a process of this OS user, asked once per connection (keep-alive requests and the
 * upgrade that follows share the answer). An owner that cannot be found is a no; a lookup that fails is a no that is
 * logged once, so a broken lookup shows in the log rather than only as refused pages.
 */
export function peerGate(owners: PeerOwners = systemOwners, me: number | undefined = process.getuid?.()): (socket: Socket) => Promise<boolean> {
  const answers = new WeakMap<Socket, Promise<boolean>>()
  let warned = false
  const decide = async (socket: Socket): Promise<boolean> => {
    const c = connectionOf(socket)
    if (c === undefined) return false
    try {
      return peerAllowed(await owners(c), me)
    } catch (e) {
      if (!warned) warn(`local gateway: cannot tell which user a connection comes from, refusing it: ${e instanceof Error ? e.message : String(e)}`)
      warned = true
      return false
    }
  }
  return (socket) => {
    const known = answers.get(socket)
    if (known !== undefined) return known
    const answer = decide(socket)
    answers.set(socket, answer)
    return answer
  }
}
