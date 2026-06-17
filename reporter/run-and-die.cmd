@echo off
REM run-and-die container entrypoint (issue #60, ADR-0021) — Windows counterpart
REM of run-and-die.sh. Installs deps + builds (when package.json is present)
REM before handing off to the watchdog so the deadline times only the test phase
REM (ADR-0021 Decision 1). Unverified until a Windows Bench is available.
REM stdout is reserved for the watchdog's JSON envelope; keep npm chatter on stderr.
if exist package.json (
  echo gsd-test: installing dependencies ^(npm ci^) 1>&2
  call npm ci 1>&2 || exit /b 1
  echo gsd-test: building ^(npm run build --if-present^) 1>&2
  call npm run build --if-present 1>&2
)
REM Per-test leak detection (ADR-0021 §F): preload the probe into each node --test child.
if not defined GSD_LEAK_DIR set GSD_LEAK_DIR=%TEMP%\gsd-leaks
set NODE_OPTIONS=--import C:/opt/gsd-test/leak-probe.mjs %NODE_OPTIONS%
node C:\opt\gsd-test\watchdog.mjs %*
