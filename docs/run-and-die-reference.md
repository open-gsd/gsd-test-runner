# Run-and-die Reference

Field-by-field reference for the run-and-die interfaces. For tasks see the [how-to guides](run-and-die-how-to.md); for concepts see [Run-and-die Execution](run-and-die.md).

## `gsd-test run`

The explicit executor agents call instead of `node --test`. Builds a run spec from the current repository and any positional test path patterns, dispatches the run to a Bench via the front door, and renders a `node --test`-style verdict to stdout. Never spawns a local `node` process.

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `linux` | Target OS: `linux` \| `windows` \| `macos-container`. |
| `--config <path>` | (standard config search) | `config.toml` path, used to resolve Benches and image versions. |
| `--estimate-ms <int>` | `0` | Expected suite duration in milliseconds. Tightens the watchdog deadline; `0` falls back to telemetry median then `hardCapMs`. |
| `--async` | off | Submit and return immediately (POSIX only). Prints a dispatched-notice to stdout and exits `0`; the run continues in a detached worker. See [`gsd-test wait` and `gsd-test status`](#gsd-test-run---async-wait-and-status). |
| `--keep` | off | (requires `--async`) Preserve run artifacts after `gsd-test wait` collects the result. Opts this invocation out of ephemeral auto-release and skips the startup prune. Equivalent to `keep_artifacts = true` in `[storage]` but scoped to one run. |

Positional arguments after flags are treated as test path patterns appended to the run spec's `testPathPatterns`.

On every invocation (blocking or async) a structured banner is printed to stderr immediately after the run-id is assigned — see [Handoff banner and run-id](#handoff-banner-and-run-id).

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | All tests passed. |
| `1` | One or more tests failed, or the run was reaped. |
| `2` | The run could not be started (infra/inconclusive error). With `--async`, also returned on non-POSIX hosts. |

## `gsd-test run --async`, `wait`, and `status`

### `gsd-test run --async`

Submits the run, prints one line to stdout, and returns immediately with exit `0`. The run continues in a detached worker process.

Dispatched-notice format:

```text
dispatched run-id=<id>  (use `gsd-test wait <id>` to collect the result, `gsd-test status <id>` to check progress)
```

Unix-only (POSIX process groups). On non-POSIX hosts (Windows) it exits `2` with an error. Blocking `gsd-test run` remains the default so correctness never depends on the agent remembering to call `wait`.

### `gsd-test wait <run-id>`

Blocks until the async run completes, then renders the same `node --test`-style verdict and exits with the same codes (`0` / `1` / `2`) a blocking `run` would produce. Never renders a partial result. There is a 90-minute absolute backstop; if the worker died without writing a result the command exits `2` (fail-loud, never a silent hang).

Takes no flags — only the positional run-id.

**Ephemeral mode (default):** After rendering the result to stdout, `wait` emits the verdict line with empty artifact paths (stdout is the authoritative record, not the on-disk files), then releases the run — deleting the state file and artifact directory. Pass `--keep` to `gsd-test run --async`, or set `keep_artifacts = true` in `[storage]`, to skip the release and keep artifacts on disk. Retention for kept artifacts is governed by the `[storage]` config section — see the [configuration reference](configuration.md#storage).

`gsd-test status` never releases, regardless of ephemeral mode — it is a pure reporter. Blocking `gsd-test run` (non-async) is reclaimed by the prune pass on the next invocation.

### Exit codes — `wait`

| Code | Meaning |
|------|---------|
| `0` | All tests passed. |
| `1` | One or more tests failed, or the run was reaped. |
| `2` | Unknown run-id, worker died without a result, or the 90-minute backstop fired. |

### `gsd-test status <run-id>`

Reports the run state without blocking. Prints one greppable line to stdout:

```text
state=running run-id=<id> pid=<pid>
```

or, when complete:

```text
state=done run-id=<id> exit=<code> outcome=<outcome>
```

A pure reporter — exits `0` when the run-id is found regardless of whether the run itself succeeded. Unknown run-id exits `2`.

Takes no flags — only the positional run-id.

Run state is persisted to `$XDG_STATE_HOME/gsd-test/runs/<run-id>.json` (falls back to `~/.local/state/gsd-test/runs/`).

### Exit codes — `status`

| Code | Meaning |
|------|---------|
| `0` | Run-id found (run may still be in progress or may have failed). |
| `2` | Unknown run-id. |

## Handoff banner and run-id

Every `gsd-test run` invocation (blocking or async) emits one structured banner to **stderr** immediately after the run-id is assigned:

```text
↪ gsd-test: handed off to Docker (run-id=<id>, target=<os>) — do not re-run locally
```

The banner names the action explicitly so the agent does not re-run or treat the wait as a hang. Blocking mode means the command does not return until the verdict exists, making a retry unnecessary. The run-id is stable across the banner, the async dispatched-notice, `wait`, `status`, the container label `sh.gsd-test.run-id`, and the telemetry record's `run_id` field.

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

## `gsd-test install-agent-hooks`

One-command, idempotent, reversible installer for the agent integration. Default (no runtime flag) installs both Claude Code and Codex into the current project.

| Flag | Default | Description |
|------|---------|-------------|
| `--claude` | off | Install only the Claude Code `PreToolUse` hook and `run-and-die` skill. |
| `--codex` | off | Install only the Codex shim and `codex-bin/` `node`/`npm` PATH shims. |
| `--global` | off | Install into `$HOME` instead of the current project. |
| `--uninstall` | off | Reverse a previous install using the stored manifest. |

When neither `--claude` nor `--codex` is given, both runtimes are installed.

On install the command prints each file it added (`+`) and any settings file it modified (`~`), then for Codex prints the PATH line to add to `~/.codex/config.toml`:

```toml
[shell_environment_policy.set]
PATH = "<abs>/.gsd-test/codex-bin:${PATH}"
```

The `codex-bin/` directory must come **first** so Codex's `node`/`npm` invocations route through the shim. The shim rewrites `node --test` / `npm test` to `gsd-test run` and passes every other command through to the real binary (located by skipping its own directory, canonicalised so symlinks cannot cause recursion). This shadows `node`/`npm` only inside Codex; the human's interactive shell is untouched.

Reverse the install with `gsd-test install-agent-hooks --uninstall`. The manifest tracks exactly what was added, so uninstall is precise and does not remove changes made outside the installer. (ADR-0022 Decision 5)

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | Install or uninstall completed successfully. |
| `2` | Could not determine the home directory (`--global`), find the repo root, or apply the install/uninstall. |

## Run spec

A single JSON object. Unknown fields are ignored; invalid values are rejected with an error naming the offending field.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runId` | string | assigned | Engine-assigned UUID when omitted. |
| `repo` | string | — (required) | Absolute path to the source repo / run payload. Run as-is unless `base`+`prBranch` are given. |
| `base` | string | — | Base git ref. With `prBranch`, the Engine builds a PR-merged worktree from `repo` and runs that. Must be set together with `prBranch`. |
| `prBranch` | string | — | PR git ref merged onto `base`. Must be set together with `base`. |
| `target` | string | — (required) | `linux`, `windows`, or `macos-container`. Selects the Bench and Tester Image. |
| `testCommand` | string[] | `["node","--test"]` | The test command (argv form). The `node --test` path is hardened (see [hardening](#test-command-hardening)); other commands run unchanged under the watchdog. |
| `testPathPatterns` | string[] | — | Test file globs appended to the command. |
| `env` | object | — | Environment variables passed into the container as `-e KEY=VALUE` (sorted by key). |
| `budget.estimateMs` | integer | — | Expected test-run duration. Must be `> 0` when set. Drives the deadline (see [deadline](#effective-deadline)). |
| `budget.overrunFactor` | number | `1.5` | Multiplier on the estimate. Must be `>= 1.0`. |
| `budget.hardCapMs` | integer | `3600000` | Absolute ceiling (one hour). |
| `isolation` | string | `process` | `process` (one child per test file) or `none` (one shared runner process). |
| `concurrency` | integer \| null | `null` | Pins `--test-concurrency` when set; `null` pins to the CPU cap. |
| `telemetry.sampleHandlesMs` | integer | `0` | *Periodic* open-handle sampling interval. `0` (the default) disables it; a positive value samples open handles every N ms during the run. Validated (`>= 0`). Best-effort and file-level; a no-op under `isolation: none`. Exit-time leak detection runs regardless — see [Per-test leak detection](#per-test-leak-detection) and [Periodic handle sampling](#periodic-handle-sampling). |
| `telemetry.captureStacks` | boolean | `false` | When sampling is on, also capture the creation stack of each live async resource, grouped by type, in every sample. Inert without a positive `sampleHandlesMs`. |

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
| `handle_samples` | array | Present only when `telemetry.sampleHandlesMs > 0`. One entry per test file (`file`, `samples`); each sample is `{ at_ms, open, leaked[], stacks? }`. See [Periodic handle sampling](#periodic-handle-sampling). |
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

## Result artifacts and verdict (ADR-0023)

Beyond the `node --test`-style text, every `gsd-test run` / `wait` emits a
failure-first contract shared with the standard `gsd-test` path. The full
contract — verbosity levels, the verdict schema, truncation, and cross-OS
grouping — is documented once in the
[Failure-first Output Reference](failure-first-output-reference.md); the
run-and-die specifics are below:

- **Verdict line.** The final line of stdout is one compact JSON object, in every
  outcome:

  ```json
  {"type":"verdict","outcome":"reaped","per_os":{"linux":{"passed":0,"failed":3,"total":3,"outcome":"reaped"}},
   "unique_failures":2,"total_failures":3,"top":[{"class":"timeout","file":"reaper.test.js","line":88,"name":"reaps orphaned node"}],
   "artifacts":{"dir":"…","failures_json":"…","failures_md":"…","junit_xml":"…"}}
  ```

  `type:"verdict"` distinguishes it from any other line. `outcome` is the source
  of truth (it matches the exit code); artifact writes are best-effort and never
  change the exit code.

- **Artifact directory.** Under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/`
  (fallback `~/.local/state/gsd-test/runs/<run-id>/`), a sibling of the run's
  `<run-id>.json` state file:
  - `failures.json` — machine-readable summary + grouped failures with **full,
    untruncated** error/stack/output (the "full at …" target).
  - `FAILURES.md` — headline first, one **bounded** block per unique failure
    (`class · file:line · name · platforms`, capped error/stack/output with a
    pointer back into `failures.json`).
  - `failures/INDEX.md` + `failures/NN-<slug>.md` — one self-contained file per
    failure.
  - `junit.xml` — JUnit XML for CI/agent tooling.

  Fastest path for an agent: read the verdict line (or grep `"type":"verdict"`),
  then open `FAILURES.md` / `failures.json` — one read instead of scrolling the
  stream.

## Container labels

Each run container carries these labels (reverse-DNS, matching the image-version sentinel convention):

| Label | Value |
|-------|-------|
| `sh.gsd-test.run-id` | The run's `runId`. |
| `sh.gsd-test.deadline` | Absolute deadline as epoch milliseconds. The Tier-2 reaper sweeps on this. |
| `sh.gsd-test.target` | The target OS. |
| `sh.gsd-test.branch` | Branch slug under test (ADR-0029). The Tier-2 reaper scopes ownership to containers whose slug matches the current invocation; an empty/unset value (pre-ADR-0029 containers) is included only by an unscoped sweep. |
| `sh.gsd-test.image-version` | (On the image, not the container) the version sentinel verified before each run. |

The container is also launched with `--name gsd-test-<branch-slug>-<short-runId>` (ADR-0029) so a Bench operator can read the branch under test directly off `docker ps`. The slug is derived from the run spec's `prBranch` (preferred) or `base`; the 8-character `runId` tail disambiguates concurrent runs of the same branch.

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

The median `duration_ms` over `passed` runs of a target is the deadline fallback when a spec gives no estimate. The runaway leaderboard ranks tests by two signals: `killed` status (reaper trips) and `exited_clean: false` on a completed test (leaks — see below).

## Per-test leak detection

A leak probe (`/opt/gsd-test/leak-probe.mjs`) is preloaded into each `node --test` child via `NODE_OPTIONS=--import` (set by the entry script, pointed at `$GSD_LEAK_DIR`). It records the open OS resources at child start and, at process exit, writes anything still open to `$GSD_LEAK_DIR`. The watchdog folds those reports into `per_test`, marking a completed test `exited_clean: false`.

Because `--test-force-exit` exits the process even with an open handle, a test that *passes but leaks* (a dangling timer, socket, child process) is still flagged — a signal independent of the deadline. Caveats:

- **File-level.** Under process isolation the probe runs once per test *file*; it attributes a leak to the file, not to a specific test within a multi-test file.
- **No-op under `isolation: "none"`.** There is no per-file child and no test file in the process argv, so the probe does not run.
- **Best-effort.** A missing report simply means no signal, never a run failure.

## Periodic handle sampling

Set `telemetry.sampleHandlesMs` to a positive interval to make the same probe sample open handles *while the test runs*, not only at exit. Every `sampleHandlesMs` it appends a snapshot — `{ at_ms, open, leaked[], stacks? }` — to a per-file sidecar in `$GSD_LEAK_DIR`, flushed synchronously so a `SIGKILL` cannot lose it. The watchdog reads the sidecars before the container is torn down and surfaces them as `handle_samples` on the result envelope.

Unlike the exit-time signal, this survives a **reaped** run: a test that hangs and is killed never reaches `process.exit`, but its periodic samples still show how its open handles accumulated up to the kill. `leaked` uses the same load-time-baseline semantics as exit-time detection.

Set `telemetry.captureStacks: true` to also record the creation stack of each live async resource, grouped by async resource type, in every sample (`stacks`). This is heavier (an `async_hooks` hook records every resource's init stack) and is inert unless sampling is enabled.

Same caveats as leak detection apply: file-level, a no-op under `isolation: "none"`, and best-effort (per-type stack capacity is bounded; promise `destroy` is GC-timed, so stacks are a creation-site snapshot, not a precise live set).
