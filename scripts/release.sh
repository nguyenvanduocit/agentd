#!/bin/bash
# Builds a release of agentd and, with --publish, publishes it as a GitHub release (tag v<version>). It carries
# agentd.app for macOS (agentd-darwin-<arch>.dmg, and the signed update installed apps take:
# agentd-<version>-darwin-<arch>.tar.gz and update-darwin-<arch>.json) and the SDK tarball (@agentd/client).
#
#   APP_UPDATE_KEY=.release-signing/mygo-update.key scripts/release.sh <x.y.z> [--publish]
#
# Without --publish it builds and checks everything in a temporary directory and prints what it would upload. The private
# key signs the app's update; it is the file named by APP_UPDATE_KEY, and nowhere else but the
# secrets of the `release` environment, which .github/workflows/release.yml runs this script with on a macOS runner
# (signing the app and notarizing it with the Developer ID and the notary profile that scripts/signing-keychain.sh sets up
# there). app/mygo.json carries the version of the app: make it <x.y.z> in the release's commit, and run
# scripts/pins.sh <x.y.z> so that the docs and the example pin this release. Publishing needs `gh` logged in, a clean work
# tree, and HEAD pushed:
# the tag marks the commit the files were built from (and is the Go module version of github.com/.../agentd).
# A tag is final: a fix is the next version.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${1:?usage: APP_UPDATE_KEY=<key> scripts/release.sh <x.y.z> [--publish]}"
PUBLISH=0
[ "${2:-}" = "--publish" ] && PUBLISH=1
[[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "version \"$VERSION\" is not x.y.z" >&2; exit 1; }
: "${APP_UPDATE_KEY:?APP_UPDATE_KEY is the path of the private key that signs the app updates (mygo keygen)}"
APP_VERSION="$(sed -n 's/^  "version": "\(.*\)",$/\1/p' app/mygo.json)"
[ "$APP_VERSION" = "$VERSION" ] || { echo "app/mygo.json is version $APP_VERSION: make it $VERSION and commit it first" >&2; exit 1; }
# The SDK file of a release never changes: its version moved if its sources did, and the pins in the docs and the example
# name this release (scripts/pins.sh moves them). Both are checked before anything is built.
./scripts/check-release.sh sdk
./scripts/check-release.sh pins "$VERSION"

REPO="nguyenvanduocit/agentd"
DIST="$(mktemp -d)"
trap 'rm -rf "$DIST"' EXIT

./scripts/build-sdk.sh "$DIST"
MYGO_UPDATER_PRIVATE_KEY="$(cat "$APP_UPDATE_KEY")" DESKTOP_ARCHES="arm64 amd64" ./scripts/build-desktop.sh
SDK_FILE="$(cd "$DIST" && ls agentd-client-*.tgz)"
FILES=("$DIST/$SDK_FILE")

# The app of each processor: its disk image under a name that stays from release to release (the SDK's download links),
# and its signed update, whose manifest names this version and this release's archive, as installed apps read it from
# the latest release.
for arch in arm64 amd64; do
  out="app/build/darwin-$arch"
  manifest="$out/update-darwin-$arch.json"
  archive="agentd-$VERSION-darwin-$arch.tar.gz"
  grep -q "\"version\": \"$VERSION\"" "$manifest" || { echo "$manifest is not version $VERSION" >&2; exit 1; }
  grep -q "\"url\": \"https://github.com/$REPO/releases/download/v$VERSION/$archive\"" "$manifest" || { echo "$manifest does not point at this release's $archive" >&2; exit 1; }
  grep -q '"signature": "[A-Za-z0-9+/=]\{40,\}"' "$manifest" || { echo "$manifest carries no signature" >&2; exit 1; }
  LC_ALL=C grep -aqF "https://github.com/$REPO/releases/latest/download/update-darwin-$arch.json" "$out/agentd.app/Contents/MacOS/agentd" ||
    { echo "the $arch app does not read its updates from the latest release" >&2; exit 1; }
  cp "$out/agentd $VERSION $arch.dmg" "$DIST/agentd-darwin-$arch.dmg"
  FILES+=("$DIST/agentd-darwin-$arch.dmg" "$out/$archive")
  for delta in "$out"/*.delta; do [ -e "$delta" ] && FILES+=("$delta"); done
  FILES+=("$manifest")
done

echo "release v$VERSION of $REPO:" >&2
for f in "${FILES[@]}"; do printf '  %s  %s bytes\n' "$(basename "$f")" "$(wc -c <"$f" | tr -d ' ')" >&2; done

if [ "$PUBLISH" != 1 ]; then
  echo "not published (add --publish)" >&2
  exit 0
fi
[ -z "$(git status --porcelain)" ] || { echo "the work tree is not clean: commit first, the tag marks the commit the files were built from" >&2; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main: push first" >&2; exit 1; }
gh release create "v$VERSION" "${FILES[@]}" --repo "$REPO" --target "$(git rev-parse HEAD)" --title "agentd $VERSION" --generate-notes
echo "published https://github.com/$REPO/releases/tag/v$VERSION" >&2
