#!/bin/bash
# Moves every pin of the SDK and of the Go module in the docs and in the example to a release:
#
#   scripts/pins.sh <x.y.z> [<sdk version>]     # the SDK version defaults to the one in sdk/web/package.json
#
# The pins are the install line of the SDK (`releases/download/v<release>/agentd-client-<sdk version>.tgz`) and the Go
# module (`agentd@v<release>`). A release needs them to name itself (scripts/release.sh checks), and the files they
# name exist only once the release is published, so this is run in the commit that is released. A placeholder
# (`v<version>`) is left as it is. Commit the result.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
RELEASE="${1:?usage: scripts/pins.sh <x.y.z> [<sdk version>]}"
[[ "$RELEASE" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "version \"$RELEASE\" is not x.y.z" >&2; exit 1; }
SDK="${2:-$(node -p "require('./sdk/web/package.json').version")}"
[[ "$SDK" =~ ^[0-9][0-9.]*$ ]] || { echo "SDK version \"$SDK\" is not a plain version" >&2; exit 1; }
export RELEASE SDK

# The same files and patterns as scripts/check-release.sh.
for f in README.md sdk/web/README.md sdk/goclient/README.md sdk/goserver/README.md sdk/release/README.md examples/minimal-app/README.md examples/minimal-app/page/package.json; do
  [ -f "$f" ] || continue
  perl -pi -e '
    s{(releases/download/v)[0-9][0-9.]*(/agentd-client-)[0-9][0-9.]*(\.tgz)}{$1$ENV{RELEASE}$2$ENV{SDK}$3}g;
    s{(github\.com/nguyenvanduocit/agentd(?:/[A-Za-z0-9_/-]+)?\@v)[0-9][0-9.]*}{$1$ENV{RELEASE}}g;
  ' "$f"
done
echo "pins name v$RELEASE (SDK $SDK)" >&2
"$ROOT/scripts/check-release.sh" pins "$RELEASE"
