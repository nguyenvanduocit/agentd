#!/bin/bash
# Fails when the committed generated code differs from what contract/define.acp.ts generates: run after editing the
# contract, and in CI.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
snapshot() { find contract -name '*.generated.*' -print0 | sort -z | xargs -0 shasum -a 256; }
before="$(snapshot)"
./scripts/gen-contract.sh >/dev/null
if [ "$before" != "$(snapshot)" ]; then
  echo "generated code is out of date: run scripts/gen-contract.sh and commit the result" >&2
  exit 1
fi
echo "generated code is up to date"
