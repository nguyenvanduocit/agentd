#!/bin/bash
# The whole product end to end, natively, through ego-browser: the release server, the example app built on the hub library and
# the page SDK (built from this tree), and real daemons installed, paired, answering and updated.
#
#   scripts/e2e.sh [scenario]        # e2e/<scenario>.mjs, default: all
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# A page scans 127.0.0.1:47615-47624 and takes the first daemon that answers: one already running there would be paired with the test app.
for port in $(seq 47615 47624); do
  if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$port/v1/status"; then
    echo "a daemon already listens on 127.0.0.1:$port; stop it and run the e2e again" >&2
    exit 1
  fi
done
free_port() { python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
WORK="$(mktemp -d)"
DIST="$WORK/dist"
mkdir -p "$DIST" "$WORK/empty-tray"
PIDS=()
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

RPORT="$(free_port)"
APORT="$(free_port)"
AGENTD_URL="http://127.0.0.1:$RPORT"
APP_URL="http://127.0.0.1:$APORT"
RUN="run$$-$RANDOM"

# The release: daemon 0.0.1 (no tray: the scenario does not look at it) and the server.
VERSION=0.0.1 RELEASE_URL="$AGENTD_URL" TRAY_DIR="$WORK/empty-tray" ./scripts/build-daemon.sh "$DIST" >&2
go build -o "$WORK/agentd-server" ./cmd/agentd-server
go build -o "$WORK/fake-agent" ./cmd/fake-agent
"$WORK/agentd-server" -addr "127.0.0.1:$RPORT" -dist "$DIST" >"$WORK/server.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 40); do curl -s -o /dev/null "$AGENTD_URL/healthz" && break; sleep 0.25; done

# The app: built the way any app builds against agentd.
./scripts/build-example.sh "$WORK/example"
"$WORK/example/minimal-app" -addr "127.0.0.1:$APORT" -static "$WORK/example/static" -agentd-url "$AGENTD_URL" >"$WORK/app.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 40); do curl -s -o /dev/null "$APP_URL/" && break; sleep 0.25; done

SED_PATH="${PATH//\\/\\\\}"; SED_PATH="${SED_PATH//&/\\&}"
status=0
for scenario in e2e/${1:-*}.mjs; do
  case "$scenario" in e2e/lib/*) continue ;; esac
  echo "== $scenario"
  cat e2e/lib/prelude.mjs "$scenario" | sed -e "s#__APP_URL__#$APP_URL#g" -e "s#__AGENTD_URL__#$AGENTD_URL#g" -e "s#__DIST__#$DIST#g" -e "s#__EMPTY_TRAY__#$WORK/empty-tray#g" -e "s#__ROOT__#$ROOT#g" \
    -e "s#__RUN__#$RUN#g" -e "s#__FAKE_AGENT__#$WORK/fake-agent#g" -e "s#__PATH__#$SED_PATH#g" | ego-browser nodejs || { status=1; echo "-- server log (tail)"; tail -n 15 "$WORK/server.log"; echo "-- app log (tail)"; tail -n 15 "$WORK/app.log"; }
  # A scenario that throws midway never reaches task.finish: close the spaces this run opened.
  sed "s#__RUN__#$RUN#g" <<'JS' | ego-browser nodejs >/dev/null 2>&1 || true
for (const s of await listTaskSpaces()) {
  if (s.name?.endsWith(" __RUN__") && s.ownership === "agent") await (await taskSpace(s.spaceId ?? s.id)).finish({ keep: [] });
}
JS
  pkill -KILL -f "/agentd-e2e-$RUN-" 2>/dev/null || true
done
exit "$status"
