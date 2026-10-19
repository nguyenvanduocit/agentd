import { dirname, join } from "node:path"

/** What the user chose in the tray's settings window. */
export interface Settings {
  /** Install the versions the release server offers by itself. */
  updates: boolean
}

export const DEFAULT_SETTINGS: Settings = { updates: true }

/** settings.json sits beside the registry file: `--config` moves both, so a test daemon never reads the user's. */
export function settingsFile(registryFile: string): string {
  return join(dirname(registryFile), "settings.json")
}

/** The settings saved in `text`; one that is missing or not of its type keeps its default. Text that is not JSON throws. */
export function parseSettings(text: string): Settings {
  const v: unknown = JSON.parse(text)
  const saved = typeof v === "object" && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  return { updates: typeof saved.updates === "boolean" ? saved.updates : DEFAULT_SETTINGS.updates }
}
