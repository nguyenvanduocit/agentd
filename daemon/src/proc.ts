import { execFile } from "node:child_process"

export interface Exited {
  code: number
  stdout: string
  stderr: string
}

/** Runs a program to the end. It never rejects: a program that is missing or fails is an exit code (127 for missing). */
export function exec(file: string, args: string[], env?: NodeJS.ProcessEnv): Promise<Exited> {
  return new Promise((resolve) => {
    execFile(file, args, { env: env ?? process.env, encoding: "utf8" }, (err, stdout, stderr) => {
      if (!err) {
        resolve({ code: 0, stdout, stderr })
        return
      }
      const code = (err as NodeJS.ErrnoException).code
      resolve({ code: typeof code === "number" ? code : 127, stdout, stderr: stderr === "" ? err.message : stderr })
    })
  })
}

export const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms))
