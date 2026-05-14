#!/usr/bin/env bash
# INSTALL_GSD_TEST.sh — install GSD test-running tools on your Mac.
#
# Installs four runnable scripts, a shared JSON reporter, and a local config:
#   ~/.local/bin/gsd-test          docker remote runner (rsync → Linux host → container)
#   ~/.local/bin/gsd-test-local    local Mac runner (node --test directly)
#   ~/.local/bin/gsd-test-both     runs both in parallel + prints a diff
#   ~/.local/bin/gsd-test-diff     python helper that diffs two JSON Lines outputs
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

# ---------- shared JSON reporter ----------
echo "» writing reporter.mjs..."
cat > "$DEST_SHARE/reporter.mjs" <<'__REPORTER_END__'
// Custom Node test reporter: emits one JSON line per test runner event.
// Handles Error objects so message/stack survive JSON.stringify.
export default async function* (source) {
  for await (const e of source) {
    yield JSON.stringify({ type: e.type, data: e.data }, (k, v) =>
      v instanceof Error ? { name: v.name, message: v.message, stack: v.stack, code: v.code, ...v } : v
    ) + '\n';
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

if $RESET; then
  log "resetting mirror + npm cache on $HOST..."
  ssh "$HOST" "rm -rf ~/$MIRROR_DIR; docker volume rm $NPM_CACHE_VOLUME 2>/dev/null || true; mkdir -p ~/$MIRROR_DIR"
else
  ssh "$HOST" "mkdir -p ~/$MIRROR_DIR"
fi

log "rsync → $HOST:~/$MIRROR_DIR"
rsync -az --delete \
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
npm run build:sdk $NPM_FLAGS 1>&2
echo '» lint:skill-deps...' 1>&2
npm run lint:skill-deps $NPM_FLAGS 1>&2"
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
exec node --test --test-reporter=/tmp/reporter.mjs --test-concurrency=4 $TEST_TARGETS
REMOTE_END
)

SCRIPT_B64="$(printf '%s' "$REMOTE_SCRIPT" | base64 | tr -d '\n')"
CONTAINER_BOOT="echo $SCRIPT_B64 | base64 -d > /tmp/run.sh && exec bash /tmp/run.sh"

log "running tests in container..."
ssh "$HOST" "docker run --rm -i -v ~/$MIRROR_DIR:/work -v $NPM_CACHE_VOLUME:/root/.npm $IMAGE bash -c '$CONTAINER_BOOT'"
__GSDTEST_END__
chmod +x "$DEST_BIN/gsd-test"

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
  npm run build:sdk $NPM_FLAGS >&2
  log "lint:skill-deps..."
  npm run lint:skill-deps $NPM_FLAGS >&2
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
# gsd-test-both — run the test suite locally AND in Docker in parallel,
# save both JSON Lines outputs, and print a comparison diff at the end.
#
# Usage:
#   gsd-test-both                       run both, summarize diff
#   gsd-test-both --no-build            skip build:sdk on both
#   gsd-test-both tests/foo.test.cjs    specific file(s) on both
#
# Output files (override with env vars LOCAL_OUT, DOCKER_OUT):
#   /tmp/gsd-test-local.jsonl
#   /tmp/gsd-test-docker.jsonl

set -uo pipefail

LOCAL_OUT="${LOCAL_OUT:-/tmp/gsd-test-local.jsonl}"
DOCKER_OUT="${DOCKER_OUT:-/tmp/gsd-test-docker.jsonl}"

echo "» local → $LOCAL_OUT" >&2
echo "» docker → $DOCKER_OUT" >&2

# Args that BOTH runners accept get passed through. Host-specific args (--host,
# --reset) only make sense for the docker side; pass them via env vars or use
# gsd-test directly if you need them.
gsd-test-local "$@" > "$LOCAL_OUT" 2>/tmp/gsd-test-local.err &
LPID=$!
gsd-test "$@" > "$DOCKER_OUT" 2>/tmp/gsd-test-docker.err &
DPID=$!

wait $LPID; LRC=$?
wait $DPID; DRC=$?
echo "» local exit=$LRC  docker exit=$DRC" >&2
echo "" >&2

# Show stderr tails if either failed badly
if [[ $LRC -ne 0 && $LRC -ne 1 ]]; then
  echo "--- local stderr (last 20 lines) ---" >&2
  tail -20 /tmp/gsd-test-local.err >&2 || true
fi
if [[ $DRC -ne 0 && $DRC -ne 1 ]]; then
  echo "--- docker stderr (last 20 lines) ---" >&2
  tail -20 /tmp/gsd-test-docker.err >&2 || true
fi

gsd-test-diff "$LOCAL_OUT" "$DOCKER_OUT"
__GSDBOTH_END__
chmod +x "$DEST_BIN/gsd-test-both"

# ---------- gsd-test-diff (Python diff helper) ----------
echo "» writing gsd-test-diff..."
cat > "$DEST_BIN/gsd-test-diff" <<'__GSDDIFF_END__'
#!/usr/bin/env python3
"""
gsd-test-diff — compare two JSON Lines test reports and summarize the diff.

Usage: gsd-test-diff <local.jsonl> <docker.jsonl>

Cross-platform path normalization: '/work/tests/foo.test.cjs' (Docker) and
'/abs/path/tests/foo.test.cjs' (Mac) both reduce to 'tests/foo.test.cjs'.
"""
import json
import sys
import os

def normalize_path(p):
    if not p:
        return p
    if p.startswith('/work/'):
        return p[len('/work/'):]
    idx = p.rfind('/tests/')
    if idx >= 0:
        return p[idx+1:]
    return p

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
            except json.JSONDecodeError:
                continue
            t = evt.get('type')
            if t not in ('test:pass', 'test:fail'):
                continue
            d = evt.get('data', {}) or {}
            key = (normalize_path(d.get('file', '')), d.get('name', ''))
            err = ''
            details = d.get('details') or {}
            error = details.get('error') or {}
            if isinstance(error, dict):
                err = error.get('message', '') or ''
            results[key] = (t, err)
    return results

if len(sys.argv) != 3:
    print(__doc__, file=sys.stderr)
    sys.exit(2)

local = load(sys.argv[1])
docker = load(sys.argv[2])

both_pass = 0
both_fail = []
mac_pass_docker_fail = []
mac_fail_docker_pass = []
only_mac = []
only_docker = []

for k in set(local.keys()) | set(docker.keys()):
    m = local.get(k)
    d = docker.get(k)
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

show("Both failed (priority — break on every platform)", both_fail)
show("Mac-only failures (passes on Docker, fails on Mac)", mac_fail_docker_pass)
show("Docker-only failures (passes on Mac, fails on Docker)", mac_pass_docker_fail)
show("Only in Mac run (missing from Docker)", only_mac)
show("Only in Docker run (missing from Mac)", only_docker)

# Exit non-zero if any failures exist
if both_fail or mac_fail_docker_pass or mac_pass_docker_fail or only_mac or only_docker:
    sys.exit(1)
__GSDDIFF_END__
chmod +x "$DEST_BIN/gsd-test-diff"

# ---------- Dockerfile (for remote hosts) ----------
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

# ---------- claude-commands/test.md ----------
echo "» writing claude-commands/test.md..."
cat > "$DEST_DOCS/claude-commands/test.md" <<'__SLASHCMD_END__'
---
description: Run the GSD test suite on Mac AND in Docker, then compare results.
allowed-tools: Bash(gsd-test-both:*), Bash(gsd-test:*), Bash(gsd-test-local:*), Bash(jq:*), Bash(cat:*), Bash(head:*), Bash(tail:*)
---

Run the project's tests on both platforms (Mac local + Docker Linux) and
analyze the diff.

## Steps

1. Run `gsd-test-both`. It launches `gsd-test-local` and `gsd-test` in parallel,
   writes JSON Lines results to `/tmp/gsd-test-local.jsonl` and
   `/tmp/gsd-test-docker.jsonl`, then prints a comparison summary.

2. Read both JSON Lines files. Parse and categorize failures into:
   - **Both fail** (real bugs — same failure on both platforms)
   - **Mac-only fail** (passes on Docker — Mac-specific issue)
   - **Docker-only fail** (passes on Mac — Linux/container-specific issue)
   - **Missing on one platform** (test discovery differs)

3. Summarize for me:
   - Counts per category
   - For each failure type, list up to 10 with file + test name + first
     line of the error message

4. If anything is Mac-only or Docker-only, those are platform-specific —
   call those out as the most interesting findings. If something only fails
   in Docker, the container environment (different homedir, different fs
   case-sensitivity, etc.) probably matters. If it only fails on Mac,
   probably a macOS quirk.

5. If `gsd-test-both` exited non-zero, surface the stderr tails it printed.

## Useful jq one-liners

    # All failures on a specific platform:
    jq -c 'select(.type=="test:fail") | {file: .data.file, name: .data.name}' /tmp/gsd-test-docker.jsonl

    # Test names grouped by file (most-broken files first):
    jq -r 'select(.type=="test:fail") | .data.file' /tmp/gsd-test-docker.jsonl | sort | uniq -c | sort -rn
__SLASHCMD_END__

# ---------- codex prompt (best-effort) ----------
echo "» writing codex-prompts/test.md..."
cat > "$DEST_DOCS/codex-prompts/test.md" <<'__CODEXPROMPT_END__'
# /test — run GSD tests on both platforms and diff

Run the project tests on both Mac (local) AND in Docker on a Linux host, in
parallel, and analyze any platform-specific differences.

## How

Execute this shell command:

    gsd-test-both

It runs `gsd-test-local` (Mac) and `gsd-test` (Docker) in parallel, captures
JSON Lines output from each to `/tmp/gsd-test-local.jsonl` and
`/tmp/gsd-test-docker.jsonl`, then prints a comparison.

## Then analyze

Read both files. Categorize failures:

- **Both fail** = real bugs, fix first
- **Mac fail only** = macOS-specific issue
- **Docker fail only** = Linux/container-specific issue (different homedir,
  case-sensitive fs, missing tools, etc.)

For each interesting failure, show file + test name + first line of the
error. Don't paste full stack traces.

## Useful one-liners

    jq -c 'select(.type=="test:fail") | {file: .data.file, name: .data.name}' /tmp/gsd-test-docker.jsonl
    jq -r 'select(.type=="test:fail") | .data.file' /tmp/gsd-test-docker.jsonl | sort | uniq -c | sort -rn
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

Dual-platform test runner for the [get-shit-done](https://github.com/gsd-build/get-shit-done) project.
Runs Node tests both on the local Mac AND inside a Docker container on a Linux host,
captures structured JSON Lines output from each, and surfaces platform-specific diffs.

The Linux side is what catches the bugs your Mac would miss — different homedir,
case-sensitive filesystem, missing system tools, etc. The Mac side is what catches
bugs that depend on the developer's actual environment. Running both gives you the
union of both safety nets.

## Installed components

| Path | What |
|---|---|
| `~/.local/bin/gsd-test` | Docker remote runner. Rsyncs working tree to a Linux host, runs tests in a one-shot container, returns JSON. |
| `~/.local/bin/gsd-test-local` | Same tests, run directly on the Mac. |
| `~/.local/bin/gsd-test-both` | Runs both in parallel and prints a diff. |
| `~/.local/bin/gsd-test-diff` | Python helper that compares two JSON Lines outputs. |
| `~/.local/share/gsd-test/reporter.mjs` | Custom Node test reporter producing JSON Lines (shared by both runners). |
| `~/.config/gsd-test/hosts` | Your SSH host aliases, one per line. Never pushed. |

## Configuration

### Hosts

Edit `~/.config/gsd-test/hosts`. One SSH alias per line. Examples:

    dockerhost1
    dockerhost2

These must be reachable via key-based SSH (no password prompt) and have Docker installed.

### SSH config

In `~/.ssh/config`:

    Host dockerhost1 dockerhost2
      HostName %h.example.com
      User youruser
      ControlMaster auto
      ControlPath ~/.ssh/cm-%r@%h:%p
      ControlPersist 10m

### Build the Docker image on each host (one-time)

    scp ~/projects/dev-tools/get-shit-done/Dockerfile dockerhost1:~/gsd-test.Dockerfile
    ssh dockerhost1 'mkdir -p ~/gsd-test && mv ~/gsd-test.Dockerfile ~/gsd-test/Dockerfile && cd ~/gsd-test && docker build -t gsd-test:node22 .'

## Daily use

From inside the get-shit-done project:

    gsd-test-both                       # run both, print diff (usual case)
    gsd-test                            # docker only
    gsd-test-local                      # mac only
    gsd-test tests/foo.test.cjs         # single test file on docker
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
| 0 | All tests passed (both platforms) |
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
echo "  scripts        → $DEST_BIN/{gsd-test,gsd-test-local,gsd-test-both,gsd-test-diff}"
echo "  reporter       → $DEST_SHARE/reporter.mjs"
echo "  hosts config   → $DEST_CONFIG/hosts"
echo "  reference docs → $DEST_DOCS/"
echo ""
echo "Try it:"
echo "  cd /path/to/get-shit-done"
echo "  gsd-test-both"
