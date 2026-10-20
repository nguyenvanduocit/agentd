#!/bin/bash
# Builds a release of agentd and, with --publish, publishes it as a GitHub release (tag v<version>). It carries:
# agentd.app, the desktop app for macOS (agentd-darwin-<arch>.dmg, and the signed update installed apps take:
# agentd-<version>-darwin-<arch>.tar.gz and update-darwin-<arch>.json); the daemon tarball (the Go engine of darwin and linux on
# arm64 and amd64), its latest.json and install.sh (the install from a terminal); and the SDK tarball (@agentd/client). The latest release serves them all.
#
#   DAEMON_SIGNING_KEY=.release-signing/daemon-signing.key APP_UPDATE_KEY=.release-signing/mygo-update.key \
#     scripts/release.sh <x.y.z> [--publish]
#
# Without --publish it builds and checks everything in a temporary directory and prints what it would upload. The private
# keys sign the daemon and the app's update on this machine; they never leave it. app/mygo.json carries the version of
# the app: make it <x.y.z> in the release's commit, and run scripts/pins.sh <x.y.z> so that the docs and the example pin
# this release. Publishing needs `gh` logged in, a clean work tree, and HEAD pushed:
# the tag marks the commit the files were built from (and is the Go module version of github.com/.../agentd).
# A tag is final: a fix is the next version.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${1:?usage: DAEMON_SIGNING_KEY=<key> APP_UPDATE_KEY=<key> scripts/release.sh <x.y.z> [--publish]}"
PUBLISH=0
[ "${2:-}" = "--publish" ] && PUBLISH=1
[[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "version \"$VERSION\" is not x.y.z" >&2; exit 1; }
: "${DAEMON_SIGNING_KEY:?DAEMON_SIGNING_KEY is the path of the release private key}"
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

RELEASE=1 VERSION="$VERSION" ./scripts/build-daemon.sh "$DIST"
./scripts/build-sdk.sh "$DIST"
MYGO_UPDATER_PRIVATE_KEY="$(cat "$APP_UPDATE_KEY")" DESKTOP_ARCHES="arm64 amd64" ./scripts/build-desktop.sh
DIR="$DIST/download/v$VERSION"
SDK_FILE="$(cd "$DIST" && ls agentd-client-*.tgz)"
FILES=("$DIR/agentd-$VERSION.tgz" "$DIR/latest.json" "$DIR/install.sh" "$DIST/$SDK_FILE")

# What was built agrees with itself: the manifest names this tarball's sha256, and the installer bakes the same one.
SHA="$(shasum -a 256 "$DIR/agentd-$VERSION.tgz" | cut -d' ' -f1)"
grep -q "\"sha256\":\"$SHA\"" "$DIR/latest.json" || { echo "latest.json does not carry the tarball's sha256" >&2; exit 1; }
grep -q "^SHA256='$SHA'" "$DIR/install.sh" || { echo "install.sh does not carry the tarball's sha256" >&2; exit 1; }
grep -q "releases/download/v$VERSION/agentd-$VERSION.tgz'" "$DIR/install.sh" || { echo "install.sh does not point at this release's tarball" >&2; exit 1; }

# The tarball carries the Go engine of every platform the installer and the updater pick from.
LISTING="$(tar -tzf "$DIR/agentd-$VERSION.tgz")"
for engine in agentd-darwin-arm64 agentd-darwin-amd64 agentd-linux-arm64 agentd-linux-amd64; do
  grep -qx "package/$engine" <<<"$LISTING" || { echo "the daemon tarball has no package/$engine" >&2; exit 1; }
done
grep -qx "package/launcher.sh" <<<"$LISTING" || { echo "the daemon tarball has no package/launcher.sh" >&2; exit 1; }

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
echo "  sha256 of the daemon tarball: $SHA" >&2

if [ "$PUBLISH" != 1 ]; then
  echo "not published (add --publish)" >&2
  exit 0
fi
[ -z "$(git status --porcelain)" ] || { echo "the work tree is not clean: commit first, the tag marks the commit the files were built from" >&2; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main: push first" >&2; exit 1; }
gh release create "v$VERSION" "${FILES[@]}" --repo "$REPO" --target "$(git rev-parse HEAD)" --title "agentd $VERSION" --generate-notes
echo "published https://github.com/$REPO/releases/tag/v$VERSION" >&2
