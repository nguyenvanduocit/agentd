import { execFile } from "node:child_process"
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { basename, dirname } from "node:path"
import { promisify } from "node:util"
import { parsePs, parseState, sweep, type PsRow, type State } from "./core/orphans.ts"
import { say } from "./log.ts"
import { signalGroup } from "./procgroup.ts"

const run = promisify(execFile)

/**
 * The directory installWrapper (daemon.ts) made for `bin`: <tmpdir>/agentd-XXXXXX/bin. The state file can
 * name any path; nothing else is removed.
 */
function wrapperDir(bin: string): string | undefined {
  const dir = dirname(bin)
  const own = basename(bin) === "bin" && dirname(dir) === tmpdir() && /^agentd-[0-9A-Za-z]{6}$/.test(basename(dir))
  return own ? dir : undefined
}

async function psTable(): Promise<PsRow[]> {
  try {
    const { stdout } = await run("ps", ["-axo", "pid=,pgid=,lstart="], { env: { ...process.env, LC_ALL: "C" } })
    return parsePs(stdout)
  } catch {
    return []
  }
}

/**
 * The record of the process groups of live agents, kept in a file. SIGKILL gives the daemon no chance to end its
 * agents; the next start reads the file and ends the groups that are still running. Windows has no process groups
 * to list, so the record is not kept there.
 */
export class SessionState {
  private readonly enabled = process.platform !== "win32"
  private state: State

  constructor(
    private readonly file: string,
    bin: string,
  ) {
    this.state = { daemon: { pid: process.pid, lstart: "" }, bin, groups: [] }
  }

  /** Ends what a dead predecessor left, then records this daemon. */
  async start(): Promise<void> {
    if (!this.enabled) return
    const table = await psTable()
    const prev = this.read()
    if (prev) this.reap(prev, table)
    const self = table.find((r) => r.pid === process.pid)
    this.state.daemon.lstart = self?.lstart ?? ""
    this.save()
  }

  private read(): State | undefined {
    try {
      return parseState(readFileSync(this.file, "utf8"))
    } catch {
      return undefined
    }
  }

  private reap(prev: State, table: PsRow[]): void {
    const result = sweep(prev, table, process.pid)
    if (result.predecessorAlive) return
    for (const g of result.kill) signalGroup(g.pgid, true)
    const wrapper = wrapperDir(prev.bin)
    if (wrapper !== undefined) rmSync(wrapper, { recursive: true, force: true })
    if (result.kill.length > 0) {
      const procs = result.kill.reduce((n, g) => n + g.members, 0)
      say(`reaped ${result.kill.length} orphan group(s), ${procs} process(es) from a previous daemon`)
    }
  }

  /** Records a new agent group. The leader's start time follows once `ps` has answered. */
  add(pgid: number): void {
    if (!this.enabled) return
    this.state.groups.push({ pgid, lstart: "" })
    this.save()
    void psTable().then((table) => {
      const row = table.find((r) => r.pid === pgid)
      const g = this.state.groups.find((x) => x.pgid === pgid)
      if (row && g) {
        g.lstart = row.lstart
        this.save()
      }
    })
  }

  remove(pgid: number): void {
    if (!this.enabled) return
    this.state.groups = this.state.groups.filter((g) => g.pgid !== pgid)
    this.save()
  }

  /** A clean exit: nothing is left to reap. */
  clear(): void {
    if (this.enabled) rmSync(this.file, { force: true })
  }

  private save(): void {
    try {
      mkdirSync(dirname(this.file), { recursive: true, mode: 0o700 })
      const tmp = `${this.file}.tmp`
      writeFileSync(tmp, JSON.stringify(this.state), { mode: 0o600 })
      renameSync(tmp, this.file)
    } catch {
      /* the record is best effort: a full disk must not stop an agent from starting */
    }
  }
}
