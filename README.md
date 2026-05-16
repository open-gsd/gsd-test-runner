# gsd-test-runner

Dual-platform test runner for the [get-shit-done](https://github.com/gsd-build/get-shit-done) project.
Runs Node tests both on the local Mac AND inside a Docker container on a Linux host,
captures structured JSON Lines output from each, and surfaces platform-specific diffs.

The Linux side is what catches the bugs your Mac would miss — different homedir,
case-sensitive filesystem, missing system tools, etc. The Mac side is what catches
bugs that depend on the developer's actual environment. Running both gives you the
union of both safety nets.

The Mac-side runner is **resource-capped** so an AI agent (or a runaway test) can't
spawn unbounded Node workers and OOM the developer's session. See "Resource caps"
below.

## Installed components

| Path | What |
|---|---|
| `~/.local/bin/gsd-test` | Docker remote runner. Rsyncs working tree to a Linux host, runs tests in a one-shot container, returns JSON. |
| `~/.local/bin/gsd-test-local` | Same tests, run natively on the Mac, capped by ulimit + V8 heap caps. |
| `~/.local/bin/gsd-test-both` | Runs both in parallel and prints a diff. |
| `~/.local/bin/gsd-test-diff` | Python helper that compares two JSON Lines outputs. |
| `~/.local/share/gsd-test/reporter.mjs` | Custom Node test reporter producing JSON Lines (shared by both runners). |
| `~/.config/gsd-test/hosts` | Your SSH host aliases, one per line. Never pushed. |

## Resource caps (Mac runner)

`gsd-test-local` applies four caps so a runaway test cannot consume the whole Mac:

| Cap | Mechanism | Default | Env override |
|---|---|---|---|
| V8 heap per Node process | `NODE_OPTIONS=--max-old-space-size` | 2048 MB | `GSD_NODE_HEAP_MB` |
| Concurrent test workers | `node --test-concurrency` | 2 | `GSD_TEST_CONCURRENCY` |
| Virtual memory (best-effort) | `ulimit -v` | 4096 MB | `GSD_MEM_MB` |
| Max user processes | `ulimit -u` | 256 | `GSD_PROC_MAX` |

The V8 heap cap and `--test-concurrency` are the two reliable ones — V8 enforces
heap caps internally, and `--test-concurrency` directly bounds the worker pool.
`ulimit -v` (RLIMIT_AS) is best-effort on macOS — not all releases enforce it
strictly — so treat it as a backstop, not the primary control.

On startup the script also `setsid`'s into its own process group and traps INT/TERM
to `kill -- -$PGID`, so Ctrl-C tears down every worker. Without this, orphaned Node
workers can outlive the wrapper and keep eating RAM — the failure mode this whole
thing exists to prevent.

    # tighten the Mac caps for a specific run:
    GSD_NODE_HEAP_MB=1024 GSD_TEST_CONCURRENCY=1 gsd-test-local

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
    gsd-test-local                      # mac only (resource-capped)
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
