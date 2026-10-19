// Strict lint for the daemon: the frontend's limits, without Vue. Run with `bun run lint`.
import js from "@eslint/js"
import globals from "globals"
import tseslint from "typescript-eslint"
import sonarjs from "eslint-plugin-sonarjs"
import { importX } from "eslint-plugin-import-x"

// src/core is pure: it decides, and src/ outside core does the I/O. Core may not reach for the shell's modules or for
// anything that touches the machine.
const IO_MODULES = ["node:fs", "node:fs/promises", "node:child_process", "node:net", "node:http", "node:https", "node:os", "socket.io-client"]

export default tseslint.config(
  { ignores: ["dist", "node_modules", "../contract/*.generated.ts"] },
  { linterOptions: { reportUnusedDisableDirectives: "error" } },

  js.configs.recommended,
  tseslint.configs.strictTypeChecked,
  tseslint.configs.stylisticTypeChecked,
  sonarjs.configs.recommended,
  importX.flatConfigs.recommended,
  importX.flatConfigs.typescript,

  {
    languageOptions: {
      globals: { ...globals.node },
      parserOptions: { projectService: false, project: ["./tsconfig.json"], tsconfigRootDir: import.meta.dirname },
    },
    settings: {
      "import-x/resolver": { typescript: { project: ["./tsconfig.json"] } },
    },
  },

  {
    rules: {
      // Complexity and cognitive load (same limits as frontend/eslint.config.js)
      complexity: ["error", 10],
      "sonarjs/cognitive-complexity": ["error", 12],
      "max-depth": ["error", 4],
      "max-nested-callbacks": ["error", 3],
      "max-params": ["error", 5],
      "max-statements": ["error", 30],
      "max-lines-per-function": ["error", { max: 50, skipBlankLines: true, skipComments: true }],
      "max-lines": ["error", { max: 400, skipBlankLines: true, skipComments: true }],

      // Coupling
      "import-x/no-cycle": "error",
      "import-x/no-self-import": "error",
      "import-x/no-useless-path-segments": "error",
      "import-x/no-duplicates": "error",
      "import-x/no-mutable-exports": "error",
      "import-x/first": "error",
      "import-x/no-unresolved": ["error", { ignore: ["^bun:"] }],
      "import-x/namespace": "off",
      "import-x/default": "off",
      "import-x/no-named-as-default": "off",
      "import-x/no-named-as-default-member": "off",

      // Correctness and explicitness
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/switch-exhaustiveness-check": "error",
      "@typescript-eslint/no-unnecessary-condition": "error",
      "@typescript-eslint/no-confusing-void-expression": ["error", { ignoreArrowShorthand: true }],
      "@typescript-eslint/no-import-type-side-effects": "error",
      "@typescript-eslint/restrict-template-expressions": ["error", { allowNumber: true }],
      "@typescript-eslint/no-floating-promises": ["error", { ignoreVoid: true }],
      "no-console": "error",
      "no-var": "error",
      "prefer-const": "error",
      eqeqeq: ["error", "always"],
      curly: ["error", "multi-line"],
    },
  },

  {
    files: ["src/core/**/*.ts"],
    rules: {
      "no-restricted-imports": ["error", {
        paths: IO_MODULES.map((name) => ({ name, message: "src/core is pure: do the I/O in src/ outside core" })),
        patterns: [{ regex: "^\\.\\./(?!\\.\\./contract)", message: "src/core must not import the shell modules of src/" }],
      }],
    },
  },

  // Tests: long describe blocks, fixtures and non-null assertions are normal.
  {
    files: ["test/**/*.ts"],
    rules: {
      "max-lines-per-function": "off",
      "max-statements": "off",
      "max-nested-callbacks": "off",
      "max-lines": "off",
      "sonarjs/no-duplicate-string": "off",
      "sonarjs/no-os-command-from-path": "off",
      "sonarjs/publicly-writable-directories": "off",
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/require-await": "off",
      "@typescript-eslint/no-empty-function": "off",
      // bun:test's `expect(...).rejects` is typed as a plain matcher; tests also feed plain strings and http:// URLs on purpose.
      "@typescript-eslint/no-unsafe-assignment": "off", // expect.stringContaining() and friends are typed any
      "@typescript-eslint/await-thenable": "off",
      "@typescript-eslint/no-confusing-void-expression": "off",
      "@typescript-eslint/prefer-promise-reject-errors": "off",
      "sonarjs/no-clear-text-protocols": "off",
      "sonarjs/prefer-specific-assertions": "off",
    },
  },
  {
    files: ["eslint.config.js"],
    ...tseslint.configs.disableTypeChecked,
  },
)
