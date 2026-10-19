import { copyFileSync, fstatSync, renameSync, rmSync, statSync, truncateSync, writeSync } from "node:fs"
import { needsRotation, rotateSteps } from "./core/rotate.ts"
import { killTrackedGroups } from "./procgroup.ts"

let stamped = false

/** From now on every line starts with a UTC timestamp: what a log file needs, and a terminal does not. */
export function stampLines(): void {
  stamped = true
}

function line(message: string): string {
  return stamped ? `${new Date().toISOString()} ${message}\n` : `${message}\n`
}

/** The daemon's whole output: status lines on stdout, problems on stderr. */
export function say(message: string): void {
  process.stdout.write(line(message))
}

export function warn(message: string): void {
  process.stderr.write(line(message))
}

/** A message for the log of a process that is about to exit: written synchronously, so exit cannot cut it off. */
export function fatal(message: string): void {
  try {
    writeSync(2, line(message))
  } catch {
    /* stderr is gone: nothing else to tell */
  }
}

function describe(reason: unknown): string {
  if (reason instanceof Error) return reason.stack ?? reason.message
  return String(reason)
}

/**
 * An exception nobody caught leaves the daemon in an unknown state: log it and exit 1, so the service manager (or
 * the launcher's loop) starts a fresh process. The same for a promise that rejects with nobody listening. An adapter
 * install running in the background goes down with the daemon.
 */
export function exitOnFatal(): void {
  process.on("uncaughtException", (e) => {
    fatal(`fatal: uncaught exception: ${describe(e)}`)
    killTrackedGroups()
    process.exit(1)
  })
  process.on("unhandledRejection", (e) => {
    fatal(`fatal: unhandled rejection: ${describe(e)}`)
    killTrackedGroups()
    process.exit(1)
  })
}

export const LOG_MAX_BYTES = 5 * 1024 * 1024
export const LOG_KEEP = 3

/** Rotates `file` when it is larger than `maxBytes`; returns whether it did. A missing file is not an error. */
export function rotateLog(file: string, maxBytes = LOG_MAX_BYTES, keep = LOG_KEEP): boolean {
  let size: number
  try {
    size = statSync(file).size
  } catch {
    return false
  }
  if (!needsRotation(size, maxBytes)) return false
  for (const step of rotateSteps(file, keep)) {
    try {
      if (step.op === "remove") rmSync(step.file, { force: true })
      else if (step.op === "rename") renameSync(step.from, step.to)
      else if (step.op === "copy") copyFileSync(step.from, step.to)
      else truncateSync(step.file)
    } catch {
      /* a missing older file is the normal case; a log must never stop the daemon */
    }
  }
  return true
}

/** Whether this process writes its stdout into `file` (a service manager or the launcher redirected it there). */
export function stdoutIs(file: string): boolean {
  try {
    const out = fstatSync(1)
    const log = statSync(file)
    return out.ino === log.ino && out.dev === log.dev
  } catch {
    return false
  }
}
