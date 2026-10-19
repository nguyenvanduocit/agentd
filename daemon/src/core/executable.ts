import { posix, win32 } from "node:path"

export type Env = Readonly<Record<string, string | undefined>>

function pathApi(platform: NodeJS.Platform) {
  return platform === "win32" ? win32 : posix
}

/** The name of the PATH variable: Windows spells it Path and compares names without case. */
export function pathKey(env: Env, platform: NodeJS.Platform): string {
  if (platform !== "win32") return "PATH"
  return Object.keys(env).find((k) => k.toUpperCase() === "PATH") ?? "Path"
}

export function pathDirs(env: Env, platform: NodeJS.Platform): string[] {
  const raw = env[pathKey(env, platform)] ?? ""
  return raw.split(pathApi(platform).delimiter).filter((d) => d !== "")
}

function nameVariants(name: string, env: Env, platform: NodeJS.Platform): string[] {
  if (platform !== "win32") return [name]
  const exts = (env.PATHEXT ?? ".COM;.EXE;.BAT;.CMD").split(";").filter((e) => e !== "")
  const hasExt = exts.some((e) => name.toUpperCase().endsWith(e.toUpperCase()))
  return hasExt ? [name] : [name, ...exts.map((e) => name + e)]
}

/**
 * Finds `name` the way a shell would, against the PATH in `env`: the first directory that holds an executable
 * file of that name. A name with a directory part is checked as given. `isExecutable` is the only I/O.
 */
export function resolveExecutable(
  name: string,
  env: Env,
  platform: NodeJS.Platform,
  isExecutable: (file: string) => boolean,
): string | undefined {
  const api = pathApi(platform)
  const variants = nameVariants(name, env, platform)
  if (name.includes("/") || (platform === "win32" && name.includes("\\"))) {
    return variants.find(isExecutable)
  }
  for (const dir of pathDirs(env, platform)) {
    const hit = variants.map((v) => api.join(dir, v)).find(isExecutable)
    if (hit) return hit
  }
  return undefined
}
