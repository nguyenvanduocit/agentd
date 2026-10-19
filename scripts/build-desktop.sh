#!/bin/bash
# Builds agentd.app, the desktop app (tray/, with mygo), for macOS. Into its resources go the engine (agentd, the Go
# binary of cmd/agentd, which never updates itself: the app does) and Node.js for the ACP adapters, the version
# daemon/install.sh.in pins, checked against nodejs.org's SHASUMS256.txt.
#
#   scripts/build-desktop.sh
#
# The version is tray/mygo.json's. Writes tray/build/darwin-<arch>/agentd.app and "agentd <version> <arch>.dmg" for
# each of $DESKTOP_ARCHES (default: "arm64 amd64"), and with $MYGO_UPDATER_PRIVATE_KEY (the key itself) the signed update
# installed apps take: agentd-<version>-darwin-<arch>.tar.gz and update-darwin-<arch>.json. Node.js downloads stay in
# $AGENTD_NODE_CACHE between builds.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARCHES="${DESKTOP_ARCHES:-arm64 amd64}"
VERSION="$(sed -n 's/^  "version": "\(.*\)",$/\1/p' "$ROOT/tray/mygo.json")"
NODE_VERSION="$(sed -n "s/^NODE_VERSION='\([0-9.]*\)'\$/\1/p" "$ROOT/daemon/install.sh.in")"
[ -n "$VERSION" ] || { echo "no version in tray/mygo.json" >&2; exit 1; }
[ -n "$NODE_VERSION" ] || { echo "no NODE_VERSION in daemon/install.sh.in" >&2; exit 1; }
NODE_DIST="${AGENTD_NODE_DIST:-https://nodejs.org/dist}"
CACHE="${AGENTD_NODE_CACHE:-$HOME/Library/Caches/agentd-build/node}"
RES="$ROOT/tray/resources"

rm -rf "$RES" "$ROOT/tray/build"
mkdir -p "$CACHE"

# The engine for one architecture, into the platform's resources, which mygo build merges into that platform's app
# (resources/darwin-<arch>/engine/agentd is engine/agentd in Contents/Resources). It has no release key and no releases
# page: the app updates it.
engine_for() {
  local arch="$1" pkg=github.com/nguyenvanduocit/agentd/daemon/home
  mkdir -p "$RES/darwin-$arch/engine"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $pkg.Version=$VERSION -X $pkg.ReleaseKey= -X $pkg.ReleaseURL=" \
    -o "$RES/darwin-$arch/engine/agentd" ./cmd/agentd)
}

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
  engine_for "$arch"
  node_for "$arch"
  platforms="${platforms:+$platforms,}darwin/$arch"
done

cd "$ROOT/tray"
go tool mygo build -platform "$platforms" -o build
echo "built agentd.app $VERSION for: $ARCHES (Node.js $NODE_VERSION)" >&2
