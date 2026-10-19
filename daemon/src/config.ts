import { chmod, mkdir, readFile, rename, writeFile } from "node:fs/promises"
import { dirname } from "node:path"
import { configFile, type Host } from "./core/paths.ts"
import { emptyRegistry, parseRegistry, type Registry } from "./core/registry.ts"

export interface ConfigFlags {
  config: string | undefined
}

export function registryFile(flags: ConfigFlags, host: Host): string {
  return flags.config ?? configFile(host)
}

let writes = 0

/** Written through a temporary file and a rename: the daemon and `pair` both write it, and a reader never sees half of it. */
export async function saveRegistry(file: string, registry: Registry): Promise<void> {
  await mkdir(dirname(file), { recursive: true, mode: 0o700 })
  const tmp = `${file}.${process.pid}.${++writes}.tmp`
  await writeFile(tmp, `${JSON.stringify(registry, null, 2)}\n`, { mode: 0o600 })
  await chmod(tmp, 0o600)
  await rename(tmp, file)
}

/** The apps and browser grants saved in `file`. A missing file is an empty registry: a daemon with nothing paired waits for an app to ask. */
export async function loadRegistry(file: string): Promise<Registry> {
  let text: string
  try {
    text = await readFile(file, "utf8")
  } catch {
    return emptyRegistry()
  }
  try {
    return parseRegistry(text)
  } catch (e) {
    throw new Error(`${file} ${e instanceof Error ? e.message : String(e)}`, { cause: e })
  }
}

// One change at a time per file within this process: the gateway, the tray and `pair` all change the registry, and two
// read-modify-write cycles at once would lose one of them.
const queues = new Map<string, Promise<unknown>>()

/** Loads the registry, applies `change` and saves the result, after every earlier change of `file` has finished. Resolves to what was saved. */
export function updateRegistry(file: string, change: (r: Registry) => Registry): Promise<Registry> {
  const run = (queues.get(file) ?? Promise.resolve()).then(async () => {
    const next = change(await loadRegistry(file))
    await saveRegistry(file, next)
    return next
  })
  queues.set(file, run.catch(() => undefined))
  return run
}
