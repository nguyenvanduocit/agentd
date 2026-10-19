/** One TCP connection to the gateway, as the gateway's socket sees it. Both addresses are IPv4 loopback. */
export interface Connection {
  peerAddress: string
  peerPort: number
  gatewayAddress: string
  gatewayPort: number
}

const TIME_WAIT = "06"
const MAPPED_PREFIX = "0000000000000000FFFF0000"

/** An IPv4 address as /proc/net/tcp writes it: the four bytes in host order (little-endian on x86-64 and arm64). */
function procHex(address: string): string | undefined {
  const bytes = address.split(".").map(Number)
  if (bytes.length !== 4 || bytes.some((b) => !Number.isInteger(b) || b < 0 || b > 255)) return undefined
  return bytes
    .toReversed()
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("")
    .toUpperCase()
}

function procEnds(address: string, port: number): string[] {
  const hex = procHex(address)
  if (hex === undefined) return []
  const p = port.toString(16).padStart(4, "0").toUpperCase()
  return [`${hex}:${p}`, `${MAPPED_PREFIX}${hex}:${p}`]
}

const UID = /^\d+$/

/**
 * The uids of the peer's sockets of `c` in the text of /proc/net/tcp or /proc/net/tcp6: the rows whose local end is the
 * peer and whose remote end is the gateway. A TIME_WAIT row belongs to no process (its uid reads 0) and is skipped.
 */
export function procNetOwners(text: string, c: Connection): number[] {
  const locals = procEnds(c.peerAddress, c.peerPort)
  const remotes = procEnds(c.gatewayAddress, c.gatewayPort)
  const owners: number[] = []
  for (const line of text.split("\n")) {
    const f = line.trim().split(/\s+/)
    const [local, remote, state, uid] = [f[1] ?? "", f[2] ?? "", f[3], f[7] ?? ""]
    if (!locals.includes(local) || !remotes.includes(remote) || state === TIME_WAIT || !UID.test(uid)) continue
    owners.push(Number(uid))
  }
  return owners
}

/** The arguments of lsof that list every process holding a TCP socket on the peer's port, in its field output. */
export function lsofArgs(peerPort: number): string[] {
  return ["-nP", "-w", `-iTCP:${peerPort}`, "-Fpun"]
}

/**
 * The uids of the processes holding the peer's end of `c`, from `lsof -F pun` output: a `p` line starts a process, its
 * `u` line is the uid, and each `n` line names one socket as `local->remote`.
 */
export function lsofOwners(text: string, c: Connection): number[] {
  const name = `${c.peerAddress}:${c.peerPort}->${c.gatewayAddress}:${c.gatewayPort}`
  const owners: number[] = []
  let uid: number | undefined
  for (const line of text.split("\n")) {
    const value = line.slice(1)
    if (line.startsWith("p")) uid = undefined
    else if (line.startsWith("u")) uid = UID.test(value) ? Number(value) : undefined
    else if (line.startsWith("n") && value === name && uid !== undefined) owners.push(uid)
  }
  return owners
}

/** A connection is let in only when its owners are known and every one of them is this process's user. */
export function peerAllowed(owners: readonly number[], me: number | undefined): boolean {
  return me !== undefined && owners.length > 0 && owners.every((u) => u === me)
}
