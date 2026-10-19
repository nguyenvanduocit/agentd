export interface PsRow {
  pid: number
  pgid: number
  lstart: string
}

export interface StateGroup {
  pgid: number
  /** Start time of the group's leader (`ps -o lstart=`); "" until it has been read. */
  lstart: string
}

export interface State {
  daemon: { pid: number; lstart: string }
  /** The directory that holds the agentd-mcp wrapper; the next start removes it. */
  bin: string
  groups: StateGroup[]
}

/** Parses `ps -axo pid=,pgid=,lstart=`. */
export function parsePs(output: string): PsRow[] {
  const rows: PsRow[] = []
  for (const line of output.split("\n")) {
    const f = line.trim().split(/\s+/)
    const pid = Number(f[0])
    const pgid = Number(f[1])
    if (f.length < 3 || !Number.isInteger(pid) || !Number.isInteger(pgid)) continue
    rows.push({ pid, pgid, lstart: f.slice(2).join(" ") })
  }
  return rows
}

function isGroup(g: unknown): g is StateGroup {
  const r = g as Partial<StateGroup> | null
  return typeof r?.pgid === "number" && Number.isInteger(r.pgid) && r.pgid > 1 && typeof r.lstart === "string"
}

/** Reads the state file; anything that is not a valid record is "no state". */
export function parseState(text: string): State | undefined {
  let raw: Partial<State>
  try {
    raw = JSON.parse(text) as Partial<State>
  } catch {
    return undefined
  }
  const d = raw.daemon
  if (typeof d?.pid !== "number" || typeof d.lstart !== "string" || !Array.isArray(raw.groups)) return undefined
  return { daemon: d, bin: typeof raw.bin === "string" ? raw.bin : "", groups: raw.groups.filter(isGroup) }
}

export interface Sweep {
  /** The daemon that wrote the state is still running: its groups are not orphans. */
  predecessorAlive: boolean
  /** Groups to end, with the number of processes found in each. */
  kill: { pgid: number; members: number }[]
}

/**
 * Decides which recorded groups a dead daemon left behind. A group is an orphan when it still has members and either
 * its leader is gone (POSIX does not recycle a pid while a group of that id exists) or the leader's start time equals
 * the recorded one (a different start time means the pid was recycled by another process). A leader whose start time
 * was never recorded is left alone.
 */
export function sweep(prev: State, table: readonly PsRow[], selfPid: number): Sweep {
  const pd = table.find((r) => r.pid === prev.daemon.pid)
  const predecessorAlive =
    prev.daemon.pid !== selfPid && pd !== undefined && (prev.daemon.lstart === "" || pd.lstart === prev.daemon.lstart)
  if (predecessorAlive) return { predecessorAlive, kill: [] }
  const kill: Sweep["kill"] = []
  for (const g of prev.groups) {
    const members = table.filter((r) => r.pgid === g.pgid).length
    if (members === 0) continue
    const leader = table.find((r) => r.pid === g.pgid)
    if (leader === undefined || (g.lstart !== "" && leader.lstart === g.lstart)) kill.push({ pgid: g.pgid, members })
  }
  return { predecessorAlive, kill }
}
