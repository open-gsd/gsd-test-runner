# Run-and-die Reference

Field-by-field reference for the run-and-die interfaces. For tasks see the [how-to guides](run-and-die-how-to.md); for concepts see [Run-and-die Execution](run-and-die.md).

## `gsd-test submit`

Reads a JSON [run spec](#run-spec), validates it, and assigns a `runId` if absent. Without `--execute` it prints the normalised spec; with `--execute` it dispatches the run and prints the [result envelope](#result-envelope).

| Flag | Default | Description |
|------|---------|-------------|
| `--spec-file <path>` | `-` | Path to the run spec, or `-` for stdin. |
| `--execute` | off | Dispatch the run to a Bench. When absent, validate and normalise only. |
| `--config <path>` | (standard config search) | `config.toml` path, used with `--execute` to resolve Benches and image versions. |

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | Accepted (validate-only) or the run passed. |
| `1` | The run failed or was reaped. |
| `2` | The spec could not be read or validated, or the run could not be started (inconclusive). |

## Run spec

A single JSON object. Unknown fields are ignored; invalid values are rejected with an error naming the offending field.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runId` | string | assigned | Engine-assigned UUID when omitted. |
| `repo` | string | — (required) | Absolute path to the run payload (the worktree copied into the container). |
| `target` | string | — (required) | `linux`, `windows`, or `macos-container`. Selects the Bench and Tester Image. |
| `testCommand` | string[] | `["node","--test"]` | The test command (argv form). The `node --test` path is hardened (see [hardening](#test-command-hardening)); other commands run unchanged under the watchdog. |
| `testPathPatterns` | string[] | — | Test file globs appended to the command. |
| `env` | object | — | Environment variables passed into the container as `-e KEY=VALUE` (sorted by key). |
| `budget.estimateMs` | integer | — | Expected test-run duration. Must be `> 0` when set. Drives the deadline (see [deadline](#effective-deadline)). |
| `budget.overrunFactor` | number | `1.5` | Multiplier on the estimate. Must be `>= 1.0`. |
| `budget.hardCapMs` | integer | `3600000` | Absolute ceiling (one hour). |
| `isolation` | string | `process` | `process` (one child per test file) or `none` (one shared runner process). |
| `concurrency` | integer \| null | `null` | Pins `--test-concurrency` when set; `null` uses the runner default. |

### Effective deadline

The watchdog kills at:

```
min(base × overrunFactor, hardCapMs)
```

floored at `30000` ms and timed from the **start of the test run**, not container start. `base` is `estimateMs` when set; otherwise the median duration of recent passing runs from [telemetry](#telemetry-record); otherwise `hardCapMs`.

### Test-command hardening

When `testCommand` is the `node --test` path, these flags are appended (ADR-0021 §E):

| Flag | Value |
|------|-------|
| `--test-force-exit` | always |
| `--test-timeout` | the effective deadline (ms) |
| `--experimental-test-isolation` | the `isolation` value |
| `--test-concurrency` | `concurrency` when set, otherwise the CPU cap (`2`) — always pinned to bound orphan fan-out |
| `--test-reporter` / `--test-reporter-destination` | `/opt/gsd-test/reporter.mjs` to `stdout`, so the watchdog receives structured events for `last_active_test` and per-test telemetry |

### Dependency install and build

Before the watchdog arms, the in-image entry script runs `npm ci` and
`npm run build --if-present` **when a `package.json` is present**. A missing
`package.json` or build script is skipped; a failing `npm ci` aborts the run
before any test executes. This keeps `npm ci`/build time out of the effective
deadline, which times only the test phase.

## Result envelope

The output of `gsd-test submit --execute`. Schema version 2 — a superset of the per-OS report, adding `outcome` and `kill`.

| Field | Type | Description |
|-------|------|-------------|
| `schema_version` | integer | Always `2`. |
| `kind` | string | `pass` or `fail` (retained for compatibility). |
| `outcome` | string | `passed`, `failed`, `reaped`, or `infra_error`. |
| `os` | string | Target OS. |
| `bench` | string | Bench name. |
| `image_id` | string | Tester Image reference. |
| `image_version` | string | Verified image-version sentinel value. |
| `started_at` | string | RFC 3339 timestamp. |
| `duration_ms` | number | Wall-clock duration of the run. |
| `total` / `passed` / `failed` | integer | Test counts. |
| `failures` | array | One entry per failed test (`file`, `name`, `duration_ms`, `retry_count`, `error`, `error_class`, `stack`, `output`). |
| `per_test` | array | Per-test telemetry (`file`, `name`, `duration_ms`, `status`, `exited_clean`) derived from the reporter events the watchdog observed. `status` is `passed`, `failed`, or `killed`; `exited_clean` is `false` for a test still in flight at a reap. |
| `kill` | object | Present only when `outcome` is `reaped`. See below. |

### `kill` object

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | `estimate_overrun`, `hard_cap`, or `external_reaper`. |
| `effective_deadline_ms` | integer | The deadline that fired. |
| `elapsed_ms` | integer | Time from test-run start to kill. |
| `last_active_test` | object | `{ file, name }` of the test running at kill time. |
| `in_flight_tests` | array | `{ file, name, started_ms_ago }` per test still executing. |
| `reaped_by` | string | `in_container` (Tier 1 watchdog) or `external` (Tier 2 reaper). |
| `signal_chain` | string[] | E.g. `["SIGTERM@30000","SIGKILL@30200"]`. |
| `granularity` | string | `"process"` when the run used `isolation: "none"` (attribution is best-effort); absent otherwise. |

`last_active_test` and `in_flight_tests` depend on the reporter having emitted a
`test:start` event before the kill. A test that blocks the runner's event loop
synchronously (a tight CPU loop) also blocks the reporter, so these fields may be
empty even though the container was reaped — attribution is best-effort. The
container-teardown guarantee is unaffected.

## Container labels

Each run container carries these labels (reverse-DNS, matching the image-version sentinel convention):

| Label | Value |
|-------|-------|
| `sh.gsd-test.run-id` | The run's `runId`. |
| `sh.gsd-test.deadline` | Absolute deadline as epoch milliseconds. The Tier-2 reaper sweeps on this. |
| `sh.gsd-test.target` | The target OS. |
| `sh.gsd-test.image-version` | (On the image, not the container) the version sentinel verified before each run. |

## Resource caps

Run containers are created with:

| Flag | Value |
|------|-------|
| `--rm` | auto-remove on exit |
| `--pids-limit` | `512` |
| `--memory` | `2g` |
| `--cpus` | `2` |

## Watchdog CLI

The watchdog is baked into each Tester Image at `/opt/gsd-test/watchdog.mjs` and invoked as the container's process:

```
node /opt/gsd-test/watchdog.mjs --deadline-ms <N> [--grace-ms <N>] \
  [--reason <R>] [--granularity <G>] -- <command> [args...]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--deadline-ms` | — | Effective deadline in milliseconds. |
| `--grace-ms` | `5000` | Wait between `SIGTERM` and `SIGKILL`. |
| `--reason` | `estimate_overrun` | `reason` recorded in the kill record. |
| `--granularity` | — | Set to `process` under `isolation: "none"`. |

Everything after `--` is the command run under the watchdog. The watchdog prints the result envelope fragment as JSON and exits `0` (passed), `1` (failed), or `75` (reaped). On a non-POSIX host (Windows) it escalates with `taskkill /T` rather than process-group signals.

## Telemetry record

Each run appends one JSON line to `$XDG_STATE_HOME/gsd-test/<repo>/telemetry.jsonl` (falling back to `~/.local/state/...`).

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | The run's `runId`. |
| `target` | string | Target OS. |
| `outcome` | string | `passed`, `failed`, `reaped`, or `infra_error`. |
| `duration_ms` | integer | Wall-clock duration. |
| `reaped` | boolean | Whether the run was killed. |
| `reap_reason` | string | Kill reason when reaped (omitted otherwise). |
| `per_test` | array | `{ file, name, duration_ms, status, peak_rss_bytes, exited_clean }` per test. |

The median `duration_ms` over `passed` runs of a target is the deadline fallback when a spec gives no estimate. Tests whose `per_test` status is `killed` across runs form the runaway leaderboard.
