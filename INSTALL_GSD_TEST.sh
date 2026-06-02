#!/usr/bin/env bash
# INSTALL_GSD_TEST.sh — install GSD test-running tools on your Mac.
#
# Installs five runnable scripts, a shared JSON reporter, and a local config:
#   ~/.local/bin/gsd-test          docker remote runner (rsync → Linux host → container)
#   ~/.local/bin/gsd-test-windows  docker remote runner (rsync → Windows host → container)
#   ~/.local/bin/gsd-test-local    local Mac runner (node --test directly)
#   ~/.local/bin/gsd-test-both     runs all platforms in parallel + prints a diff
#   ~/.local/bin/gsd-test-diff     python helper that diffs two or three JSON Lines outputs
#   ~/.local/share/gsd-test/reporter.mjs   custom JSON reporter (shared)
#   ~/.config/gsd-test/hosts       per-host config (your hostnames; never pushed)
#
# Also drops reference files in ~/projects/dev-tools/get-shit-done/ and offers
# to install the /test slash command + Stop hook into ~/.claude/.
#
# Idempotent. Safe to re-run.

set -euo pipefail

DEST_BIN="$HOME/.local/bin"
DEST_DOCS="$HOME/projects/dev-tools/get-shit-done"
DEST_SHARE="$HOME/.local/share/gsd-test"
DEST_CONFIG="$HOME/.config/gsd-test"

mkdir -p "$DEST_BIN" "$DEST_DOCS" "$DEST_DOCS/claude-commands" "$DEST_DOCS/codex-prompts" "$DEST_SHARE" "$DEST_CONFIG"

# ---------- copy installer to safe location ----------
SELF_PATH="${BASH_SOURCE[0]}"
SELF_ABS="$(cd "$(dirname "$SELF_PATH")" && pwd)/$(basename "$SELF_PATH")"
SAFE_COPY="$DEST_DOCS/INSTALL_GSD_TEST.sh"
if [[ "$SELF_ABS" != "$SAFE_COPY" ]]; then
  cp "$SELF_ABS" "$SAFE_COPY"
  chmod +x "$SAFE_COPY"
  echo "» installer copied to $SAFE_COPY"
fi

# ---------- hosts config (first-run prompt) ----------
HOSTS_FILE="$DEST_CONFIG/hosts"
WINDOWS_HOSTS_FILE="$DEST_CONFIG/windows-hosts"

if [[ ! -s "$HOSTS_FILE" ]]; then
  echo ""
  echo "» First-time setup: which Linux Docker host(s) do you want to test on?"
  echo "  Enter SSH host aliases (from ~/.ssh/config) separated by spaces."
  echo "  Example: dockerhost1 dockerhost2"
  echo ""
  read -r -p "  Hosts: " HOSTS_INPUT
  if [[ -z "$HOSTS_INPUT" ]]; then
    echo "  (empty — writing placeholder; edit $HOSTS_FILE before running gsd-test)"
    HOSTS_INPUT="dockerhost1"
  fi
  : > "$HOSTS_FILE"
  for h in $HOSTS_INPUT; do
    echo "$h" >> "$HOSTS_FILE"
  done
  echo "  saved to $HOSTS_FILE"
fi

if [[ ! -f "$WINDOWS_HOSTS_FILE" ]]; then
  echo ""
  echo "» Windows Docker host (optional): SSH alias for a Windows host running Docker"
  echo "  in Windows containers mode with OpenSSH server. Leave empty to skip Windows"
  echo "  testing. You can edit $WINDOWS_HOSTS_FILE later."
  echo ""
  read -r -p "  Windows host (or Enter to skip): " WINDOWS_HOST_INPUT
  : > "$WINDOWS_HOSTS_FILE"
  if [[ -n "$WINDOWS_HOST_INPUT" ]]; then
    echo "$WINDOWS_HOST_INPUT" >> "$WINDOWS_HOSTS_FILE"
    echo "  saved to $WINDOWS_HOSTS_FILE"
  else
    echo "  (empty — Windows runner will be skipped; edit $WINDOWS_HOSTS_FILE to enable)"
  fi
fi

# ---------- shared JSON reporter ----------
echo "» writing reporter.mjs..."
cat > "$DEST_SHARE/reporter.mjs" <<'__REPORTER_END__'
// Custom Node test reporter: emits one JSON line per test runner event.
// For test pass/fail events, emits a structured test_event record per
// ADR-0013 (schema_version=1). All other events are emitted as raw
// {type, data} lines for the drain/parse legs.
//
// Handles Error objects so message/stack survive JSON.stringify.

/**
 * Build the fully-qualified test name by walking the context chain.
 * Node's test runner attaches .context.name and .context.parent.
 * We accumulate from the root down, joining with " > ".
 */
function buildTestName(context) {
  const parts = [];
  let cur = context;
  while (cur) {
    if (cur.name) parts.unshift(cur.name);
    cur = cur.parent || null;
  }
  return parts.join(' > ');
}

/**
 * Strip the /work/ container prefix from file paths so the stored path is
 * repo-relative. Leaves non-/work/ paths untouched (handles non-container
 * runs).
 */
function repoRelative(file) {
  if (typeof file === 'string' && file.startsWith('/work/')) {
    return file.slice('/work/'.length);
  }
  return file || '';
}

/**
 * Classify an error object into one of the six ADR-0013 ErrorClass values.
 *   assertion  — AssertionError or ERR_ASSERTION code
 *   timeout    — error message contains "timed out"
 *   setup      — error thrown inside a before/beforeEach hook
 *   teardown   — error thrown inside an after/afterEach hook
 *   throw      — any other unhandled throw inside a test body
 *   unknown    — fallback
 */
function classifyError(err, hookType) {
  if (!err) return 'unknown';
  // Hook-sourced failures take priority over the error type.
  if (hookType === 'before' || hookType === 'beforeEach') return 'setup';
  if (hookType === 'after' || hookType === 'afterEach') return 'teardown';
  if (err.name === 'AssertionError' || err.code === 'ERR_ASSERTION') return 'assertion';
  if (typeof err.message === 'string' && err.message.includes('timed out')) return 'timeout';
  if (err instanceof Error || err.stack) return 'throw';
  return 'unknown';
}

export default async function* (source) {
  for await (const e of source) {
    const type = e.type;
    const data = e.data;

    if (type === 'test:pass' || type === 'test:fail') {
      const ctx = data && data.details;
      const err = ctx && ctx.error;
      const isPassing = type === 'test:pass';

      // Determine hook type if this failure originated inside a lifecycle hook.
      // Node surfaces hook failures as test:fail on the hook's synthetic test
      // node; its context.type may be "before", "after", "beforeEach", or
      // "afterEach".
      const hookType = (data && data.context && data.context.type) || null;

      const name = buildTestName(data && data.context);
      const file = repoRelative(data && data.file);
      const durationMs = (ctx && typeof ctx.duration === 'number') ? ctx.duration : 0;
      const retryCount = (data && typeof data.currentAttempt === 'number')
        ? Math.max(0, data.currentAttempt - 1)
        : 0;

      const record = {
        type: 'test_event',
        kind: isPassing ? 'pass' : 'fail',
        file,
        name,
        duration_ms: durationMs,
        retry_count: retryCount,
      };

      if (!isPassing && err) {
        const errMsg = (typeof err.message === 'string') ? err.message.split('\n')[0] : String(err);
        record.error = errMsg;
        record.error_class = classifyError(err, hookType);
        record.stack = (err && err.stack) ? err.stack : '';
        // Captured output lives on data.details.output in newer Node versions.
        record.output = (ctx && typeof ctx.output === 'string') ? ctx.output : '';
      }

      yield JSON.stringify(record) + '\n';
    } else {
      // All other event types (test:diagnostic, test:plan, test:start, etc.)
      // are passed through verbatim for forward compatibility.
      yield JSON.stringify({ type, data }, (k, v) =>
        v instanceof Error
          ? { name: v.name, message: v.message, stack: v.stack, code: v.code, ...v }
          : v
      ) + '\n';
    }
  }
}
__REPORTER_END__

# ---------- gsd-test (Docker remote runner) ----------
echo "» writing gsd-test..."
cat > "$DEST_BIN/gsd-test" <<'__GSDTEST_END__'
#!/usr/bin/env bash
# gsd-test — run get-shit-done tests in an ephemeral remote Docker container.
#
# Syncs the working tree to a persistent mirror dir on a host listed in
# ~/.config/gsd-test/hosts via rsync (delta after first run), then runs
# `node --test` in a one-shot container that mounts the mirror as /work
# plus a persistent named volume for /root/.npm. Returns JSON Lines on
# stdout, progress on stderr.
#
# Usage:
#   gsd-test                            # all tests, auto-pick host
#   gsd-test --host plex2               # pin a host
#   gsd-test tests/foo.test.cjs         # run specific test file(s)
#   gsd-test --no-build                 # skip build:sdk
#   gsd-test --reset                    # wipe mirror + npm cache
#   gsd-test --verbose                  # forward npm/build chatter to stderr
#   gsd-test --quiet                    # suppress progress lines
#   gsd-test --help

set -euo pipefail

CONFIG_DIR="$HOME/.config/gsd-test"
HOSTS_FILE="$CONFIG_DIR/hosts"
REPORTER="$HOME/.local/share/gsd-test/reporter.mjs"
IMAGE="gsd-test:node22"
MIRROR_DIR="gsd-mirror-get-shit-done"
NPM_CACHE_VOLUME="gsd-npm-cache"

PINNED_HOST=""
QUIET=false
VERBOSE=false
SKIP_BUILD=false
RESET=false
PASSTHROUGH=()

show_usage() {
  cat <<'USAGE_END' >&2
Usage:
  gsd-test                            run all tests, auto-pick a host
  gsd-test --host <name>              pin to a host from your hosts file
  gsd-test tests/foo.test.cjs ...     run specific test file(s)
  gsd-test --no-build                 skip npm run build:sdk
  gsd-test --reset                    wipe remote mirror + npm cache
  gsd-test --verbose                  forward npm/build chatter to stderr
  gsd-test --quiet                    suppress progress messages
  gsd-test --help                     this help

Hosts are loaded from ~/.config/gsd-test/hosts (one per line).
USAGE_END
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)        PINNED_HOST="${2:?--host requires a value}"; shift 2 ;;
    --quiet|-q)    QUIET=true; shift ;;
    --verbose|-v)  VERBOSE=true; shift ;;
    --no-build)    SKIP_BUILD=true; shift ;;
    --reset)       RESET=true; shift ;;
    --help|-h)     show_usage; exit 0 ;;
    --)            shift; PASSTHROUGH+=("$@"); break ;;
    *)             PASSTHROUGH+=("$1"); shift ;;
  esac
done

log() { $QUIET || echo "» $*" >&2; }

# Load hosts
if [[ ! -f "$HOSTS_FILE" ]]; then
  echo "ERROR: $HOSTS_FILE not found. Re-run INSTALL_GSD_TEST.sh to set it up." >&2
  exit 2
fi
HOSTS=()
while IFS= read -r line; do
  case "$line" in ''|\#*) continue ;; esac
  HOSTS+=("$line")
done < "$HOSTS_FILE"
if [[ ${#HOSTS[@]} -eq 0 ]]; then
  echo "ERROR: $HOSTS_FILE is empty. Add at least one SSH host alias (one per line)." >&2
  exit 2
fi

PROJECT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [[ ! -f "$PROJECT_DIR/package.json" ]]; then
  echo "ERROR: no package.json at $PROJECT_DIR — run from inside a Node project." >&2
  exit 2
fi

pick_host() {
  if [[ -n "$PINNED_HOST" ]]; then
    echo "$PINNED_HOST"
    return
  fi
  local h
  for h in $(printf "%s\n" "${HOSTS[@]}" | sort -R); do
    if ssh -o ConnectTimeout=3 -o BatchMode=yes "$h" true 2>/dev/null; then
      echo "$h"
      return
    fi
  done
  echo "ERROR: no reachable docker host (tried: ${HOSTS[*]})" >&2
  exit 3
}

HOST="$(pick_host)"
log "host=$HOST  project=$PROJECT_DIR"

# Resolve the remote user's uid/gid/login once — used both for the
# pre-rsync poisoned-mirror probe and for the in-container chown-back.
REMOTE_IDS="$(ssh "$HOST" 'printf "%s:%s:%s" "$(id -u)" "$(id -g)" "$(id -un)"')"
REMOTE_UID="${REMOTE_IDS%%:*}"
REMOTE_REST="${REMOTE_IDS#*:}"
REMOTE_GID="${REMOTE_REST%%:*}"
REMOTE_USER="${REMOTE_REST#*:}"

if $RESET; then
  log "resetting mirror + npm cache on $HOST..."
  ssh "$HOST" "rm -rf ~/$MIRROR_DIR; docker volume rm $NPM_CACHE_VOLUME 2>/dev/null || true; mkdir -p ~/$MIRROR_DIR"
else
  ssh "$HOST" "mkdir -p ~/$MIRROR_DIR"
fi

# Unstick poisoned mirrors. A previous run on this host may have written
# root-owned artifacts into ~/$MIRROR_DIR (any container that ran without
# --user and without chown-back-before-exec). The next rsync from local
# would fail with "mkstemp ... Permission denied" because the leaf files
# are root-owned even though the parent dir is owned by $REMOTE_USER.
#
# Probe cheaply; only invoke the privileged cleanup container when needed.
if ssh "$HOST" "find ~/$MIRROR_DIR -maxdepth 4 ! -user $REMOTE_USER -print -quit 2>/dev/null | grep -q ." 2>/dev/null; then
  log "mirror has non-$REMOTE_USER files on $HOST — running one-shot chown..."
  ssh "$HOST" "docker run --rm -v \$HOME/$MIRROR_DIR:/work $IMAGE \
    chown -R $REMOTE_UID:$REMOTE_GID /work" >/dev/null
fi

log "rsync → $HOST:~/$MIRROR_DIR"
rsync -az --delete --force \
  --exclude='node_modules' \
  --exclude='.git' \
  --exclude='.claude' \
  --exclude='.vscode' \
  --exclude='.idea' \
  --exclude='coverage' \
  --exclude='.cache' \
  --exclude='.env' \
  --exclude='.env.*' \
  --exclude='.DS_Store' \
  --exclude='local-dev-tools' \
  --exclude='._*' \
  -e ssh \
  "$PROJECT_DIR/" "$HOST:~/$MIRROR_DIR/"

if $VERBOSE; then NPM_FLAGS=""; else NPM_FLAGS="--silent"; fi

if $SKIP_BUILD; then
  BUILD_BLOCK="echo '» skipping build:sdk + lint:skill-deps (--no-build)' 1>&2"
else
  BUILD_BLOCK="echo '» build:sdk...' 1>&2
npm run build:sdk --if-present $NPM_FLAGS 1>&2
echo '» lint:skill-deps...' 1>&2
npm run lint:skill-deps --if-present $NPM_FLAGS 1>&2"
fi

TEST_TARGETS="tests/*.test.cjs"
if [[ ${#PASSTHROUGH[@]} -gt 0 ]]; then
  TEST_TARGETS=""
  for arg in "${PASSTHROUGH[@]}"; do
    TEST_TARGETS+=" $(printf '%q' "$arg")"
  done
fi

REMOTE_SCRIPT=$(cat <<REMOTE_END
set -e
cd /work
cat > /tmp/reporter.mjs <<'JSONREPORTER_END'
export default async function* (source) {
  for await (const e of source) {
    yield JSON.stringify({ type: e.type, data: e.data }, (k, v) =>
      v instanceof Error ? { name: v.name, message: v.message, stack: v.stack, code: v.code, ...v } : v
    ) + '\n';
  }
}
JSONREPORTER_END
echo '» npm ci...' 1>&2
npm ci --no-audit --no-fund $NPM_FLAGS 1>&2
$BUILD_BLOCK
echo '» running tests...' 1>&2
# Chown the mirror back to the host user before exec. The container runs
# as root, so npm ci / build artifacts land as root and would poison the
# bind-mounted mirror for the next rsync. Doing it before exec (not via
# a trap) keeps the cleanup synchronous with normal exits; abnormal exits
# are caught by the pre-rsync probe on the next run.
chown -R $REMOTE_UID:$REMOTE_GID /work 2>/dev/null || true
exec node --test --test-reporter=/tmp/reporter.mjs --test-concurrency=4 $TEST_TARGETS
REMOTE_END
)

SCRIPT_B64="$(printf '%s' "$REMOTE_SCRIPT" | base64 | tr -d '\n')"
CONTAINER_BOOT="echo $SCRIPT_B64 | base64 -d > /tmp/run.sh && exec bash /tmp/run.sh"

log "running tests in container..."
ssh "$HOST" "docker run --rm -i -v ~/$MIRROR_DIR:/work -v $NPM_CACHE_VOLUME:/root/.npm $IMAGE bash -c '$CONTAINER_BOOT'"
__GSDTEST_END__
chmod +x "$DEST_BIN/gsd-test"

# ---------- gsd-test-windows (Windows Docker remote runner) ----------
echo "» writing gsd-test-windows..."
cat > "$DEST_BIN/gsd-test-windows" <<'__GSDWINDOWS_END__'
#!/usr/bin/env bash
# gsd-test-windows — run get-shit-done tests in an ephemeral Windows Docker container.
#
# Syncs the working tree to a Windows host via rsync over SSH (assumes the
# Windows host has OpenSSH server + rsync available via WSL or Cygwin/MSYS2),
# then runs `node --test` in a one-shot Windows container. Returns JSON Lines
# on stdout, progress on stderr.
#
# TODO: If rsync-over-SSH to Windows is not available in your environment,
# the alternative is to use docker --context with docker cp instead of rsync.
# Document your host's rsync setup in ~/.config/gsd-test/windows-hosts comments.
#
# Usage:
#   gsd-test-windows                            # all tests
#   gsd-test-windows --host winhost1            # pin a host
#   gsd-test-windows tests/foo.test.cjs         # run specific test file(s)
#   gsd-test-windows --no-build                 # skip build:sdk
#   gsd-test-windows --reset                    # wipe mirror
#   gsd-test-windows --verbose                  # forward npm/build chatter to stderr
#   gsd-test-windows --quiet                    # suppress progress lines
#   gsd-test-windows --help

set -euo pipefail

CONFIG_DIR="$HOME/.config/gsd-test"
WINDOWS_HOSTS_FILE="$CONFIG_DIR/windows-hosts"
IMAGE="gsd-test:node22-win"
MIRROR_DIR="gsd-mirror-get-shit-done"
NPM_CACHE_VOLUME="gsd-npm-cache-win"

PINNED_HOST=""
QUIET=false
VERBOSE=false
SKIP_BUILD=false
RESET=false
PASSTHROUGH=()

show_usage() {
  cat <<'USAGE_END' >&2
Usage:
  gsd-test-windows                        run all tests, auto-pick a host
  gsd-test-windows --host <name>          pin to a specific host
  gsd-test-windows tests/foo.test.cjs     run specific test file(s)
  gsd-test-windows --no-build             skip npm run build:sdk
  gsd-test-windows --reset                wipe remote mirror
  gsd-test-windows --verbose              forward npm/build chatter to stderr
  gsd-test-windows --quiet                suppress progress messages
  gsd-test-windows --help                 this help

Hosts are loaded from ~/.config/gsd-test/windows-hosts (one per line).
USAGE_END
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)        PINNED_HOST="${2:?--host requires a value}"; shift 2 ;;
    --quiet|-q)    QUIET=true; shift ;;
    --verbose|-v)  VERBOSE=true; shift ;;
    --no-build)    SKIP_BUILD=true; shift ;;
    --reset)       RESET=true; shift ;;
    --help|-h)     show_usage; exit 0 ;;
    --)            shift; PASSTHROUGH+=("$@"); break ;;
    *)             PASSTHROUGH+=("$1"); shift ;;
  esac
done

log() { $QUIET || echo "» $*" >&2; }

if [[ ! -f "$WINDOWS_HOSTS_FILE" ]]; then
  echo "ERROR: $WINDOWS_HOSTS_FILE not found. Re-run INSTALL_GSD_TEST.sh to set it up." >&2
  exit 2
fi

HOSTS=()
while IFS= read -r line; do
  case "$line" in ''|\#*) continue ;; esac
  HOSTS+=("$line")
done < "$WINDOWS_HOSTS_FILE"

if [[ ${#HOSTS[@]} -eq 0 ]]; then
  echo "ERROR: $WINDOWS_HOSTS_FILE is empty. Add a Windows SSH host alias." >&2
  exit 2
fi

PROJECT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [[ ! -f "$PROJECT_DIR/package.json" ]]; then
  echo "ERROR: no package.json at $PROJECT_DIR — run from inside a Node project." >&2
  exit 2
fi

pick_host() {
  if [[ -n "$PINNED_HOST" ]]; then
    echo "$PINNED_HOST"
    return
  fi
  local h
  for h in $(printf "%s\n" "${HOSTS[@]}" | sort -R); do
    if ssh -o ConnectTimeout=3 -o BatchMode=yes "$h" true 2>/dev/null; then
      echo "$h"
      return
    fi
  done
  echo "ERROR: no reachable Windows docker host (tried: ${HOSTS[*]})" >&2
  exit 3
}

HOST="$(pick_host)"
log "host=$HOST  project=$PROJECT_DIR"

if $RESET; then
  log "resetting mirror on $HOST..."
  ssh "$HOST" "rm -rf ~/$MIRROR_DIR; docker volume rm $NPM_CACHE_VOLUME 2>/dev/null || true; mkdir -p ~/$MIRROR_DIR"
else
  ssh "$HOST" "mkdir -p ~/$MIRROR_DIR"
fi

log "rsync → $HOST:~/$MIRROR_DIR"
# rsync over SSH to a Windows host requires rsync on the remote side
# (available via WSL2, Cygwin, or MSYS2 in the OpenSSH server's PATH).
rsync -az --delete --force \
  --exclude='node_modules' \
  --exclude='.git' \
  --exclude='.claude' \
  --exclude='.vscode' \
  --exclude='.idea' \
  --exclude='coverage' \
  --exclude='.cache' \
  --exclude='.env' \
  --exclude='.env.*' \
  --exclude='.DS_Store' \
  --exclude='local-dev-tools' \
  --exclude='._*' \
  -e ssh \
  "$PROJECT_DIR/" "$HOST:~/$MIRROR_DIR/"

if $VERBOSE; then NPM_FLAGS=""; else NPM_FLAGS="--silent"; fi

if $SKIP_BUILD; then
  BUILD_BLOCK='Write-Host "» skipping build:sdk + lint:skill-deps (--no-build)" -ForegroundColor Cyan'
else
  BUILD_BLOCK='Write-Host "» build:sdk..." -ForegroundColor Cyan
npm run build:sdk --if-present '"$NPM_FLAGS"' 2>&1 | Out-Host
Write-Host "» lint:skill-deps..." -ForegroundColor Cyan
npm run lint:skill-deps --if-present '"$NPM_FLAGS"' 2>&1 | Out-Host'
fi

TEST_TARGETS="tests/*.test.cjs"
if [[ ${#PASSTHROUGH[@]} -gt 0 ]]; then
  TEST_TARGETS=""
  for arg in "${PASSTHROUGH[@]}"; do
    TEST_TARGETS+=" $(printf '%q' "$arg")"
  done
fi

# The reporter script is embedded inline inside the PowerShell heredoc because
# Windows containers don't have a shared volume path from the Mac host. We
# write it to a temp location inside the container at runtime.
REPORTER_CONTENT='export default async function* (source) {
  for await (const e of source) {
    yield JSON.stringify({ type: e.type, data: e.data }, (k, v) =>
      v instanceof Error ? { name: v.name, message: v.message, stack: v.stack, code: v.code, ...v } : v
    ) + "\\n";
  }
}'

REPORTER_B64="$(printf '%s' "$REPORTER_CONTENT" | base64 | tr -d '\n')"

# The mirror dir on the Windows host is passed via env var so the
# PowerShell script can translate it to a Windows path.
MIRROR_WIN_PATH="C:\\work"

REMOTE_SCRIPT=$(cat <<REMOTE_END
Set-StrictMode -Version Latest
\$ErrorActionPreference = 'Stop'

\$reporterB64 = '$REPORTER_B64'
\$reporterBytes = [Convert]::FromBase64String(\$reporterB64)
[System.IO.File]::WriteAllBytes('C:\\reporter.mjs', \$reporterBytes)

Set-Location C:\\work
Write-Host '» npm ci...' -ForegroundColor Cyan
npm ci --no-audit --no-fund $NPM_FLAGS 2>&1 | Out-Host
$BUILD_BLOCK
Write-Host '» running tests...' -ForegroundColor Cyan
node --test --test-reporter=C:\\reporter.mjs --test-concurrency=4 $TEST_TARGETS
REMOTE_END
)

SCRIPT_B64="$(printf '%s' "$REMOTE_SCRIPT" | base64 | tr -d '\n')"

# --isolation=process is required when the container image OS matches the
# host OS (both Windows Server Core). Hyper-V isolation requires the host
# to have Hyper-V enabled which is not guaranteed on Windows Server hosts.
log "running tests in Windows container..."
ssh "$HOST" "docker run --rm -i --isolation=process \
  -v \$HOME/$MIRROR_DIR:C:\\work \
  -v ${NPM_CACHE_VOLUME}:C:\\npmcache \
  -e NPM_CONFIG_CACHE=C:\\npmcache \
  $IMAGE \
  powershell -NoProfile -EncodedCommand $SCRIPT_B64"
__GSDWINDOWS_END__
chmod +x "$DEST_BIN/gsd-test-windows"

# ---------- gsd-test-local (Mac native runner) ----------
echo "» writing gsd-test-local..."
cat > "$DEST_BIN/gsd-test-local" <<'__GSDLOCAL_END__'
#!/usr/bin/env bash
# gsd-test-local — run the GSD test suite on this Mac with JSON Lines output.
#
# Mirrors npm test's pretest chain (build:sdk + lint:skill-deps) so the
# tested code matches what `npm test` would test. Then runs node --test
# with the shared JSON reporter, producing the same output format as
# the dockerized runner so they can be diffed.
#
# Usage:
#   gsd-test-local                       all tests
#   gsd-test-local tests/foo.test.cjs    specific file(s)
#   gsd-test-local --no-build            skip build:sdk
#   gsd-test-local --verbose             forward npm/build chatter to stderr
#   gsd-test-local --quiet               suppress progress messages
#   gsd-test-local --help

set -euo pipefail

REPORTER="$HOME/.local/share/gsd-test/reporter.mjs"

QUIET=false
VERBOSE=false
SKIP_BUILD=false
PASSTHROUGH=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --quiet|-q)    QUIET=true; shift ;;
    --verbose|-v)  VERBOSE=true; shift ;;
    --no-build)    SKIP_BUILD=true; shift ;;
    --help|-h)
      cat <<'USAGE_END' >&2
Usage: gsd-test-local [--no-build] [--verbose] [--quiet] [test-file...]
Runs Node tests locally on the Mac, emitting JSON Lines to stdout.
USAGE_END
      exit 0 ;;
    --reset) shift ;;
    --both)  shift ;;
    *) PASSTHROUGH+=("$1"); shift ;;
  esac
done

log() { $QUIET || echo "» $*" >&2; }

if [[ ! -f "$REPORTER" ]]; then
  echo "ERROR: reporter missing at $REPORTER. Re-run INSTALL_GSD_TEST.sh." >&2
  exit 2
fi

PROJECT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [[ ! -f "$PROJECT_DIR/package.json" ]]; then
  echo "ERROR: no package.json at $PROJECT_DIR" >&2
  exit 2
fi
cd "$PROJECT_DIR"

if $VERBOSE; then NPM_FLAGS=""; else NPM_FLAGS="--silent"; fi

if [[ ! -d node_modules ]]; then
  log "node_modules missing → npm ci..."
  npm ci --no-audit --no-fund $NPM_FLAGS >&2
fi

if ! $SKIP_BUILD; then
  log "build:sdk..."
  npm run build:sdk --if-present $NPM_FLAGS >&2
  log "lint:skill-deps..."
  npm run lint:skill-deps --if-present $NPM_FLAGS >&2
fi

if [[ ${#PASSTHROUGH[@]} -gt 0 ]]; then
  TARGETS=("${PASSTHROUGH[@]}")
else
  TARGETS=(tests/*.test.cjs)
fi

log "running tests..."
exec node --test --test-reporter="$REPORTER" --test-concurrency=4 "${TARGETS[@]}"
__GSDLOCAL_END__
chmod +x "$DEST_BIN/gsd-test-local"

# ---------- gsd-test-both (parallel runner + diff) ----------
echo "» writing gsd-test-both..."
cat > "$DEST_BIN/gsd-test-both" <<'__GSDBOTH_END__'
#!/usr/bin/env bash
# gsd-test-both — run the test suite on all platforms in parallel,
# save all JSON Lines outputs, and print a comparison diff at the end.
#
# Platforms:
#   Mac native   → gsd-test-local
#   Linux Docker → gsd-test
#   Windows Docker → gsd-test-windows  (skipped if GSD_WINDOWS_HOST unset and
#                                        ~/.config/gsd-test/windows-hosts is empty)
#
# Usage:
#   gsd-test-both                       run all platforms, summarize diff
#   gsd-test-both --no-build            skip build:sdk on all platforms
#   gsd-test-both tests/foo.test.cjs    specific file(s) on all platforms
#
# Output files (per-invocation PID suffix prevents interleaving when run from
# multiple worktrees concurrently):
#   /tmp/gsd-test-local-<pid>.jsonl
#   /tmp/gsd-test-docker-<pid>.jsonl
#   /tmp/gsd-test-windows-<pid>.jsonl   (when Windows runner is active)
#   /tmp/gsd-test-local-<pid>.err
#   /tmp/gsd-test-docker-<pid>.err
#   /tmp/gsd-test-windows-<pid>.err

set -uo pipefail

LOCAL_OUT="${LOCAL_OUT:-/tmp/gsd-test-local-$$.jsonl}"
DOCKER_OUT="${DOCKER_OUT:-/tmp/gsd-test-docker-$$.jsonl}"
WINDOWS_OUT="${WINDOWS_OUT:-/tmp/gsd-test-windows-$$.jsonl}"
LOCAL_ERR="${LOCAL_ERR:-/tmp/gsd-test-local-$$.err}"
DOCKER_ERR="${DOCKER_ERR:-/tmp/gsd-test-docker-$$.err}"
WINDOWS_ERR="${WINDOWS_ERR:-/tmp/gsd-test-windows-$$.err}"

# Determine if Windows runner is configured
WINDOWS_HOSTS_FILE="$HOME/.config/gsd-test/windows-hosts"
RUN_WINDOWS=false
if [[ -s "$WINDOWS_HOSTS_FILE" ]]; then
  while IFS= read -r _line; do
    case "$_line" in ''|\#*) continue ;; esac
    RUN_WINDOWS=true
    break
  done < "$WINDOWS_HOSTS_FILE"
fi

echo "» local → $LOCAL_OUT" >&2
echo "» docker → $DOCKER_OUT" >&2
if $RUN_WINDOWS; then
  echo "» windows → $WINDOWS_OUT" >&2
else
  echo "» Windows runner skipped (GSD_WINDOWS_HOST unset)" >&2
fi

gsd-test-local "$@" > "$LOCAL_OUT" 2>"$LOCAL_ERR" &
LPID=$!
gsd-test "$@" > "$DOCKER_OUT" 2>"$DOCKER_ERR" &
DPID=$!

WPID=""
if $RUN_WINDOWS; then
  gsd-test-windows "$@" > "$WINDOWS_OUT" 2>"$WINDOWS_ERR" &
  WPID=$!
fi

wait $LPID; LRC=$?
wait $DPID; DRC=$?
WRC=0
if [[ -n "$WPID" ]]; then
  wait $WPID; WRC=$?
fi

echo "» local exit=$LRC  docker exit=$DRC$(if $RUN_WINDOWS; then echo "  windows exit=$WRC"; fi)" >&2
echo "" >&2

if [[ $LRC -ne 0 && $LRC -ne 1 ]]; then
  echo "--- local stderr (last 20 lines) ---" >&2
  tail -20 "$LOCAL_ERR" >&2 || true
fi
if [[ $DRC -ne 0 && $DRC -ne 1 ]]; then
  echo "--- docker stderr (last 20 lines) ---" >&2
  tail -20 "$DOCKER_ERR" >&2 || true
fi
if $RUN_WINDOWS && [[ $WRC -ne 0 && $WRC -ne 1 ]]; then
  echo "--- windows stderr (last 20 lines) ---" >&2
  tail -20 "$WINDOWS_ERR" >&2 || true
fi

if $RUN_WINDOWS; then
  gsd-test-diff "$LOCAL_OUT" "$DOCKER_OUT" "$WINDOWS_OUT"
else
  gsd-test-diff "$LOCAL_OUT" "$DOCKER_OUT"
fi
DIFF_RC=$?

{
  echo ""
  echo "» outputs:"
  echo "    local:  $LOCAL_OUT"
  echo "    docker: $DOCKER_OUT"
  if $RUN_WINDOWS; then
    echo "    windows: $WINDOWS_OUT"
  fi
} >&2

OVERALL_RC=0
for _rc in "$LRC" "$DRC" "$WRC" "$DIFF_RC"; do
  if [ -n "$_rc" ] && [ "$_rc" -ne 0 ]; then OVERALL_RC=1; fi
done
exit $OVERALL_RC
__GSDBOTH_END__
chmod +x "$DEST_BIN/gsd-test-both"

# ---------- gsd-test-diff (Python diff helper) ----------
echo "» writing gsd-test-diff..."
cat > "$DEST_BIN/gsd-test-diff" <<'__GSDDIFF_END__'
#!/usr/bin/env python3
"""
gsd-test-diff — compare two or three JSON Lines test reports and summarize the diff.

Usage:
  gsd-test-diff <local.jsonl> <linux.jsonl>
  gsd-test-diff <local.jsonl> <linux.jsonl> <windows.jsonl>

Cross-platform path normalization: '/work/tests/foo.test.cjs' (Docker) and
'/abs/path/tests/foo.test.cjs' (Mac) both reduce to 'tests/foo.test.cjs'.
Windows paths like 'C:\\work\\tests\\foo.test.cjs' are also normalized.
"""
import json
import sys
import os

def normalize_path(p):
    if not p:
        return p
    # Windows container paths: C:\work\tests\foo → tests/foo
    if p.startswith('C:\\work\\') or p.startswith('c:\\work\\'):
        return p[len('C:\\work\\'):].replace('\\', '/')
    if p.startswith('/work/'):
        return p[len('/work/'):]
    idx = p.rfind('/tests/')
    if idx >= 0:
        return p[idx+1:]
    return p

def extract_result(evt):
    """
    Normalize a parsed JSON event to (outcome, key, err) where:
      outcome  = 'test:pass' or 'test:fail'
      key      = (normalized_file, name)
      err      = first line of error message (may be empty)

    Accepts two schemas:
      1. reporter.mjs (ADR-0013): {type:'test_event', kind:'pass'|'fail',
                                    file, name, error?, ...}
      2. Raw passthrough:         {type:'test:pass'|'test:fail', data:{file,name,...}}

    Returns None if the event is not a test result.
    """
    t = evt.get('type')
    if t == 'test_event':
        kind = evt.get('kind')
        if kind not in ('pass', 'fail'):
            return None
        outcome = 'test:pass' if kind == 'pass' else 'test:fail'
        key = (normalize_path(evt.get('file', '')), evt.get('name', ''))
        err = evt.get('error', '') or ''
        # error field in reporter.mjs is already the first line of the message
        return (outcome, key, err)
    elif t in ('test:pass', 'test:fail'):
        d = evt.get('data', {}) or {}
        key = (normalize_path(d.get('file', '')), d.get('name', ''))
        err = ''
        details = d.get('details') or {}
        error = details.get('error') or {}
        if isinstance(error, dict):
            err = error.get('message', '') or ''
        return (t, key, err)
    return None

def load(path):
    results = {}
    if not os.path.exists(path):
        print(f"WARN: {path} not found", file=sys.stderr)
        return results
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                evt = json.loads(line)
            except Exception:
                continue
            r = extract_result(evt)
            if r is None:
                continue
            outcome, key, err = r
            results[key] = (outcome, err)
    return results

if len(sys.argv) not in (3, 4):
    print(__doc__, file=sys.stderr)
    sys.exit(2)

three_way = len(sys.argv) == 4

local = load(sys.argv[1])
linux = load(sys.argv[2])
windows = load(sys.argv[3]) if three_way else {}

def show(category_name, items, limit=15):
    if not items:
        return
    print(f"{category_name}:")
    for entry in items[:limit]:
        if isinstance(entry, tuple) and len(entry) == 2 and isinstance(entry[1], str):
            (file, name), err = entry
        else:
            file, name = entry
            err = ''
        print(f"  ✗ {file}  {name}")
        if err:
            first = err.splitlines()[0] if err else ''
            if len(first) > 110:
                first = first[:107] + '...'
            print(f"    → {first}")
    if len(items) > limit:
        print(f"  ... and {len(items) - limit} more")
    print()

if not three_way:
    # --- 2-platform mode (backward compatible) ---
    both_pass = 0
    both_fail = []
    mac_pass_docker_fail = []
    mac_fail_docker_pass = []
    only_mac = []
    only_docker = []

    for k in set(local.keys()) | set(linux.keys()):
        m = local.get(k)
        d = linux.get(k)
        if m is None:
            only_docker.append((k, d[1]))
        elif d is None:
            only_mac.append((k, m[1]))
        elif m[0] == 'test:pass' and d[0] == 'test:pass':
            both_pass += 1
        elif m[0] == 'test:fail' and d[0] == 'test:fail':
            both_fail.append(k)
        elif m[0] == 'test:pass' and d[0] == 'test:fail':
            mac_pass_docker_fail.append((k, d[1]))
        elif m[0] == 'test:fail' and d[0] == 'test:pass':
            mac_fail_docker_pass.append((k, m[1]))

    print("=" * 60)
    print("COMPARISON: Mac (local)  vs  Docker (Linux)")
    print("=" * 60)
    print(f"  Both passed:              {both_pass}")
    print(f"  Both failed:              {len(both_fail)}  (real bugs, fix these first)")
    print(f"  Mac fails, Docker passes: {len(mac_fail_docker_pass)}  (Mac-specific)")
    print(f"  Mac passes, Docker fails: {len(mac_pass_docker_fail)}  (Linux/container-specific)")
    print(f"  Only in Mac run:          {len(only_mac)}")
    print(f"  Only in Docker run:       {len(only_docker)}")
    print()

    show("Both failed (priority — break on every platform)", both_fail)
    show("Mac-only failures (passes on Docker, fails on Mac)", mac_fail_docker_pass)
    show("Docker-only failures (passes on Mac, fails on Docker)", mac_pass_docker_fail)
    show("Only in Mac run (missing from Docker)", only_mac)
    show("Only in Docker run (missing from Mac)", only_docker)

    if both_fail or mac_fail_docker_pass or mac_pass_docker_fail or only_mac or only_docker:
        sys.exit(1)

else:
    # --- 3-platform mode ---
    all_keys = set(local.keys()) | set(linux.keys()) | set(windows.keys())

    all_pass = 0
    all_fail = []
    local_only_fail = []
    linux_only_fail = []
    windows_only_fail = []
    local_pass_others_fail = []
    linux_pass_others_fail = []
    windows_pass_others_fail = []
    mixed_fail = []
    only_local = []
    only_linux = []
    only_windows = []

    def result(d, k):
        """Return ('test:pass'/'test:fail', err) or None if key absent."""
        return d.get(k)

    for k in all_keys:
        m = result(local, k)
        d = result(linux, k)
        w = result(windows, k)

        present = [x for x in (m, d, w) if x is not None]
        absent_count = 3 - len(present)

        # Exclusively in one platform's output
        if m is not None and d is None and w is None:
            only_local.append((k, m[1]))
            continue
        if d is not None and m is None and w is None:
            only_linux.append((k, d[1]))
            continue
        if w is not None and m is None and d is None:
            only_windows.append((k, w[1]))
            continue

        # All three present
        if m is not None and d is not None and w is not None:
            mp, dp, wp = m[0], d[0], w[0]
            if mp == 'test:pass' and dp == 'test:pass' and wp == 'test:pass':
                all_pass += 1
            elif mp == 'test:fail' and dp == 'test:fail' and wp == 'test:fail':
                all_fail.append(k)
            elif mp == 'test:fail' and dp == 'test:pass' and wp == 'test:pass':
                local_only_fail.append((k, m[1]))
            elif dp == 'test:fail' and mp == 'test:pass' and wp == 'test:pass':
                linux_only_fail.append((k, d[1]))
            elif wp == 'test:fail' and mp == 'test:pass' and dp == 'test:pass':
                windows_only_fail.append((k, w[1]))
            elif mp == 'test:pass' and dp == 'test:fail' and wp == 'test:fail':
                local_pass_others_fail.append((k, d[1]))
            elif dp == 'test:pass' and mp == 'test:fail' and wp == 'test:fail':
                linux_pass_others_fail.append((k, m[1]))
            elif wp == 'test:pass' and mp == 'test:fail' and dp == 'test:fail':
                windows_pass_others_fail.append((k, m[1]))
            else:
                mixed_fail.append(k)
            continue

        # Two platforms present — treat the absent one as not-run (ignore for pass/fail counts)
        # but still track failures on the present platforms
        if m is not None and d is not None:
            if m[0] == 'test:fail' and d[0] == 'test:fail':
                all_fail.append(k)
            elif m[0] == 'test:fail':
                local_only_fail.append((k, m[1]))
            elif d[0] == 'test:fail':
                linux_only_fail.append((k, d[1]))
            else:
                all_pass += 1
        elif m is not None and w is not None:
            if m[0] == 'test:fail' and w[0] == 'test:fail':
                all_fail.append(k)
            elif m[0] == 'test:fail':
                local_only_fail.append((k, m[1]))
            elif w[0] == 'test:fail':
                windows_only_fail.append((k, w[1]))
            else:
                all_pass += 1
        elif d is not None and w is not None:
            if d[0] == 'test:fail' and w[0] == 'test:fail':
                all_fail.append(k)
            elif d[0] == 'test:fail':
                linux_only_fail.append((k, d[1]))
            elif w[0] == 'test:fail':
                windows_only_fail.append((k, w[1]))
            else:
                all_pass += 1

    print("=" * 60)
    print("COMPARISON: Mac (local)  vs  Linux Docker  vs  Windows Docker")
    print("=" * 60)
    print(f"  All passed:                        {all_pass}")
    print(f"  All failed:                        {len(all_fail)}  (real bugs, fix these first)")
    print(f"  Mac-only failures:                 {len(local_only_fail)}")
    print(f"  Linux-only failures:               {len(linux_only_fail)}")
    print(f"  Windows-only failures:             {len(windows_only_fail)}")
    print(f"  Mac passes, others fail:           {len(local_pass_others_fail)}")
    print(f"  Linux passes, others fail:         {len(linux_pass_others_fail)}")
    print(f"  Windows passes, others fail:       {len(windows_pass_others_fail)}")
    print(f"  Mixed (some pass, some fail):      {len(mixed_fail)}")
    print(f"  Only in Mac run:                   {len(only_local)}")
    print(f"  Only in Linux run:                 {len(only_linux)}")
    print(f"  Only in Windows run:               {len(only_windows)}")
    print()

    show("All failed (priority — break on every platform)", all_fail)
    show("Mac-only failures", local_only_fail)
    show("Linux-only failures", linux_only_fail)
    show("Windows-only failures", windows_only_fail)
    show("Mac passes, Linux+Windows fail", local_pass_others_fail)
    show("Linux passes, Mac+Windows fail", linux_pass_others_fail)
    show("Windows passes, Mac+Linux fail", windows_pass_others_fail)
    show("Mixed failures (some pass, some fail)", mixed_fail)
    show("Only in Mac run (missing from others)", only_local)
    show("Only in Linux run (missing from others)", only_linux)
    show("Only in Windows run (missing from others)", only_windows)

    any_failure = (all_fail or local_only_fail or linux_only_fail or windows_only_fail
                   or local_pass_others_fail or linux_pass_others_fail or windows_pass_others_fail
                   or mixed_fail or only_local or only_linux or only_windows)
    if any_failure:
        sys.exit(1)
__GSDDIFF_END__
chmod +x "$DEST_BIN/gsd-test-diff"

# ---------- Dockerfile (for Linux remote hosts) ----------
echo "» writing Dockerfile..."
cat > "$DEST_DOCS/Dockerfile" <<'__DOCKERFILE_END__'
FROM node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends tar git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /work
ENV CI=1
ENV NODE_ENV=test
CMD ["bash"]
__DOCKERFILE_END__

# ---------- Dockerfile.windows (for Windows remote hosts) ----------
echo "» writing Dockerfile.windows..."
cat > "$DEST_DOCS/Dockerfile.windows" <<'__DOCKERFILE_WIN_END__'
# escape=`
FROM mcr.microsoft.com/windows/servercore:ltsc2022
SHELL ["powershell", "-NoProfile", "-Command", "$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue';"]

ARG NODE_VERSION=22.11.0
ARG GIT_VERSION=2.47.1

RUN Invoke-WebRequest -Uri "https://nodejs.org/dist/v$env:NODE_VERSION/node-v$env:NODE_VERSION-win-x64.zip" -OutFile node.zip ; `
    Expand-Archive node.zip -DestinationPath C:\ ; `
    Rename-Item "C:\node-v$env:NODE_VERSION-win-x64" C:\nodejs ; `
    Remove-Item node.zip

RUN Invoke-WebRequest -Uri "https://github.com/git-for-windows/git/releases/download/v$env:GIT_VERSION.windows.1/MinGit-$env:GIT_VERSION-64-bit.zip" -OutFile mingit.zip ; `
    Expand-Archive mingit.zip -DestinationPath C:\mingit ; `
    Remove-Item mingit.zip

RUN setx /M PATH "C:\nodejs;C:\mingit\cmd;$env:PATH"

WORKDIR C:\work
ENV CI=1
ENV NODE_ENV=test
# --isolation=process is required when running on Windows Server Core hosts
# without Hyper-V enabled. The image and host must have the same OS version.
CMD ["powershell"]
__DOCKERFILE_WIN_END__

# ---------- claude-commands/test.md ----------
echo "» writing claude-commands/test.md..."
cat > "$DEST_DOCS/claude-commands/test.md" <<'__SLASHCMD_END__'
---
description: Run the GSD test suite on Mac, Linux Docker, and Windows Docker (if configured), then compare results.
allowed-tools: Bash(gsd-test-both:*), Bash(gsd-test:*), Bash(gsd-test-local:*), Bash(gsd-test-windows:*), Bash(jq:*), Bash(cat:*), Bash(head:*), Bash(tail:*)
---

Run the project's tests on all configured platforms (Mac local + Linux Docker + Windows Docker)
and analyze the diff.

## Steps

1. Run `gsd-test-both`. It launches `gsd-test-local`, `gsd-test`, and (if configured)
   `gsd-test-windows` in parallel, writes JSON Lines results to per-invocation files in
   `/tmp/`, then prints a comparison summary. Windows is skipped with a notice if
   `~/.config/gsd-test/windows-hosts` is empty.

2. Read the JSON Lines files. Parse and categorize failures into:
   - **All fail** (real bugs — same failure on every platform)
   - **Mac-only fail** (passes on Docker platforms — Mac-specific issue)
   - **Linux-only fail** (passes on Mac + Windows — Linux/container-specific issue)
   - **Windows-only fail** (passes on Mac + Linux — Windows-specific issue)
   - **Missing on one platform** (test discovery differs)

3. Summarize for me:
   - Counts per category
   - For each failure type, list up to 10 with file + test name + first
     line of the error message

4. If anything is platform-specific, call those out as the most interesting findings.

5. If `gsd-test-both` exited non-zero, surface the stderr tails it printed.

## Useful jq one-liners

    # All failures on Linux:
    jq -c 'select((.type=="test:fail") or (.type=="test_event" and .kind=="fail")) | {file: (.data.file // .file), name: (.data.name // .name)}' /tmp/gsd-test-docker-*.jsonl

    # Test names grouped by file (most-broken files first):
    jq -r 'select((.type=="test:fail") or (.type=="test_event" and .kind=="fail")) | (.data.file // .file)' /tmp/gsd-test-docker-*.jsonl | sort | uniq -c | sort -rn
__SLASHCMD_END__

# ---------- codex prompt (best-effort) ----------
echo "» writing codex-prompts/test.md..."
cat > "$DEST_DOCS/codex-prompts/test.md" <<'__CODEXPROMPT_END__'
# /test — run GSD tests on all platforms and diff

Run the project tests on Mac (local), Linux Docker, and Windows Docker (if configured),
in parallel, and analyze any platform-specific differences.

## How

Execute this shell command:

    gsd-test-both

It runs `gsd-test-local` (Mac), `gsd-test` (Linux Docker), and `gsd-test-windows`
(Windows Docker, if configured) in parallel, captures JSON Lines output from each
to per-invocation files in `/tmp/`, then prints a comparison.

Windows is skipped with a notice if `~/.config/gsd-test/windows-hosts` is empty.

## Then analyze

Read the output files. Categorize failures:

- **All fail** = real bugs, fix first
- **Mac fail only** = macOS-specific issue
- **Linux fail only** = Linux/container-specific issue (different homedir,
  case-sensitive fs, missing tools, etc.)
- **Windows fail only** = Windows-specific issue (path separators, line endings,
  missing POSIX tools, etc.)

For each interesting failure, show file + test name + first line of the
error. Don't paste full stack traces.

## Useful one-liners

    jq -c 'select((.type=="test:fail") or (.type=="test_event" and .kind=="fail")) | {file: (.data.file // .file), name: (.data.name // .name)}' /tmp/gsd-test-docker-*.jsonl
    jq -r 'select((.type=="test:fail") or (.type=="test_event" and .kind=="fail")) | (.data.file // .file)' /tmp/gsd-test-docker-*.jsonl | sort | uniq -c | sort -rn
__CODEXPROMPT_END__

# ---------- claude-stop-hook.json ----------
echo "» writing claude-stop-hook.json..."
cat > "$DEST_DOCS/claude-stop-hook.json" <<'__HOOK_END__'
{
  "_comment": "Merge the hooks block below into ~/.claude/settings.json. Runs gsd-test-both after Claude finishes a turn IF cwd is the get-shit-done project. Output is capped at 200KB.",
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "test -f package.json && grep -q '\"name\": \"get-shit-done-cc\"' package.json && gsd-test-both 2>&1 | tail -c 200000 || true"
          }
        ]
      }
    ]
  }
}
__HOOK_END__

# ---------- README ----------
echo "» writing README..."
cat > "$DEST_DOCS/README.md" <<'__README_END__'
# gsd-test-runner

Tri-platform test runner for the [get-shit-done](https://github.com/gsd-build/get-shit-done) project.
Runs Node tests on the local Mac, inside a Linux Docker container, and inside a Windows Docker
container, captures structured JSON Lines output from each, and surfaces platform-specific diffs.

The Linux side catches bugs your Mac would miss — different homedir, case-sensitive filesystem,
missing system tools. The Windows side catches path separator bugs, line-ending issues, and
missing POSIX tools. The Mac side catches bugs that depend on the developer's environment.
Running all three gives you the union of all three safety nets.

## Installed components

| Path | What |
|---|---|
| `~/.local/bin/gsd-test` | Linux Docker remote runner. Rsyncs working tree to a Linux host, runs tests in a one-shot container, returns JSON. |
| `~/.local/bin/gsd-test-windows` | Windows Docker remote runner. Rsyncs working tree to a Windows host, runs tests in a one-shot Windows container, returns JSON. |
| `~/.local/bin/gsd-test-local` | Same tests, run directly on the Mac. |
| `~/.local/bin/gsd-test-both` | Runs all platforms in parallel and prints a diff. |
| `~/.local/bin/gsd-test-diff` | Python helper that compares two or three JSON Lines outputs. |
| `~/.local/share/gsd-test/reporter.mjs` | Custom Node test reporter producing JSON Lines (shared by all runners). |
| `~/.config/gsd-test/hosts` | Your Linux SSH host aliases, one per line. Never pushed. |
| `~/.config/gsd-test/windows-hosts` | Your Windows SSH host alias (one line). Never pushed. Leave empty to skip Windows. |

## Configuration

### Linux hosts

Edit `~/.config/gsd-test/hosts`. One SSH alias per line. Examples:

    dockerhost1
    dockerhost2

These must be reachable via key-based SSH (no password prompt) and have Docker installed.

### Windows host

Edit `~/.config/gsd-test/windows-hosts`. One SSH alias per line (typically just one host).

Requirements for the Windows host:
- Windows Server 2022 (or Windows 10/11 with Windows containers mode)
- Docker for Windows switched to **Windows containers mode**
- OpenSSH server installed and running
- `rsync` available in the SSH server's PATH — easiest via WSL2 (install rsync in WSL2
  and ensure the WSL2 binary is in the default PATH for SSH sessions), or via MSYS2/Cygwin

Leave `~/.config/gsd-test/windows-hosts` empty to skip Windows testing. `gsd-test-both`
will print "Windows runner skipped (GSD_WINDOWS_HOST unset)" and run 2-platform mode.

### SSH config

In `~/.ssh/config`:

    Host dockerhost1 dockerhost2
      HostName %h.example.com
      User youruser
      ControlMaster auto
      ControlPath ~/.ssh/cm-%r@%h:%p
      ControlPersist 10m

    Host winhost1
      HostName winhost1.example.com
      User Administrator
      ControlMaster auto
      ControlPath ~/.ssh/cm-%r@%h:%p
      ControlPersist 10m

### Build the Docker images (one-time per host)

**Linux host:**

    scp ~/projects/dev-tools/get-shit-done/Dockerfile dockerhost1:~/gsd-test.Dockerfile
    ssh dockerhost1 'mkdir -p ~/gsd-test && mv ~/gsd-test.Dockerfile ~/gsd-test/Dockerfile && cd ~/gsd-test && docker build -t gsd-test:node22 .'

**Windows host** (build takes ~10–20 min; image is ~5 GB):

    scp ~/projects/dev-tools/get-shit-done/Dockerfile.windows winhost1:~/Dockerfile.windows
    ssh winhost1 'docker build -f Dockerfile.windows -t gsd-test:node22-win .'

## Daily use

From inside the get-shit-done project:

    gsd-test-both                       # run all platforms, print diff (usual case)
    gsd-test                            # linux docker only
    gsd-test-windows                    # windows docker only
    gsd-test-local                      # mac only
    gsd-test tests/foo.test.cjs         # single test file on linux docker
    gsd-test-both --no-build            # skip build:sdk for a faster iteration

Output (default): JSON Lines on stdout, progress on stderr.
With `gsd-test-both`: human diff summary on stdout, progress on stderr.

## Claude Code integration

### Claude Code

    cp ~/projects/dev-tools/get-shit-done/claude-commands/test.md ~/.claude/commands/test.md

Now `/test` in Claude Code runs `gsd-test-both` and analyzes the diff.

#### Stop hook (optional)

Merge `~/projects/dev-tools/get-shit-done/claude-stop-hook.json` into `~/.claude/settings.json`.
It runs `gsd-test-both` after every Claude turn, gated to fire only inside the GSD project.

### Codex CLI (best-effort)

    cp ~/projects/dev-tools/get-shit-done/codex-prompts/test.md ~/.codex/prompts/test.md

Then `/test` (or however your Codex version invokes user prompts) should
trigger the test+diff run. Codex's prompt-file convention has been less stable
than Claude Code's, so if your version doesn't pick this up, just call
`gsd-test-both` directly from inside the Codex shell — the scripts are
agent-agnostic and work from any CLI's bash tool regardless of where the
prompt file lives.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | All tests passed (all configured platforms) |
| 1 | Some tests failed |
| 2 | Configuration error (missing hosts file, missing project root) |
| 3 | No reachable Docker host |
__README_END__

# ---------- claude code slash command + hook prompts ----------
echo ""

# Slash command (Claude Code)
if [[ -f "$HOME/.claude/commands/test.md" ]]; then
  echo "» Claude Code /test command already installed (skipping)"
else
  read -r -p "Install /test slash command into ~/.claude/commands/? [Y/n] " yn
  yn="${yn:-Y}"
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    mkdir -p "$HOME/.claude/commands"
    cp "$DEST_DOCS/claude-commands/test.md" "$HOME/.claude/commands/test.md"
    echo "  installed: ~/.claude/commands/test.md"
  fi
fi

# Prompt file (Codex CLI) — best-effort, location may need adjustment
if [[ -f "$HOME/.codex/prompts/test.md" ]]; then
  echo "» Codex /test prompt already installed (skipping)"
else
  read -r -p "Install /test prompt into ~/.codex/prompts/? [Y/n] " yn
  yn="${yn:-Y}"
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    mkdir -p "$HOME/.codex/prompts"
    cp "$DEST_DOCS/codex-prompts/test.md" "$HOME/.codex/prompts/test.md"
    echo "  installed: ~/.codex/prompts/test.md"
    echo "  NOTE: verify Codex picks it up. If your Codex version uses a"
    echo "        different prompts location, copy the file there manually."
  fi
fi

# Stop hook
if grep -q '"gsd-test-both"' "$HOME/.claude/settings.json" 2>/dev/null; then
  echo "» Stop hook already configured (skipping)"
else
  read -r -p "Install Claude Code Stop hook (auto-runs tests after each turn)? [y/N] " yn
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    SETTINGS="$HOME/.claude/settings.json"
    mkdir -p "$HOME/.claude"
    if [[ -f "$SETTINGS" ]]; then
      echo "  manual merge required: open $SETTINGS and merge the 'hooks' block from $DEST_DOCS/claude-stop-hook.json"
    else
      cp "$DEST_DOCS/claude-stop-hook.json" "$SETTINGS"
      # Strip the _comment field if jq exists
      if command -v jq >/dev/null 2>&1; then
        jq 'del(._comment)' "$SETTINGS" > "$SETTINGS.tmp" && mv "$SETTINGS.tmp" "$SETTINGS"
      fi
      echo "  installed: $SETTINGS"
    fi
  fi
fi

# ---------- done ----------
echo ""
echo "✓ Install complete."
echo ""
echo "  scripts        → $DEST_BIN/{gsd-test,gsd-test-windows,gsd-test-local,gsd-test-both,gsd-test-diff}"
echo "  reporter       → $DEST_SHARE/reporter.mjs"
echo "  linux hosts    → $DEST_CONFIG/hosts"
echo "  windows host   → $DEST_CONFIG/windows-hosts"
echo "  reference docs → $DEST_DOCS/"
echo ""
echo "Try it:"
echo "  cd /path/to/get-shit-done"
echo "  gsd-test-both"
