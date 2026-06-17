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
# Per-test leak detection (ADR-0021 §F): preload the probe into each node --test
# child and point it at a scratch dir the watchdog reads after the run. Set after
# npm ci/build so install processes aren't probed.
export GSD_LEAK_DIR="${GSD_LEAK_DIR:-/tmp/gsd-leaks}"
export NODE_OPTIONS="--import /opt/gsd-test/leak-probe.mjs${NODE_OPTIONS:+ $NODE_OPTIONS}"
exec node /opt/gsd-test/watchdog.mjs "$@"
