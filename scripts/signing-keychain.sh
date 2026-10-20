#!/bin/bash
# Makes a macOS machine ready to sign and notarize agentd.app, as the release workflow needs it: a temporary keychain holding
# the Developer ID Application certificate (with the Apple intermediate it chains to) and the `notary` notarytool profile that
# app/mygo.json names. `mygo build` then signs with the identity and notarizes with the profile like it does on a developer's Mac.
#
#   APPLE_CERT_P12_BASE64=... APPLE_CERT_P12_PASSWORD=... APPLE_NOTARY_KEY_BASE64=... \
#     APPLE_NOTARY_KEY_ID=... APPLE_NOTARY_ISSUER_ID=... scripts/signing-keychain.sh setup
#   scripts/signing-keychain.sh teardown
#
# setup puts the temporary keychain first in the search list and makes it the default; teardown puts back the keychains the
# machine had and deletes the temporary one (and is safe to run when setup did not finish). The secrets pass through files
# in a private directory that setup and teardown delete.
set -euo pipefail

STATE="${AGENTD_SIGNING_STATE:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/agentd-signing}"
KEYCHAIN="$STATE/signing.keychain-db"

unquote() { sed -e 's/^[[:space:]]*"//' -e 's/"[[:space:]]*$//'; }

setup() {
  : "${APPLE_CERT_P12_BASE64:?the Developer ID certificate and its key as a base64 .p12}"
  : "${APPLE_CERT_P12_PASSWORD:?the password of that .p12}"
  : "${APPLE_NOTARY_KEY_BASE64:?the App Store Connect API key (.p8) as base64}"
  : "${APPLE_NOTARY_KEY_ID:?the key id of that API key}"
  : "${APPLE_NOTARY_ISSUER_ID:?the issuer id of the App Store Connect team}"
  [ ! -e "$STATE" ] || { echo "$STATE exists: run teardown first" >&2; exit 1; }
  mkdir -m 700 "$STATE"

  # A machine can have no default keychain or none listed: the files are then empty, and teardown puts back nothing.
  { security default-keychain -d user 2>/dev/null || true; } | unquote >"$STATE/default.prev"
  { security list-keychains -d user 2>/dev/null || true; } | unquote >"$STATE/list.prev"

  local pass
  pass="$(openssl rand -hex 24)"
  security create-keychain -p "$pass" "$KEYCHAIN"
  security set-keychain-settings -lut 21600 "$KEYCHAIN"
  security unlock-keychain -p "$pass" "$KEYCHAIN"

  printf '%s' "$APPLE_CERT_P12_BASE64" | base64 -d >"$STATE/cert.p12"
  security import "$STATE/cert.p12" -k "$KEYCHAIN" -P "$APPLE_CERT_P12_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
  rm -f "$STATE/cert.p12"
  # Lets codesign use the key without the prompt nobody is there to click.
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$pass" "$KEYCHAIN" >/dev/null

  local previous=()
  while IFS= read -r line; do [ -n "$line" ] && previous+=("$line"); done <"$STATE/list.prev"
  # ${previous[@]+...}: bash 3.2, the macOS one, treats an empty array as unset under set -u.
  security list-keychains -d user -s "$KEYCHAIN" ${previous[@]+"${previous[@]}"}
  security default-keychain -d user -s "$KEYCHAIN"

  security find-identity -v -p codesigning "$KEYCHAIN" | grep -q 'Developer ID Application' ||
    { echo "no valid Developer ID Application identity in the keychain" >&2; exit 1; }

  printf '%s' "$APPLE_NOTARY_KEY_BASE64" | base64 -d >"$STATE/notary.p8"
  xcrun notarytool store-credentials notary --key "$STATE/notary.p8" --key-id "$APPLE_NOTARY_KEY_ID" \
    --issuer "$APPLE_NOTARY_ISSUER_ID" --keychain "$KEYCHAIN" >/dev/null
  rm -f "$STATE/notary.p8"
  echo "signing keychain ready: $(security find-identity -v -p codesigning "$KEYCHAIN" | sed -n 's/.*"\(.*\)"/\1/p'), profile notary" >&2
}

teardown() {
  [ -d "$STATE" ] || return 0
  if [ -s "$STATE/list.prev" ]; then
    local previous=()
    while IFS= read -r line; do [ -n "$line" ] && previous+=("$line"); done <"$STATE/list.prev"
    security list-keychains -d user -s ${previous[@]+"${previous[@]}"}
  fi
  if [ -s "$STATE/default.prev" ]; then security default-keychain -d user -s "$(cat "$STATE/default.prev")"; fi
  if [ -e "$KEYCHAIN" ]; then security delete-keychain "$KEYCHAIN" 2>/dev/null || true; fi
  rm -rf "$STATE"
}

case "${1:-}" in
  setup) setup ;;
  teardown) teardown ;;
  *) echo "usage: scripts/signing-keychain.sh setup|teardown" >&2; exit 2 ;;
esac
