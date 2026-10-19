#!/bin/bash
# Regenerates the Go server code of the daemon contract beside contract/define.acp.ts. socketrpc-gen always writes a TypeScript
# client and package files too; the daemon is Go, so they are removed.
# socketrpc-gen is pinned in the root package.json.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GEN="$ROOT/node_modules/.bin/socketrpc-gen"
[ -x "$GEN" ] || (cd "$ROOT" && bun install --frozen-lockfile)
cd "$ROOT/contract"
"$GEN" ./define.acp.ts --client typescript --server go --go-out . --go-package contract </dev/null
rm -f package.json tsconfig.json client.generated.ts types.generated.ts
