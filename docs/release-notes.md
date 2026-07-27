# Release Notes

This page summarizes the recent releases so you can quickly decide what to adopt.

## At a glance

- `v1.3.0`: configurable test command in the `run_tests` leg
- `v1.3.1`: shell-aware parsing for string commands + explicit argv command arrays
- `v1.3.2`: per-bench container platform pinning (`linux/amd64`, `linux/arm64`, etc.)
- `v1.4.0`: run-and-die for coding agents — the `gsd-test run` handoff, a one-command installer, and non-blocking `--async`/`wait`/`status`
- `v1.5.0`: failure-first output — a quiet-by-default stream, a loud machine-readable verdict, and saved `FAILURES.md` / `failures.json` / `junit.xml` artifacts; plus ephemeral run storage you opt out of with `--keep` or `[storage]`
- `v1.6.0`: Node LTS matrix — test your project on every supported Node LTS line at once, fanned out across your Benches with per-Bench concurrency
- `v1.6.1`: fixes `run`/`wait`/`submit` reporting `infra_error` on large suites — the watchdog now drains its result envelope to the dispatcher before exiting
- `v1.6.2`: internal architecture cleanup (ADRs 0027/0028) — consolidated image/worktree prep, testable runner policy, pipeline god-file split, streaming-leg dedup; run-and-die now pulls the explicit `:<version>` Tester Image tag
- `v1.7.0`: branch-derived container names — `docker ps` on a Bench shows `gsd-test-<branch>-<runId>` instead of `keen_euclid`, and the Tier-2 reaper reaps only containers from the branch it is working on
- `v1.8.0`: extends that to the standard `gsd-test` path (v1.7.0 covered run-and-die only) and fixes the leak behind it — Pipeline containers were unlabeled, so nothing ever reaped the ones a crashed run left behind

## Unreleased

_Nothing yet — changes land here before the next tagged release._

## v1.8.0

### Added

- **The standard `gsd-test` path now names its containers too** ([ADR-0029](adr/0029-container-name-from-branch.md) §5). v1.7.0 shipped branch-derived names for run-and-die only; the multi-OS/Node-matrix path — the one most people actually run — still produced `keen_euclid`. Every cell's container is now `gsd-test-<branch-slug>-<os>-node<major>-<short-runId>` and carries the same four `sh.gsd-test.*` labels the run-and-die path emits. The `<os>-node<major>` cell segment is what keeps the concurrent cells of one matrix run from colliding on a name.
- **The branch is resolved from git when you don't pass `--head`.** A bare `gsd-test` asks the source repo for its current branch, so names are useful without extra flags. Detached HEAD or a non-repo source falls back to the `unknown` sentinel rather than failing the run.

### Fixed

- **Leaked Pipeline containers are finally reaped.** This is the real bug behind the cosmetic one. Because Pipeline containers carried *no labels at all*, the Tier-2 reaper's `--filter label=sh.gsd-test.run-id` could never see them — so a container orphaned by a crashed or killed `gsd-test` run survived indefinitely (observed in the wild at 37 hours, with 19 alive on one Bench at once). They were only ever cleaned up by `RunAll`'s deferred `docker rm -f`, which by definition does not run when the engine dies. Labeling them brings them under the existing "reap on next contact" contract (ADR-0021 Decision 2), and `gsd-test` now sweeps each distinct Bench once — scoped to its own branch — before scheduling any cell.
- **A run's container labels and its artifact directory now agree.** The run id was previously minted twice: once (post-hoc) for artifacts, never for containers. There is now one id per invocation, shared by both.

### Upgrade notes

- **Pipeline containers now have a 2-hour deadline.** They previously had none, because nothing reaped them. A container still alive past `runner.DefaultContainerTTL` is presumed leaked and can be swept by a later invocation of the **same branch** against the **same Bench** — a run in isolation is never at risk no matter how long it takes, and other branches are never touched. If you have a single (OS, Node) cell that legitimately runs longer than two hours, say so and the TTL should become configurable.
- **Containers started before v1.8.0 still need one manual cleanup.** They carry no `sh.gsd-test.*` labels, so the scoped sweep cannot see them either. `docker ps --filter label=sh.gsd-test.run-id` will *not* list them; find them by image instead:

  ```
  docker ps -q --filter ancestor=ghcr.io/open-gsd/gsd-tester-linux:v1.7.0-node22 | xargs -r docker rm -f
  ```

  Everything started from v1.8.0 onward is swept automatically.

### Why it matters

v1.7.0 answered "which branch is this container testing?" for the run-and-die path and left the common path untouched — and the gap was worse than cosmetic, because unlabeled containers are also unreapable. This closes both halves for both engines.

## v1.7.0

### Added

- **Run containers are named after the branch under test** ([ADR-0029](adr/0029-container-name-from-branch.md)). Every run-and-die dispatch now launches its container as `gsd-test-<branch-slug>-<short-runId>` instead of accepting Docker's random name (`keen_euclid`, `sleepy_kepler`, …). `docker ps` on a Bench now answers both "is this one mine?" (the `gsd-test-` prefix) and "which branch is it testing?" (the slug) at a glance, with no `docker inspect` round-trip at exactly the moment something else is already broken. The slug is derived from the run spec's `prBranch` (preferred) or `base`; the 8-character `runId` tail keeps concurrent runs of the same branch distinct. Names are capped at Docker's 63-byte ceiling, truncating the slug and never the tail, so the uniqueness guarantee never weakens (issue #123).
- **A `sh.gsd-test.branch` container label** carrying that same slug. The name is the human-readable ownership signal; the label is the machine-readable one the reaper matches on. Both come from one helper, so they cannot drift apart.

### Changed

- **The Tier-2 reaper now only reaps its own branch's containers.** `gsd-test submit --execute` for `fix/foo` previously swept every overdue container the runner had ever labeled — including a leftover from yesterday's `fix/bar` run. It now filters on the `sh.gsd-test.branch` label before applying the deadline filter, so each invocation cleans up only what it owns and leaves other branches' containers for their own invocations ([ADR-0029](adr/0029-container-name-from-branch.md) §3). The match is on the label value, not on parsing the container name, so it stays correct even where Docker substitutes a random name.

### Upgrade notes

- **Containers started before v1.7.0 are no longer swept automatically.** They carry no `sh.gsd-test.branch` label, so a branch-scoped sweep skips them by design. Clear any stragglers once with `docker ps --filter label=sh.gsd-test.run-id` and `docker rm -f <id>`; everything started from v1.7.0 onward is swept normally.
- The standard multi-OS Pipeline path is **not** covered by this scheme yet — it does not carry the branch through to its container leg, and its containers are torn down directly rather than by the Tier-2 reaper. Tracked as a follow-up in [ADR-0029](adr/0029-container-name-from-branch.md) §4.

### Why it matters

Leftover run containers on a shared Bench used to be both anonymous and collectively owned: you could not tell which branch spawned one without inspecting it, and any run could kill any other run's leftovers. Branch-derived names remove the first problem at `docker ps`; branch-scoped ownership makes the second impossible.

## v1.6.2

### Changed (internal)

Architecture-deepening refactors behind [ADR-0027](adr/0027-two-execution-engines-behind-shared-preparation-primitives.md) and [ADR-0028](adr/0028-runner-policy-tested-via-extraction-not-dependency-injection.md). No user-facing API change; the full test suite stayed green throughout.

- **Consolidated Tester Image + worktree preparation** ([ADR-0027](adr/0027-two-execution-engines-behind-shared-preparation-primitives.md)). The standard multi-OS path and the run-and-die path now share one image-policy module — `images.Ref` encodes the [ADR-0024](adr/0024-node-matrix-tester-images.md) tag convention; `images.VerifyImageVersion` is the single version-sentinel check — and one `worktree.Prepare` (resolve + conditional construct). The two execution engines (Pipeline + Watchdog) stay separate; they're a real seam, not duplication.
- **Runner policy is now tested via extraction, not dependency injection** ([ADR-0028](adr/0028-runner-policy-tested-via-extraction-not-dependency-injection.md)). `runner.Aggregate` (verdict/exit-code classification) and `runner.ResolveEffective` (flag→config precedence) are pure, directly-tested modules; `Run` keeps its effects. The `workerPIDAlive` liveness guard is now an injectable test seam.
- **Split the pipeline god file** into `pipeline.go` (legs), `events.go`, `queue.go`, and `errors.go`.
- **Deduped the streaming legs.** One `streamAndCapture` helper and one `streamError` type replace the copy-pasted NpmCI/Build/RunTests streaming blocks and the three near-identical typed errors.
- **cmd handlers take `io.Writer`** instead of `*os.File`, so tests can use a `bytes.Buffer`.

### Fixed

- **Run-and-die now pulls the explicit `:<version>` Tester Image tag** instead of untagged (`:latest`). Semantically equivalent per [ADR-0024](adr/0024-node-matrix-tester-images.md) (the plain tag resolves to the Active-LTS build) and the version was already verified via the OCI sentinel label — but the explicit tag is more correct and removes a latent drift risk. A Bench holding only an untagged image will now fall back to building from the in-repo Dockerfile.

### Why it matters

No change to the common path. The cleanup concentrates the policies that were drifting (image tag/version, streaming capture) so the next fix applies once; the tag-pin removes a silent-drift risk where `:latest` could move under a pinned config.

## v1.6.1

### Fixed

- **Large suites no longer fail with `infra_error`.** Every run-and-die dispatch (`gsd-test run` / `wait`, `submit --execute`) could return `outcome:"infra_error"` (exit 2) with `parse watchdog envelope: unexpected end of JSON input` on a large suite — the container ran the whole suite green, but the dispatcher received a truncated result. The in-container watchdog wrote its multi-MB JSON envelope to stdout and exited before the kernel pipe (~64 KB buffer) had drained, so the tail was lost. The watchdog now defers its exit until stdout has fully drained, so the complete envelope always reaches the dispatcher (issue #111). The legacy synchronous `gsd-test` path was never affected — it streams events rather than relying on the envelope.

### Why it matters

The truncation scaled with suite size, so it hit exactly the large suites that most need unattended run-and-die dispatch — and because the run-and-die verdict gates the gsd-core push gate, an affected suite silently blocked pushes despite passing. Upgrade to v1.6.1 (binary **and** Tester Images) to clear it.

## v1.6.0

### Added

- **A Node.js version axis.** `gsd-test` now tests each OS against more than one Node major at once. Add a `[node]` table to `config.toml` (`linux = ["22", "24"]`) or pass `--node 22,24`; absent config, it uses the currently-supported Node LTS lines. Each `(OS, Node)` pair is a **cell** with its own live stream (`linux-node22`, `linux-node24`), its own row in the verdict's `per_os`, and its own `<testsuite>` in `junit.xml` (issue #108, [ADR-0024](adr/0024-node-matrix-tester-images.md)).
- **Tester Images per Node LTS major.** Images are published per major with a node-suffixed tag — `ghcr.io/open-gsd/gsd-tester-linux:v1.6.0-node22` — and carry a companion `sh.gsd-test.node-major` sentinel label. The Active-LTS major additionally keeps the plain `:v1.6.0` / `:latest` tags, so existing single-version configs keep resolving unchanged.
- **Fan-out across Benches with per-Bench concurrency.** Cells are dispatched, not statically pinned: all cells for an OS are pulled from a shared queue by whichever eligible Benches have free capacity, so a bigger Bench takes more work automatically (least-loaded, no tuning). A new per-Bench `capacity` field caps concurrent containers; leave it unset and `gsd-test` uses the Bench's own CPU count, so a capable Bench runs several cells side by side out of the box (issue #108, [ADR-0025](adr/0025-capacity-aware-fanout-scheduler.md)).

### Why it matters

A suite that passes on Node 22 can break on Node 24 — API removals, V8 changes, `node:test` runner changes. Before v1.6.0 you exercised exactly one Node line and found out about the others in production. Now one `gsd-test` run covers every supported LTS line, and because the cells fan out across your Benches in parallel, widening the matrix spends your hardware instead of your wall-clock. Nothing changes for anyone who does not opt in: absent `[node]` and `capacity`, a run behaves exactly as it did in v1.5.0.

### Example

```toml
# config.toml
[node]
linux = ["22", "24"]

[[benches]]
name = "lab-rig-1"
host = "lab-rig-1"
os   = "linux"
capacity = 6        # run up to 6 cells at once; unset = the Bench's CPU count
```

```bash
gsd-test                    # runs linux-node22 and linux-node24, fanned out
gsd-test --node 24          # this run: Node 24 only, every target OS
```

The verdict's `per_os` and the saved `junit.xml` report each `(OS, Node)` cell separately. See [The Node version matrix](node-matrix.md) for the full picture.

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
