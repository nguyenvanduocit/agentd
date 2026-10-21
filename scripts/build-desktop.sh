#!/bin/bash
# Builds agentd.app, the desktop app (app/, with mygo), for macOS. The app is one executable that carries the engine
# (daemon/run) and the command line (daemon/entry); into its resources goes Node.js for the ACP adapters, checked
# against nodejs.org's SHASUMS256.txt.
#
#   scripts/build-desktop.sh
#
# The version is app/mygo.json's. Writes app/build/darwin-<arch>/agentd.app and "agentd <version> <arch>.dmg" for
# each of $DESKTOP_ARCHES (default: "arm64 amd64"), and with $MYGO_UPDATER_PRIVATE_KEY (the key itself) the signed update
# installed apps take: agentd-<version>-darwin-<arch>.tar.gz and update-darwin-<arch>.json. Node.js downloads stay in
# $AGENTD_NODE_CACHE between builds.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARCHES="${DESKTOP_ARCHES:-arm64 amd64}"
VERSION="$(sed -n 's/^  "version": "\(.*\)",$/\1/p' "$ROOT/app/mygo.json")"
NODE_VERSION="24.21.0" # the Node.js the adapters run on
[ -n "$VERSION" ] || { echo "no version in app/mygo.json" >&2; exit 1; }
NODE_DIST="${AGENTD_NODE_DIST:-https://nodejs.org/dist}"
CACHE="${AGENTD_NODE_CACHE:-$HOME/Library/Caches/agentd-build/node}"
RES="$ROOT/app/resources"

rm -rf "$RES" "$ROOT/app/build"
mkdir -p "$CACHE"

# Node.js for one architecture, from nodejs.org once, checked against its SHASUMS256.txt: bin (node, npm, npx) and lib
# (npm itself), into the platform's resources, which mygo build merges into that platform's app. The adapters run on it.
node_for() {
  local arch="$1" name sums tgz want got
  name="node-v$NODE_VERSION-darwin-${arch/amd64/x64}" # Node.js names Intel builds x64
  sums="$CACHE/SHASUMS256-$NODE_VERSION.txt"
  tgz="$CACHE/$name.tar.gz"
  [ -s "$sums" ] || curl -fsSL "$NODE_DIST/v$NODE_VERSION/SHASUMS256.txt" -o "$sums"
  [ -s "$tgz" ] || curl -fsSL "$NODE_DIST/v$NODE_VERSION/$name.tar.gz" -o "$tgz"
  want="$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$sums")"
  got="$(shasum -a 256 "$tgz" | cut -d' ' -f1)"
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    rm -f "$tgz"
    echo "$name.tar.gz: sha256 $got is not nodejs.org's ($want)" >&2
    exit 1
  fi
  mkdir -p "$RES/darwin-$arch/node"
  tar -xzf "$tgz" -C "$RES/darwin-$arch/node" --strip-components=1 "$name/bin" "$name/lib"
}

platforms=""
for arch in $ARCHES; do
  node_for "$arch"
  platforms="${platforms:+$platforms,}darwin/$arch"
done

cd "$ROOT/app"
go tool mygo build -platform "$platforms" -o build
echo "built agentd.app $VERSION for: $ARCHES (Node.js $NODE_VERSION)" >&2
