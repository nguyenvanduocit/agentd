#!/bin/bash
# Builds @agentd/client as the tarball an app installs: the compiled ESM, its .d.ts files, the README, the LICENSE and a
# package.json without the development fields. The tarball is a file of the GitHub release (scripts/release.sh).
#
#   scripts/build-sdk.sh <outdir>      # writes <outdir>/agentd-client-<version>.tgz
#
# `npm pack` fixes every timestamp and owner of the archive: the same sources make the same bytes, so a version's file
# keeps the integrity a lockfile recorded.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: scripts/build-sdk.sh <outdir>}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

cd "$ROOT/sdk/web"
[ -d node_modules ] || bun install --frozen-lockfile
rm -rf dist
bunx tsc -p tsconfig.build.json
VERSION="$(node -p "require('./package.json').version")"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
mkdir "$STAGE/package"
cp -R dist "$STAGE/package/dist"
cp README.md "$STAGE/package/README.md"
cp "$ROOT/LICENSE" "$STAGE/package/LICENSE"
node -e '
  const p = require("./package.json")
  delete p.scripts
  delete p.devDependencies
  require("fs").writeFileSync(process.argv[1], JSON.stringify(p, null, 2) + "\n")
' "$STAGE/package/package.json"
(cd "$STAGE/package" && npm pack --silent --pack-destination "$STAGE" >/dev/null)
mv "$STAGE/agentd-client-$VERSION.tgz" "$OUT/agentd-client-$VERSION.tgz"
echo "built $OUT/agentd-client-$VERSION.tgz" >&2
