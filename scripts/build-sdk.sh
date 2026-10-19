#!/bin/bash
# Builds @agentd/client as the package an app installs: the compiled ESM, its .d.ts files, the README, the LICENSE and a
# package.json without the development fields. <outdir> becomes the root of the agentd-client repository.
#
#   scripts/build-sdk.sh <outdir>
#
# The same sources make the same files, so a version's tag in agentd-client always holds the bytes its lockfile pinned.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: scripts/build-sdk.sh <outdir>}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

cd "$ROOT/sdk/client"
[ -d node_modules ] || bun install --frozen-lockfile
rm -rf dist
bunx tsc -p tsconfig.build.json

rm -rf "$OUT/dist"
cp -R dist "$OUT/dist"
cp README.md "$OUT/README.md"
cp "$ROOT/LICENSE" "$OUT/LICENSE"
node -e '
  const p = require("./package.json")
  delete p.scripts
  delete p.devDependencies
  require("fs").writeFileSync(process.argv[1], JSON.stringify(p, null, 2) + "\n")
' "$OUT/package.json"
echo "built @agentd/client $(node -p "require('./package.json').version") in $OUT" >&2
