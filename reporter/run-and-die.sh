#!/bin/sh
# run-and-die container entrypoint (issue #60, ADR-0021).
#
# Installs dependencies and builds the project — when a package.json is present —
# BEFORE handing off to the watchdog, so the watchdog's deadline times only the
# test phase (ADR-0021 Decision 1), not npm ci / build.
#
# Robustness principle applied visibly (Postel's Law): a missing package.json or
# a missing "build" script is skipped gracefully, but a failing `npm ci` aborts
# the run loudly (set -e) before any test executes.
set -e
# stdout is reserved for the watchdog's JSON envelope; keep npm chatter on stderr.
if [ -f package.json ]; then
  echo "gsd-test: installing dependencies (npm ci)" >&2
  npm ci >&2
  echo "gsd-test: building (npm run build --if-present)" >&2
  npm run build --if-present >&2
fi
exec node /opt/gsd-test/watchdog.mjs "$@"
