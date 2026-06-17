#!/bin/sh
# codex-shim.sh — POSIX sh exec-path shim for Codex agents (ADR-0022, issue
# #60/#65). Place on PATH before the real node/npm so Codex picks it up.
#
# Usage: codex-shim.sh <cmd> [args...]
#   If <cmd> is a `node --test` / `npm test` invocation, REDIRECT it to
#   `gsd-test run` — which runs the suite in a disposable Docker container under
#   the run-and-die watchdog and returns a node:test verdict — and exec that, so
#   no orphan-prone local node test process is ever spawned. Otherwise exec the
#   original command unchanged.
#
# POSIX-portable: no bashisms, no arrays, no [[ ]].

CMD="$*"

# Match the test invocation (strip leading "KEY=value " env prefixes first).
BARE=$(printf '%s\n' "$CMD" | sed 's/^[A-Z_][A-Z0-9_]*=[^ ]* //g')

is_node_test() {
  case "$BARE" in
    "node --test"*|"node --test-"*)
      return 0 ;;
  esac
  return 1
}

is_npm_test() {
  case "$BARE" in
    "npm test"*|"npm t"|"npm t "*|"npm run test"*)
      return 0 ;;
  esac
  return 1
}

if is_node_test || is_npm_test; then
  # Notify (ADR-0022 Decision 4) so the agent knows the run moved to Docker and
  # does not re-run it locally or treat the wait as a hang.
  printf >&2 '↪ gsd-test: handing off "%s" to Docker via `gsd-test run` (ADR-0022) — do not re-run locally\n' "$CMD"

  # Forward only test-file path patterns; node --test flags (--test-timeout,
  # --test-force-exit, …) do not apply to `gsd-test run` — the watchdog supplies
  # its own hardening. npm test carries no paths, so the whole suite runs.
  PATTERNS=""
  for a in "$@"; do
    case "$a" in
      *.test.mjs|*.test.cjs|*.test.js) PATTERNS="$PATTERNS $a" ;;
    esac
  done

  # shellcheck disable=SC2086  # intentional word-splitting of collected patterns
  exec gsd-test run $PATTERNS
fi

exec "$@"
