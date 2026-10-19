export type RotateStep =
  | { op: "remove"; file: string }
  | { op: "rename"; from: string; to: string }
  | { op: "copy"; from: string; to: string }
  | { op: "truncate"; file: string }

/**
 * The steps that rotate `file` and keep `keep` files in all (daemon.log, daemon.log.1, daemon.log.2 for 3). The
 * active file is copied and truncated in place, not renamed: a service manager that holds it open for the daemon's
 * stdout keeps writing to the same name.
 */
export function rotateSteps(file: string, keep: number): RotateStep[] {
  const older = keep - 1
  if (older < 1) return [{ op: "truncate", file }]
  const steps: RotateStep[] = [{ op: "remove", file: `${file}.${older}` }]
  for (let i = older - 1; i >= 1; i--) steps.push({ op: "rename", from: `${file}.${i}`, to: `${file}.${i + 1}` })
  steps.push({ op: "copy", from: file, to: `${file}.1` }, { op: "truncate", file })
  return steps
}

export function needsRotation(size: number, maxBytes: number): boolean {
  return size > maxBytes
}
