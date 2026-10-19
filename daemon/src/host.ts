import { accessSync, constants, statSync } from "node:fs"
import { homedir } from "node:os"
import type { Env } from "./core/executable.ts"
import { resolveExecutable } from "./core/executable.ts"
import type { Host } from "./core/paths.ts"

export function currentHost(): Host {
  return { platform: process.platform, env: process.env, home: homedir() }
}

function isExecutableFile(file: string): boolean {
  try {
    if (!statSync(file).isFile()) return false
    if (process.platform !== "win32") accessSync(file, constants.X_OK)
    return true
  } catch {
    return false
  }
}

/** Finds a program on the PATH of `env`, as a shell would. */
export function findProgram(name: string, env: Env): string | undefined {
  return resolveExecutable(name, env, process.platform, isExecutableFile)
}
