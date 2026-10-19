#!/bin/sh
# agentd: the launcher. install.sh copies it to ~/.agentd/bin/agentd; a service manager
# runs it as `agentd run`.
#
#   - puts a Node.js that install.sh downloaded (~/.agentd/node) first on PATH,
#   - runs `node ~/.agentd/daemon/current/daemon.js "$@"`,
#   - keeps the previous version: when a version exits non-zero within 30 s of its start, twice in a row, `current`
#     points back at the previous one and the daemon does not update to the bad version again (daemon/rejected),
#   - starts the daemon again at once when it exits with 75 (a Restart from the web UI, or an update in place),
#   - with AGENTD_RESTART=loop (no service manager) starts it again after every crash (a non-zero exit), with a
#     growing delay, and marks the daemon as managed (AGENTD_MANAGED=1; the service definitions set it too).
#
# The exit codes are the protocol (daemon/src/core/exit.ts): 0 stay stopped, 75 start again now, anything else is a
# crash. Without the loop, the exit code of the daemon is the exit code of the launcher, and the service manager
# restarts it after a crash only. pair, version, service and mcp are not supervised: they run as they are.
#
# $AGENTD_HOME replaces ~/.agentd (a test seam).
MC_HOME="${AGENTD_HOME:-$HOME/.agentd}"
D="$MC_HOME/daemon"
QUICK_SECONDS="${AGENTD_QUICK_SECONDS:-30}" # a test seam
EXIT_RESTART=75 # EXIT_RESTART in daemon/src/core/exit.ts

if [ -x "$MC_HOME/node/bin/node" ]; then
  PATH="$MC_HOME/node/bin:$PATH"
fi
export PATH

say() { printf '%s launcher: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

if [ ! -f "$D/current/daemon.js" ]; then
  say "no daemon is installed ($D/current/daemon.js is missing): run the install command from the app's web UI again"
  exit 1
fi

case "${1:-run}" in
  run | --*) ;;
  *) exec node "$D/current/daemon.js" "$@" ;;
esac

CHILD=""
STOPPING=0
trap 'STOPPING=1; if [ -n "$CHILD" ]; then kill -TERM "$CHILD" 2>/dev/null; fi' TERM INT HUP

# version_dir: the version `current` points at.
version_dir() {
  local_target="$(readlink "$D/current" 2>/dev/null || true)"
  printf '%s' "${local_target##*/}"
}

# plain_version <text>: succeeds when the text is a plain version (nothing that could leave $D, and none of $D's own
# entries, in any case: macOS file names ignore it). VERSION_PATTERN in daemon/src/core/update.ts says the same.
plain_version() {
  case "$1" in
    '' | .* | *[!0-9A-Za-z.-]*) return 1 ;;
  esac
  case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    current | previous | rejected | crashes) return 1 ;;
  esac
}

forget_crashes() { rm -f "$D/crashes"; }

# rollback <bad version>: current -> previous, and the bad version is not tried again.
rollback() {
  prev=""
  if [ -f "$D/previous" ]; then read -r prev <"$D/previous" || true; fi
  if plain_version "$prev" && [ "$prev" != "$1" ] && [ -f "$D/$prev/daemon.js" ]; then
    ln -sfn "$prev" "$D/current"
    rm -f "$D/previous" "$D/crashes"
    printf '%s\n' "$1" >"$D/rejected"
    say "version $1 crashed at start twice: back on $prev; $1 is not tried again until the server serves another version"
  fi
}

# note_crash <version>: counts a quick non-zero exit of that version; the second one in a row rolls back.
note_crash() {
  cv=""
  cn=0
  if [ -f "$D/crashes" ]; then read -r cv cn <"$D/crashes" || true; fi
  case "$cn" in '' | *[!0-9]*) cn=0 ;; esac
  if [ "$cv" = "$1" ]; then cn=$((cn + 1)); else cn=1; fi
  printf '%s %s\n' "$1" "$cn" >"$D/crashes"
  if [ "$cn" -ge 2 ]; then rollback "$1"; fi
}

# run_once <args>: one daemon process. Sets RUN_CODE and RAN (seconds).
run_once() {
  running="$(version_dir)"
  started="$(date +%s)"
  node "$D/current/daemon.js" "$@" &
  CHILD=$!
  wait "$CHILD"
  RUN_CODE=$?
  # A signal for the launcher ends `wait` early: wait again until the daemon itself is gone.
  while kill -0 "$CHILD" 2>/dev/null; do
    wait "$CHILD"
    RUN_CODE=$?
  done
  CHILD=""
  RAN=$(($(date +%s) - started))
  if [ "$STOPPING" = 1 ]; then return; fi
  if [ "$RUN_CODE" = 0 ] || [ "$RUN_CODE" = "$EXIT_RESTART" ] || [ "$RAN" -ge "$QUICK_SECONDS" ]; then
    forget_crashes
  else
    say "daemon $running exited with $RUN_CODE after $RAN s"
    note_crash "$running"
  fi
}

# pause <seconds>: sleeps, and ends early when the launcher is told to stop.
pause() {
  sleep "$1" &
  CHILD=$!
  wait "$CHILD" 2>/dev/null || true
  CHILD=""
}

LOOP=0
if [ "${AGENTD_RESTART:-}" = loop ]; then
  LOOP=1
  AGENTD_MANAGED=1
  export AGENTD_MANAGED
  mkdir -p "$MC_HOME/run"
  rm -f "$MC_HOME/run/last-exit"
  printf '%s\n' "$$" >"$MC_HOME/run/loop.pid"
  trap 'rm -f "$MC_HOME/run/loop.pid"' EXIT
fi

DELAY=1
while :; do
  run_once "$@"
  if [ "$STOPPING" = 1 ]; then break; fi
  if [ "$RUN_CODE" = "$EXIT_RESTART" ]; then
    say "starting the daemon again at once ($(version_dir))"
    DELAY=1
    continue
  fi
  if [ "$LOOP" = 0 ]; then break; fi
  printf '%s\n' "$RUN_CODE" >"$MC_HOME/run/last-exit"
  if [ "$RUN_CODE" = 0 ]; then
    say "daemon stopped (exit 0): it stays stopped; start it again with: agentd service start"
    break
  fi
  if [ "$RAN" -ge 60 ]; then DELAY=1; fi
  say "daemon exited with $RUN_CODE; starting it again in $DELAY s"
  pause "$DELAY"
  if [ "$STOPPING" = 1 ]; then break; fi
  DELAY=$((DELAY * 2))
  if [ "$DELAY" -gt 60 ]; then DELAY=60; fi
done
# Told to stop: that is a clean exit, whatever the daemon's last code was.
if [ "$STOPPING" = 1 ]; then
  if [ "$LOOP" = 1 ]; then printf '0\n' >"$MC_HOME/run/last-exit"; fi
  exit 0
fi
exit "$RUN_CODE"
