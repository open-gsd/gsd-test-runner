#!/bin/sh
# codex-shim.sh — POSIX sh test-interception shim for Codex agents (ADR-0022,
# issue #60/#65/#78).
#
# Two invocation modes, both supported:
#   1. PATH shim    — installed as `node` / `npm` on Codex's exec PATH (the
#      installer's codex-bin wrappers do this). Codex runs `node --test …`; this
#      shim is what actually runs.
#   2. Command wrapper — invoked as `codex-shim.sh <cmd> [args…]`.
#
# If the command is a `node --test` / `npm test` invocation it is REDIRECTED to
# `gsd-test run` — which runs the suite in a disposable Docker container under
# the run-and-die watchdog and returns a node:test verdict — so no orphan-prone
# local node test process is ever spawned. Every other command is passed through
# to the REAL binary, found by scanning PATH while skipping this shim's own
# directory and $GSD_SHIM_DIR (so a `node`/`npm` PATH shim never recurses).
#
# POSIX-portable: no bashisms, no arrays, no [[ ]].

# Determine the command being run. As a PATH shim, argv0's basename is node/npm;
# as a wrapper, the first argument names the command.
prog=$(basename -- "$0")
case "$prog" in
  node|npm)
    cmd=$prog ;;
  *)
    cmd=${1:-}
    [ "$#" -gt 0 ] && shift ;;
esac

# Reconstruct the full command (strip leading "KEY=value " env prefixes) and
# decide whether it is a test invocation.
full="$cmd $*"
bare=$(printf '%s\n' "$full" | sed 's/^[A-Z_][A-Z0-9_]*=[^ ]* //g')
is_test=0
case "$bare" in
  "node --test"|"node --test "*|"node --test-"*) is_test=1 ;;
  "npm test"|"npm test "*|"npm t"|"npm t "*|"npm run test"|"npm run test "*) is_test=1 ;;
esac

if [ "$is_test" -eq 1 ]; then
  # Notify (ADR-0022 Decision 4) so the agent knows the run moved to Docker and
  # does not re-run it locally or treat the wait as a hang.
  printf >&2 '↪ gsd-test: handing off "%s" to Docker via `gsd-test run` (ADR-0022) — do not re-run locally\n' "$full"

  # Forward only test-file path patterns; node --test flags (--test-timeout,
  # --test-force-exit, …) do not apply to `gsd-test run` — the watchdog supplies
  # its own hardening. npm test carries no paths, so the whole suite runs.
  patterns=""
  for a in "$@"; do
    case "$a" in
      *.test.mjs|*.test.cjs|*.test.js) patterns="$patterns $a" ;;
    esac
  done

  # shellcheck disable=SC2086  # intentional word-splitting of collected patterns
  exec gsd-test run $patterns
fi

# Passthrough: exec the REAL cmd. Scan PATH, skipping this shim's own directory
# and $GSD_SHIM_DIR (the codex-bin dir the install wrappers export), so a shim
# named `node`/`npm` resolves the genuine binary instead of itself. Directories
# are CANONICALISED (cd+pwd) before comparison so symlink/alias differences
# (e.g. /var vs /private/var on macOS, or a trailing slash) can't defeat the
# skip and cause infinite recursion.
canon() { CDPATH= cd -- "$1" 2>/dev/null && pwd -P; }
self_dir=$(canon "$(dirname -- "$0")")
shim_dir=$(canon "${GSD_SHIM_DIR:-/nonexistent-gsd-shim-dir}")
real=""
old_ifs=$IFS
IFS=:
for d in $PATH; do
  [ -z "$d" ] && d=.
  cd_d=$(canon "$d") || cd_d=$d
  [ -n "$cd_d" ] && [ "$cd_d" = "$self_dir" ] && continue
  [ -n "$cd_d" ] && [ "$cd_d" = "$shim_dir" ] && continue
  if [ -x "$d/$cmd" ]; then
    real="$d/$cmd"
    break
  fi
done
IFS=$old_ifs

if [ -n "$real" ]; then
  exec "$real" "$@"
fi
# Fallback: cmd not found on a non-shimmed PATH entry. Do NOT blindly re-exec by
# name (that would re-hit this shim and recurse); report and fail loudly.
printf >&2 'gsd-test codex-shim: could not find the real %s on PATH (outside the shim dir)\n' "$cmd"
exit 127
