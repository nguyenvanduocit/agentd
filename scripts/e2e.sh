#!/bin/bash
# The whole product end to end, natively, through ego-browser: the release server, the example app built on sdk/goserver and
# the page SDK (built from this tree), and real daemons installed, paired, answering and updated.
#
#   scripts/e2e.sh [scenario]        # e2e/<scenario>.mjs, default: all
#   E2E_SHOTS=/abs/dir scripts/e2e.sh   # also saves a screenshot of the page at each key moment (png, in that directory)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# A page takes the first daemon that answers on the ports it looks at, and a daemon's gateway walks up ten ports from
# AGENTD_LOCAL_PORT. This run uses ten ports of its own (the example app's page looks only there), so a daemon of yours on
# 47615-47624 is neither paired with the test app nor in its way: nothing needs stopping.
range_free() {
  local p
  for p in $(seq "$1" $(($1 + 9))); do
    curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$p/v1/status" && return 1
    if command -v lsof >/dev/null; then lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 && return 1; fi
  done
  return 0
}
GATEWAY_BASE=""
for _ in $(seq 1 60); do
  candidate=$((47700 + RANDOM % 1200))
  if range_free "$candidate"; then GATEWAY_BASE="$candidate"; break; fi
done
if [ -z "$GATEWAY_BASE" ]; then
  echo "no range of ten free ports found between 47700 and 48908: free some and run the e2e again" >&2
  exit 1
fi
echo "gateway ports of this run: $GATEWAY_BASE-$((GATEWAY_BASE + 9)) (a daemon on 47615-47624 is left alone)" >&2
free_port() { python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
WORK="$(mktemp -d)"
DIST="$WORK/dist"
mkdir -p "$DIST" "$WORK/empty-tray" "$WORK/machines"
RUN="run$$-$RANDOM"
PIDS=()
# A scenario's machines (HOME, daemon) live under $WORK/machines and their processes carry /agentd-e2e-$RUN- in their
# command lines: however the run ends (a failed build, a scenario that throws, Ctrl-C), none of them is left behind.
end_machines() {
  # On macOS a machine's process is a launchd job (e2e/lib/prelude.mjs), and launchd starts a job again when it is killed: remove it first.
  if command -v launchctl >/dev/null; then
    launchctl list | awk -v p="agentd-e2e-$RUN-" 'index($3, p) == 1 { print $3 }' | while read -r job; do launchctl remove "$job" 2>/dev/null || true; done
  fi
  pkill -KILL -f "/agentd-e2e-$RUN-" 2>/dev/null || true
}
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  end_machines
  rm -rf "$WORK"
}
trap cleanup EXIT

RPORT="$(free_port)"
APORT="$(free_port)"
AGENTD_URL="http://127.0.0.1:$RPORT"
APP_URL="http://127.0.0.1:$APORT"

# The release: daemon 0.0.1 (the Go engine of every platform; no tray: the scenario does not look at it) and the server.
VERSION=0.0.1 RELEASE_URL="$AGENTD_URL" TRAY_DIR="$WORK/empty-tray" ./scripts/build-daemon.sh "$DIST" >&2
go build -o "$WORK/agentd-server" ./cmd/agentd-server
go build -o "$WORK/fake-agent" ./cmd/fake-agent
"$WORK/agentd-server" -addr "127.0.0.1:$RPORT" -dist "$DIST" >"$WORK/server.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 40); do curl -s -o /dev/null "$AGENTD_URL/healthz" && break; sleep 0.25; done

# The app: built the way any app builds against agentd.
./scripts/build-example.sh "$WORK/example"
"$WORK/example/minimal-app" -addr "127.0.0.1:$APORT" -static "$WORK/example/static" -agentd-url "$AGENTD_URL" -agentd-ports "$GATEWAY_BASE-$((GATEWAY_BASE + 9))" >"$WORK/app.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 40); do curl -s -o /dev/null "$APP_URL/" && break; sleep 0.25; done

SED_PATH="${PATH//\\/\\\\}"; SED_PATH="${SED_PATH//&/\\&}"
status=0
for scenario in e2e/${1:-*}.mjs; do
  case "$scenario" in e2e/lib/*) continue ;; esac
  echo "== $scenario"
  cat e2e/lib/prelude.mjs "$scenario" | sed -e "s#__APP_URL__#$APP_URL#g" -e "s#__AGENTD_URL__#$AGENTD_URL#g" -e "s#__DIST__#$DIST#g" -e "s#__EMPTY_TRAY__#$WORK/empty-tray#g" -e "s#__ROOT__#$ROOT#g" -e "s#__WORK__#$WORK#g" \
    -e "s#__RUN__#$RUN#g" -e "s#__GATEWAY_BASE__#$GATEWAY_BASE#g" -e "s#__SHOTS__#${E2E_SHOTS:-}#g" -e "s#__FAKE_AGENT__#$WORK/fake-agent#g" -e "s#__PATH__#$SED_PATH#g" | ego-browser nodejs || { status=1; echo "-- server log (tail)"; tail -n 15 "$WORK/server.log"; echo "-- app log (tail)"; tail -n 15 "$WORK/app.log"; }
  # A scenario that throws midway never reaches task.finish: close the spaces this run opened.
  sed "s#__RUN__#$RUN#g" <<'JS' | ego-browser nodejs >/dev/null 2>&1 || true
for (const s of await listTaskSpaces()) {
  if (s.name?.endsWith(" __RUN__") && s.ownership === "agent") await (await taskSpace(s.spaceId ?? s.id)).finish({ keep: [] });
}
JS
  end_machines
done
exit "$status"
