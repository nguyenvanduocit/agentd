#!/bin/bash
# Builds a release of agentd and, with --publish, publishes it as a GitHub release (tag v<version>) with four files:
# the daemon tarball, its latest.json and install.sh, and the SDK tarball (@agentd/client).
#
#   DAEMON_SIGNING_KEY=.release-signing/daemon-signing.key scripts/release.sh <x.y.z> [--publish]
#
# Without --publish it builds and checks everything in a temporary directory and prints what it would upload. The private
# key signs the daemon on this machine; it never leaves it. Publishing needs `gh` logged in, a clean work tree, and HEAD
# pushed: the tag marks the commit the files were built from (and is the Go module version of github.com/.../agentd).
# A tag is final: a fix is the next version.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${1:?usage: DAEMON_SIGNING_KEY=<key> scripts/release.sh <x.y.z> [--publish]}"
PUBLISH=0
[ "${2:-}" = "--publish" ] && PUBLISH=1
[[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "version \"$VERSION\" is not x.y.z" >&2; exit 1; }
: "${DAEMON_SIGNING_KEY:?DAEMON_SIGNING_KEY is the path of the release private key}"

REPO="nguyenvanduocit/agentd"
DIST="$(mktemp -d)"
trap 'rm -rf "$DIST"' EXIT

RELEASE=1 VERSION="$VERSION" ./scripts/build-daemon.sh "$DIST"
./scripts/build-sdk.sh "$DIST"
DIR="$DIST/download/v$VERSION"
SDK_FILE="$(cd "$DIST" && ls agentd-client-*.tgz)"
FILES=("$DIR/agentd-$VERSION.tgz" "$DIR/latest.json" "$DIR/install.sh" "$DIST/$SDK_FILE")

# What was built agrees with itself: the manifest names this tarball's sha256, and the installer bakes the same one.
SHA="$(shasum -a 256 "$DIR/agentd-$VERSION.tgz" | cut -d' ' -f1)"
grep -q "\"sha256\":\"$SHA\"" "$DIR/latest.json" || { echo "latest.json does not carry the tarball's sha256" >&2; exit 1; }
grep -q "^SHA256='$SHA'" "$DIR/install.sh" || { echo "install.sh does not carry the tarball's sha256" >&2; exit 1; }
grep -q "releases/download/v$VERSION/agentd-$VERSION.tgz'" "$DIR/install.sh" || { echo "install.sh does not point at this release's tarball" >&2; exit 1; }
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
