import { describe, expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import ts from "typescript"

// Every ```ts block of the README is compiled, strictly, against this package's own sources, as an app would compile it
// against the package: what the README shows cannot name something that does not exist or call it the wrong way.
// A block is a whole module (its imports are real); the names the reader supplies from their own app are declared below,
// and the README says so where it uses them.
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const readme = readFileSync(resolve(root, "README.md"), "utf8")
const blocks = [...readme.matchAll(/^```ts\n([\s\S]*?)^```$/gm)].map((m) => m[1] ?? "")

/** What the reader's own app provides, as the README's comments describe it. */
const PRELUDE = `
declare const myServer: { startDaemonPairing(): Promise<{ code: string }>; cancelDaemonPairing(): Promise<void> }
declare function render(what: unknown): void
declare function offer(what: unknown): void
declare function askTheUser(params: unknown): Promise<string | null>
`

const PRELUDE_FILE = resolve(root, "readme-prelude.d.ts")
const blockFile = (i: number): string => resolve(root, `readme-block-${i}.ts`)

function diagnosticsByFile(): Map<string, string[]> {
  const config = ts.readConfigFile(resolve(root, "tsconfig.json"), ts.sys.readFile)
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root)
  const options: ts.CompilerOptions = {
    ...parsed.options,
    noEmit: true,
    baseUrl: root,
    paths: { "@agentd/client": ["./src/index.ts"] },
    skipLibCheck: true,
  }
  const virtual = new Map<string, string>([[PRELUDE_FILE, PRELUDE], ...blocks.map((b, i): [string, string] => [blockFile(i), `${b}\nexport {}\n`])])
  const host = ts.createCompilerHost(options)
  const { getSourceFile, fileExists, readFile } = host
  host.fileExists = (f) => virtual.has(f) || fileExists.call(host, f)
  host.readFile = (f) => virtual.get(f) ?? readFile.call(host, f)
  host.getSourceFile = (f, languageVersion, ...rest) => {
    const text = virtual.get(f)
    return text === undefined ? getSourceFile.call(host, f, languageVersion, ...rest) : ts.createSourceFile(f, text, languageVersion, true)
  }
  const program = ts.createProgram([...virtual.keys()], options, host)
  const out = new Map<string, string[]>()
  for (const d of ts.getPreEmitDiagnostics(program)) {
    if (d.file === undefined || !virtual.has(d.file.fileName)) continue
    const { line } = d.file.getLineAndCharacterOfPosition(d.start ?? 0)
    const where = `line ${line + 1}`
    out.set(d.file.fileName, [...(out.get(d.file.fileName) ?? []), `${where}: TS${d.code} ${ts.flattenDiagnosticMessageText(d.messageText, "\n")}`])
  }
  return out
}

describe("the README", () => {
  test("has code to check", () => {
    expect(blocks.length).toBeGreaterThan(5)
  })

  const problems = blocks.length === 0 ? new Map<string, string[]>() : diagnosticsByFile()
  test.each(blocks.map((b, i) => [i + 1, b.split("\n")[0] ?? "", i] as const))("ts block %i (%s…) compiles", (_n, _first, i) => {
    expect(problems.get(blockFile(i)) ?? []).toEqual([])
  })
})
