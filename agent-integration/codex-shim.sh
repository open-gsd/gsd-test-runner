#!/bin/sh
# codex-shim.sh — POSIX sh exec-path shim for Codex agents.
#
# Mirrors the Claude Code PreToolUse hook in route-tests.mjs (ADR-0021 §G,
# issue #60). Place on PATH before the real node/npm so Codex picks it up.
#
# Usage: codex-shim.sh <cmd> [args...]
#   If <cmd> looks like a node test invocation, print a routing message on
#   stderr and exit non-zero so Codex knows the command was blocked.
#   Otherwise, exec the original command unchanged.
#
# POSIX-portable: no bashisms, no arrays, no [[ ]].

CMD="$*"

# Match node --test (with any leading env vars stripped heuristically).
# We strip "KEY=value " prefixes before testing.
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
  printf >&2 '[gsd-test codex-shim] BLOCKED: "%s"\n' "$CMD"
  printf >&2 'Local node --test / npm test is intercepted (ADR-0021 §G, issue #60).\n'
  printf >&2 'Orphaned node processes risk wedging the Dev Workstation.\n'
  printf >&2 'Route via the gsd-test front door instead:\n'
  printf >&2 '  gsd-test submit --spec-file -\n'
  printf >&2 'Pipe a JSON run spec on stdin (see agent-integration/README.md).\n'
  exit 1
fi

exec "$@"
