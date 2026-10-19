export interface SpawnPlan {
  file: string
  args: string[]
  shell: boolean
  /** POSIX: the agent leads its own process group, so one signal ends it and everything it started. */
  detached: boolean
}

function winQuote(s: string): string {
  return /[\s"&|<>^()]/.test(s) || s === "" ? `"${s.replaceAll('"', '""')}"` : s
}

/**
 * How to start `file` on `platform`. Windows runs .cmd and .bat (npx.cmd, the wrapper) through the shell, which then
 * needs its arguments quoted; the agent's tree is ended with taskkill, so it is not detached there.
 */
export function spawnPlan(file: string, args: readonly string[], platform: NodeJS.Platform): SpawnPlan {
  if (platform !== "win32") return { file, args: [...args], shell: false, detached: true }
  const viaShell = /\.(cmd|bat)$/i.test(file)
  return viaShell
    ? { file: winQuote(file), args: args.map(winQuote), shell: true, detached: false }
    : { file, args: [...args], shell: false, detached: false }
}

/** The command that ends the process tree rooted at `pid` on Windows (POSIX uses signals on the group). */
export function taskkillArgs(pid: number): string[] {
  return ["/pid", String(pid), "/T", "/F"]
}
