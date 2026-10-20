#!/bin/bash
# Builds the example app: the Go server and its page. The page installs @agentd/client the way an app does, except that
# the package is the tarball of this working tree (scripts/build-sdk.sh) instead of the one on the GitHub release.
#
#   scripts/build-example.sh <outdir>
#
# Writes <outdir>/minimal-app and <outdir>/static/{index.html,app.js}.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: scripts/build-example.sh <outdir>}"
mkdir -p "$OUT/static"
OUT="$(cd "$OUT" && pwd)"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp "$ROOT/examples/minimal-app/page/"{index.html,main.ts,package.json} "$STAGE/"
"$ROOT/scripts/build-sdk.sh" "$STAGE/sdk"
(cd "$STAGE" && node -e '
  const p = require("./package.json")
  p.dependencies["@agentd/client"] = "file:./sdk/" + require("fs").readdirSync("./sdk").find((f) => f.endsWith(".tgz"))
  require("fs").writeFileSync("package.json", JSON.stringify(p, null, 2) + "\n")
')
# A cache of its own: a file: dependency is copied, and a shared cache would hand back an older copy of the same version.
(cd "$STAGE" && BUN_INSTALL_CACHE_DIR="$STAGE/.cache" bun install >&2)
# The page is typechecked, strictly, against the SDK it is built with (the tarball above): what the example calls must exist there.
(cd "$STAGE" && "$ROOT/sdk/web/node_modules/.bin/tsc" --noEmit --strict --target es2022 --module esnext --moduleResolution bundler --lib es2022,dom --skipLibCheck main.ts >&2)
(cd "$STAGE" && bun build main.ts --target=browser --outfile "$OUT/static/app.js" >&2)
cp "$STAGE/index.html" "$OUT/static/index.html"
(cd "$ROOT" && go build -o "$OUT/minimal-app" ./examples/minimal-app)
echo "built the example app in $OUT" >&2
