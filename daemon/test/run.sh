#!/bin/sh
# bun run test: the integration tests build the daemon bundle and two Go binaries (about 35 MB) once per run. They go
# into a directory of this run, removed when it ends, whatever the result. Arguments replace the default test paths.
dir="$(mktemp -d "${TMPDIR:-/tmp}/mc-daemon-it-build-XXXXXX")"
trap 'rm -rf "$dir"' EXIT INT TERM
if [ $# -eq 0 ]; then set -- test; fi
MC_IT_BUILD_DIR="$dir" bun test --timeout 90000 "$@"
