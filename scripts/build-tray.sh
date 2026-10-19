#!/bin/bash
# Builds the tray program (tray/, Go with mygo) for every platform the daemon supports.
#
#   scripts/build-tray.sh <outdir>
#
# Writes <outdir>/<os>-<arch>/agentd-tray (agentd-tray.exe on Windows), named as Go names its platforms: it is the
# layout scripts/build-daemon.sh packs as tray/ and the daemon looks for (daemon/tray/state.go Binary). No cgo: every
# platform builds from any machine. $TRAY_TARGETS ("darwin/arm64 linux/amd64") limits the list (tests).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: scripts/build-tray.sh <outdir>}"
TARGETS="${TRAY_TARGETS:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64}"

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
cd "$ROOT/tray"
for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  exe=agentd-tray
  [ "$os" = windows ] && exe=agentd-tray.exe
  mkdir -p "$OUT/$os-$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$OUT/$os-$arch/$exe" .
done
echo "built the tray for: $TARGETS" >&2
