#!/bin/bash
# Puts the current SDK into a checkout of the agentd-client repository, commits it and tags it v<version>. Nothing is pushed:
# the commit and the tag are for you to read first.
#
#   scripts/release-sdk.sh <path of the agentd-client checkout>
#
# A version's tag is final (apps' lockfiles pin its commit): when v<version> exists and the build differs from it, the script
# stops and asks for a new version in sdk/client/package.json.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO="${1:?usage: scripts/release-sdk.sh <path of the agentd-client checkout>}"
REPO="$(cd "$REPO" && pwd)"
git -C "$REPO" rev-parse --git-dir >/dev/null
VERSION="$(node -p "require('$ROOT/sdk/client/package.json').version")"
TAG="v$VERSION"

BUILT="$(mktemp -d)"
trap 'rm -rf "$BUILT"' EXIT
"$ROOT/scripts/build-sdk.sh" "$BUILT"

if git -C "$REPO" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  TAGGED="$(mktemp -d)"
  git -C "$REPO" archive "$TAG" | tar -x -C "$TAGGED"
  if ! diff -r "$BUILT" "$TAGGED" >/dev/null; then
    rm -rf "$TAGGED"
    echo "$TAG already exists in $REPO with other contents: bump the version in sdk/client/package.json" >&2
    exit 1
  fi
  rm -rf "$TAGGED"
  echo "$TAG already holds this build" >&2
  exit 0
fi

find "$REPO" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -R "$BUILT"/. "$REPO"/
git -C "$REPO" add -A
git -C "$REPO" commit -q -m "@agentd/client $VERSION"
git -C "$REPO" tag "$TAG"
echo "committed and tagged $TAG in $REPO (not pushed)" >&2
