#!/bin/bash
# The two promises a release makes about the SDK, as checks.
#
#   scripts/check-release.sh sdk            # the SDK's version moved if its sources did (CI runs it on every push)
#   scripts/check-release.sh pins <x.y.z>   # every pin in the docs and the example names this release (release.sh runs it)
#
# sdk: `@agentd/client` is a file of a GitHub release, named by the SDK's own version, and "a published file never
# changes" (sdk/web/README.md). So when the compiled SDK would differ from the one of the last release tag, which is its
# non-test sources, sdk/web/package.json must name another version. The README does not count: it carries the pins, which
# move with every release.
#
# pins: the install line of the SDK (`releases/download/v<release>/agentd-client-<sdk version>.tgz`) and the Go module
# (`agentd@v<release>`) are written in the docs and in the example. Between releases they name the last release, whose
# files exist; the release being cut must name itself, and `scripts/pins.sh` moves them. A placeholder (`v<version>`) is
# not a pin.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Where the pins are written: scripts/pins.sh rewrites the same files.
PIN_FILES=(README.md sdk/web/README.md sdk/goclient/README.md sdk/goserver/README.md examples/minimal-app/README.md examples/minimal-app/page/package.json)
SDK_URL_PIN='releases/download/v[0-9][0-9.]*/agentd-client-[0-9][0-9.]*\.tgz'
GO_PIN='github\.com/nguyenvanduocit/agentd(/[A-Za-z0-9_/-]+)?@v[0-9][0-9.]*'

version_of() { node -p "JSON.parse(require('fs').readFileSync(0, 'utf8')).version"; }

check_sdk() {
  local prev old_dir before now
  prev="$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)"
  if [ -z "$prev" ]; then
    echo "no release tag yet: nothing to compare the SDK with" >&2
    return 0
  fi
  # The SDK was sdk/client until it was renamed: the last tag may carry it under either name.
  old_dir=sdk/web
  git cat-file -e "$prev:sdk/web/package.json" 2>/dev/null || old_dir=sdk/client
  if ! git cat-file -e "$prev:$old_dir/package.json" 2>/dev/null; then
    echo "$prev has no SDK to compare with" >&2
    return 0
  fi
  before="$(git show "$prev:$old_dir/package.json" | version_of)"
  now="$(version_of <sdk/web/package.json)"
  # The compiled SDK is its non-test sources: compare them by content, file by file, so a renamed directory is no change.
  local was is
  was="$(git ls-tree -r "$prev" -- "$old_dir/src" | awk -v p="$old_dir/src/" -F'\t' '{ split($1, a, " "); sub("^" p, "", $2); print a[3], $2 }' | grep -v '\.test\.ts$' | sort || true)"
  is="$(cd sdk/web/src && find . -type f ! -name '*.test.ts' | sed 's#^\./##' | sort | while read -r f; do echo "$(git hash-object "$f") $f"; done | sort)"
  if [ "$was" = "$is" ]; then
    echo "the SDK's sources are those of $prev: version $now is fine" >&2
    return 0
  fi
  if [ "$now" = "$before" ]; then
    echo "the SDK's sources changed since $prev, and sdk/web/package.json is still version $before: bump it." >&2
    echo "A published agentd-client-$before.tgz never changes, so a changed SDK is a new version in the next release." >&2
    return 1
  fi
  echo "the SDK's sources changed since $prev and its version moved ($before -> $now): fine" >&2
}

check_pins() {
  local release="$1" sdk bad=0 f line
  sdk="$(version_of <sdk/web/package.json)"
  for f in "${PIN_FILES[@]}"; do
    [ -f "$f" ] || continue
    while IFS=: read -r line pin; do
      [ "$pin" = "releases/download/v$release/agentd-client-$sdk.tgz" ] && continue
      echo "$f:$line pins $pin, the release being cut is v$release with SDK $sdk" >&2
      bad=1
    done < <(grep -noE "$SDK_URL_PIN" "$f" || true)
    while IFS=: read -r line pin; do
      [[ "$pin" == *"@v$release" ]] && continue
      echo "$f:$line pins $pin, the release being cut is v$release" >&2
      bad=1
    done < <(grep -noE "$GO_PIN" "$f" || true)
  done
  if [ "$bad" != 0 ]; then
    echo "run: scripts/pins.sh $release   (then commit)" >&2
    return 1
  fi
  echo "every pin names v$release (SDK $sdk)" >&2
}

case "${1:-}" in
  sdk) check_sdk ;;
  pins)
    release="${2:?usage: scripts/check-release.sh pins <x.y.z>}"
    [[ "$release" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "version \"$release\" is not x.y.z" >&2; exit 1; }
    check_pins "$release"
    ;;
  *)
    echo "usage: scripts/check-release.sh sdk | pins <x.y.z>" >&2
    exit 2
    ;;
esac
