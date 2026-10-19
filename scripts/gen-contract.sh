#!/bin/bash
# Regenerates the Go server code and the TypeScript client of the daemon contract, both beside contract/define.acp.ts.
# socketrpc-gen is pinned in the root package.json.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GEN="$ROOT/node_modules/.bin/socketrpc-gen"
[ -x "$GEN" ] || (cd "$ROOT" && bun install --frozen-lockfile)
cd "$ROOT/contract"
"$GEN" ./define.acp.ts --client typescript --server go --go-out . --go-package contract </dev/null
rm -f package.json tsconfig.json
