# Release Notes

This page summarizes the recent releases so you can quickly decide what to adopt.

## At a glance

- `v1.3.0`: configurable test command in the `run_tests` leg
- `v1.3.1`: shell-aware parsing for string commands + explicit argv command arrays
- `v1.3.2`: per-bench container platform pinning (`linux/amd64`, `linux/arm64`, etc.)
- `v1.4.0`: run-and-die for coding agents — the `gsd-test run` handoff, a one-command installer, and non-blocking `--async`/`wait`/`status`
- `v1.5.0`: failure-first output — a quiet-by-default stream, a loud machine-readable verdict, and saved `FAILURES.md` / `failures.json` / `junit.xml` artifacts; plus ephemeral run storage you opt out of with `--keep` or `[storage]`

## Unreleased

_Nothing yet — changes land here before the next tagged release._

## v1.5.0

### Added

- **Failure-first run output.** Runs are now quiet by default: you see the pipeline legs, a compact heartbeat (one line per 25 passing tests, per OS), and failures — not every passing test. Each failure surfaces loudly the instant it happens as `✗ FAIL <file>:<line> · <class> · <name> — <msg>`, so you never scroll back to find it. The full firehose is still one flag away (`--verbose` / `GSD_TEST_VERBOSE=1`); `--quiet` trims output to the essentials (epic #84, ADR-0023).
- **A machine-readable verdict on every run.** The last line of stdout is always one compact JSON `verdict` object whose `outcome` (`passed` / `failed` / `reaped` / `infra_error`) matches the exit code and whose `artifacts.dir` points at the saved output. Script against the last stdout line instead of parsing the stream.
- **Saved failure-first artifacts.** Every run writes a `FAILURES.md`, `failures.json`, per-failure files, and a `junit.xml` under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/`. The JUnit XML (one `<testsuite>` per OS) drops straight into CI test-report viewers.
- **Lossless event emission.** Events flow through an unbounded queue and pump, so nothing is dropped under load — the digest and verdict stay complete even when a suite is noisy.
- **Ephemeral run storage with opt-out.** Run artifacts are released automatically once consumed, so the runs store under `$XDG_STATE_HOME/gsd-test/runs/` no longer grows without bound. `gsd-test wait` releases a run after printing its result, and blocking runs are pruned on a later invocation. A new `[storage]` config section (`keep_artifacts`, `artifact_ttl`, `keep_last_runs`) and a per-run `--keep` flag opt out (#102).

### Fixed

- Every infrastructure outcome now ends in a `verdict`, and run-ids are guarded against path traversal before they touch the runs directory (epic #84).
- The reaper no longer aborts a sweep when a container has already exited; it verifies actual state and reaps the remaining overdue containers (#104).
- The JSONL drain temp file is now removed after it is persisted into the run directory, fixing a per-run temp-file leak.

### Why it matters

A failing run used to bury its one important line under a wall of passing-test noise. Now the signal is loud and immediate — the failure prints in real time, the last line is a verdict you can parse, and the full detail is saved as `FAILURES.md` / `failures.json` / `junit.xml`. And the runs store no longer grows without bound: artifacts are released once consumed, with `--keep` or `[storage]` when you need them to persist.

### Example

```bash
# quiet by default — pipeline legs, a heartbeat, and failures only
gsd-test

# full firehose (every passing test, plus npm ci / build output)
gsd-test --verbose
```

The last stdout line is the `verdict`; its `artifacts.dir` holds `FAILURES.md`, `failures.json`, and `junit.xml`.

Keep an async run's artifacts instead of auto-releasing them, or set a retention policy:

```bash
gsd-test run --async --keep < spec.json
```

```toml
[storage]
artifact_ttl = "72h"
keep_last_runs = 25
```

### Learn more

Start with [Failure-first Output](failure-first-output.md), the [output how-to guides](failure-first-output-how-to.md), and the [output reference](failure-first-output-reference.md). For retention, see the `[storage]` section of the [Configuration Reference](configuration.md).

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
