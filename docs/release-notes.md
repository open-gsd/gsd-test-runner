# Release Notes

This page summarizes the recent releases so you can quickly decide what to adopt.

## At a glance

- `v1.3.0`: configurable test command in the `run_tests` leg
- `v1.3.1`: shell-aware parsing for string commands + explicit argv command arrays
- `v1.3.2`: per-bench container platform pinning (`linux/amd64`, `linux/arm64`, etc.)
- `v1.4.0`: run-and-die for coding agents — the `gsd-test run` handoff, a one-command installer, and non-blocking `--async`/`wait`/`status`
- Unreleased: ephemeral run storage — artifacts auto-released after `wait`; opt out with `--keep` or `[storage]`

## Unreleased

### Added

- **Ephemeral run storage with opt-out.** Run artifacts are released automatically once consumed, so the runs store under `$XDG_STATE_HOME/gsd-test/runs/` no longer grows without bound. A new `[storage]` config section (`keep_artifacts`, `artifact_ttl`, `keep_last_runs`) and a per-run `--keep` flag control retention.

  **Why it matters:** a long-lived Bench previously accumulated every run's artifacts forever, with no operator signal. Now `gsd-test wait` releases a run after printing its result, blocking runs are pruned on a later invocation, and you opt out only when you need the files to persist.

  **Example:**
  ```bash
  gsd-test run --async --keep < spec.json   # keep this run's artifacts
  ```
  ```toml
  [storage]
  artifact_ttl = "72h"
  keep_last_runs = 25
  ```

### Fixed

- The reaper no longer aborts a sweep when a container has already exited; it verifies actual state and reaps the remaining overdue containers (#104).
- The JSONL drain temp file is now removed after it is persisted into the run directory, fixing a per-run temp-file leak.

## v1.4.0

### Added

- `gsd-test run` — the executor coding agents call instead of `node --test`: it runs the suite in a disposable container and returns a `node --test`-style verdict and exit code, so the agent treats it like a normal test run (issue #67, ADR-0022).
- `gsd-test run --async`, with `gsd-test wait <run-id>` and `gsd-test status <run-id>` — non-blocking dispatch. `--async` returns a run-id immediately so the agent can keep working; `wait` blocks for the complete verdict, `status` reports progress without blocking. Blocking `gsd-test run` stays the default (issue #70, ADR-0022 Decision 3).
- `gsd-test install-agent-hooks` — a one-command, idempotent, reversible installer that wires the Claude Code `PreToolUse` hook plus skill and the Codex shim. Flags: `--claude`, `--codex`, `--global`, `--uninstall` (issue #71, ADR-0022 Decision 5).
- `gsd-test submit` — the run-spec front door with an estimate-aware in-container watchdog and a two-tier external reaper that kill runaway suites and name the test that ran away (`outcome: "reaped"`, result `schema_version: 2`) (issue #60, ADR-0021).
- Claude Code and Codex integration that intercepts `node --test` / `npm test` and routes it to `gsd-test run` (issues #68, #69, #78).
- Per-repo telemetry with a runaway leaderboard.

### Why it matters

A coding agent can no longer wedge the workstation with an orphaned `node --test`: execution moves into a container that dies when the run ends, the result is a recognisable verdict — a reaped run is a loud, attributed failure rather than a silent hang — and wiring it onto a workstation is a single `gsd-test install-agent-hooks`.

### Example

```bash
# one-time setup on the dev workstation
gsd-test install-agent-hooks

# the agent runs this instead of `node --test`
gsd-test run

# or dispatch without blocking and collect the verdict later
gsd-test run --async
gsd-test wait <run-id>
```

### Learn more

Start with [Run-and-die Execution](run-and-die.md), the [how-to guides](run-and-die-how-to.md), and the [reference](run-and-die-reference.md).

## v1.3.2

### Added

- `[[benches]].platform` (optional) in `config.toml`
- `docker run --platform <value>` passthrough in the pipeline when `platform` is set

### Why it matters

On mixed hardware benches (for example, Apple Silicon local Docker + x86 Linux host), default container platform selection can vary by host. `platform` gives you deterministic architecture selection across benches.

### Example

```toml
[[benches]]
name = "linux-host"
host = "lab-rig-1"
os = "linux"
platform = "linux/amd64"

[[benches]]
name = "mac-local"
host = "local"
os = "macos"
platform = "linux/amd64"
```

Use the same platform value across benches when you want architecture parity.

## v1.3.1

### Added

- Shell-quote-aware parsing for string-form `testing.command`
- Explicit argv form for `testing.command` via string arrays

### Why it matters

Multi-step commands like `bash -c 'cmd1 && cmd2'` now parse correctly. You can also avoid quoting complexity entirely by using argv arrays.

### Examples

String form:

```toml
[testing]
command = "bash -c 'npm run pretest && node --test tests/*.test.cjs'"
```

Array form:

```toml
[testing]
command = ["bash", "-c", "npm run pretest && node --test tests/*.test.cjs"]
```

## v1.3.0

### Added

- Configurable `run_tests` command through `[testing].command`
- Reporter placeholder substitution:
  - `{{REPORTER_PATH}}`
  - `{{REPORTER_DEST}}`

### Why it matters

Projects are no longer locked to the built-in `node --test ...` command path. You can run your own test orchestration while still integrating with the gsd-test reporter output contract.

### Example

```toml
[testing]
command = [
  "npm",
  "test",
  "--",
  "--test-reporter={{REPORTER_PATH}}",
  "--test-reporter-destination={{REPORTER_DEST}}"
]
```

## Upgrade guide (v1.2.x -> v1.3.x)

1. Upgrade to the latest release binary.
2. Keep your existing config as-is if you do not need custom test orchestration.
3. Add `[testing].command` if your test entrypoint is not the default.
4. Add `[[benches]].platform` if benches run on mixed CPU architectures and you need deterministic parity.

## See also

- [Configuration Reference](configuration.md)
- [Troubleshooting](troubleshooting.md)
