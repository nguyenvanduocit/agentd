import { mkdir, readFile, rename, writeFile } from "node:fs/promises"
import { dirname } from "node:path"
import { DEFAULT_SETTINGS, parseSettings, type Settings } from "./core/settings.ts"
import { warn } from "./log.ts"

async function load(file: string): Promise<Settings> {
  let text: string
  try {
    text = await readFile(file, "utf8")
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") warn(`cannot read ${file}: ${e instanceof Error ? e.message : String(e)}; using the default settings`)
    return DEFAULT_SETTINGS
  }
  try {
    return parseSettings(text)
  } catch (e) {
    warn(`${file} is not JSON (${e instanceof Error ? e.message : String(e)}); using the default settings`)
    return DEFAULT_SETTINGS
  }
}

let writes = 0

/** Written through a temporary file and a rename, as the registry is: a reader never sees half of it. */
async function save(file: string, settings: Settings): Promise<void> {
  await mkdir(dirname(file), { recursive: true, mode: 0o700 })
  const tmp = `${file}.${process.pid}.${++writes}.tmp`
  await writeFile(tmp, `${JSON.stringify(settings, null, 2)}\n`, { mode: 0o600 })
  await rename(tmp, file)
}

/**
 * The settings of this daemon: read once at start (a missing or broken file is the defaults, which a broken one says in
 * the log) and saved at every change, one change at a time, so the file ends with the last one made.
 */
export class SettingsStore {
  private saving: Promise<unknown> = Promise.resolve()

  private constructor(
    private readonly file: string,
    private current: Settings,
  ) {}

  static async open(file: string): Promise<SettingsStore> {
    return new SettingsStore(file, await load(file))
  }

  get(): Settings {
    return this.current
  }

  /** Saves the settings with `patch` applied, after every earlier change; they hold in memory once saved. A failed save rejects and changes nothing. */
  change(patch: Partial<Settings>): Promise<Settings> {
    const run = this.saving.then(async () => {
      const next = { ...this.current, ...patch }
      await save(this.file, next)
      this.current = next
      return next
    })
    this.saving = run.catch(() => undefined)
    return run
  }
}
