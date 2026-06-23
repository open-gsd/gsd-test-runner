# Run-and-die Execution

This document explains *why* run-and-die exists and how its pieces fit together. For hands-on material see the [tutorial](run-and-die-tutorial.md), the [how-to guides](run-and-die-how-to.md), and the [reference](run-and-die-reference.md). For the locked design decisions, read [ADR-0021](adr/0021-run-and-die-execution-and-two-tier-reaping.md).

## The problem: orphaned test processes

When a coding agent (Claude Code, Codex) runs `node --test` directly on your workstation, a single misbehaving test can take the whole machine down. `node --test` defaults to `--test-timeout=Infinity`, and process isolation spawns one child per test file. A test that leaks a handle — an open socket, a dangling timer, a child process, a file watcher — keeps its runner child alive forever. The agent sees no error, moves on, and starts the next run. The orphans accumulate. Eventually the workstation runs out of resources and falls over.

The ordinary `gsd-test` flow already offloads test *execution* to a Bench, so the workstation is never burdened. But an agent that shells out to `node --test` locally bypasses that entirely. Run-and-die closes that gap: the agent submits a *run spec* instead of spawning `node`, and the run happens in a disposable container that **dies** when it is done — taking any orphans with it.

## The core idea

A run-and-die run is one-shot and self-terminating:

1. The agent submits a run spec to the Local Engine (`gsd-test submit`) instead of running `node --test`.
2. The Engine builds a disposable, resource-capped container on a Bench and copies the worktree in.
3. Inside the container, dependencies are installed and the project is built (`npm ci` + `npm run build`, when a `package.json` is present) — *before* the deadline starts, so install time is never charged against it.
4. A **watchdog** then wraps `node --test` under a hard deadline.
5. The container runs, streams back a structured result, and is removed (`--rm`). Nothing persists; no process outlives the container.

The result is a *loud* outcome. A run that gets killed comes back as `outcome: "reaped"` with a record of exactly where it was when it died — never a silent hang.

## Why a deadline you can trust

A timeout is only useful if it is neither too tight (killing healthy work) nor too loose (a one-hour ceiling on a thirty-second suite). Run-and-die asks the agent for an *estimate* — agents usually know the order of magnitude — and kills at `estimate × 1.5`, bounded by an absolute one-hour cap. A thirty-second suite that has been running fifty seconds is reaped, rather than squatting for an hour.

When no estimate is given, the deadline falls back to the median of recent passing runs (from accumulated [telemetry](run-and-die-reference.md#telemetry-record)), and only to the one-hour cap when there is no history at all. The clock starts when the *tests* start, not when the container starts, so a slow `npm ci` is never charged against the estimate.

## Two-tier reaping

A watchdog that can itself wedge is not a guarantee. Run-and-die therefore kills in two independent tiers:

- **Tier 1 — the in-container watchdog.** Cheapest and most precise: it knows which test was running, so its kill record points straight at the suspect. It escalates `SIGTERM` → grace → `SIGKILL` of the whole process group (on Windows, `taskkill /T` of the tree, since Windows has no process groups).
- **Tier 2 — the external reaper.** The Local Engine itself. Every run container is labelled with its deadline; on the Engine's next contact with a Bench it kills any container past its deadline. This survives a wedged Tier 1 because the killer lives on a *different machine* than the container.

The hard guarantee underneath both tiers is the container boundary: `docker kill` (and `--rm`) tears the container and every process in it down identically on Linux and Windows. Per-OS signal handling only affects how *gracefully* Tier 1 kills; it never affects whether orphans can survive.

Why not a long-lived reaper daemon on each Bench? Benches are personal hardware kept deliberately stateless — they pull images, run `--rm` containers, and hold nothing. A daemon adds an install-and-upgrade surface that the "reap on next contact" sweep avoids entirely. See [ADR-0021 Decision 2](adr/0021-run-and-die-execution-and-two-tier-reaping.md).

## Finding the bugged test, not just killing it

Killing a runaway is damage control; the point is to *fix* the test that keeps running away. Every run records per-test telemetry — durations, which test tripped the reaper, clean-exit flags — to a per-repo log on the workstation. Aggregated across runs this yields a "runaway leaderboard": the tests that repeatedly trip the reaper are the bugged ones. Bump the estimate and you have papered over the problem; read the leaderboard and you fix the root cause.

The leaderboard ranks on **two** signals, on purpose (Goodhart's Law): reaper trips, *and* unclean exits. Ranking on trips alone would be gameable — raise a leaky test's estimate so it stops being killed and it falls off the board without being fixed. So a second, estimate-proof signal sits alongside it: a test that *completes* but leaks a handle (`exited_clean: false`). A leak probe preloaded into each test child catches the leftover handle at exit (`--test-force-exit` makes the process exit even with the handle open), so the leak surfaces no matter how generous the budget. Raising the estimate cannot hide it. (The signal is file-level and best-effort, and a no-op under `isolation: "none"` — see the [reference](run-and-die-reference.md#per-test-leak-detection).)

## How it relates to the normal flow

Run-and-die is additive, not a replacement. The ordinary `gsd-test` run (no subcommand) still drives the five-phase pipeline across your configured targets and produces per-OS reports — see [Getting Started](getting-started.md) and [Architecture](architecture.md). Run-and-die is a second front door (`gsd-test submit`) aimed at *agents*, built on the same machinery: the same Benches, the same Tester Images, the same copy-in worktree, the same JSON result shape (extended with the `kill` record). The result envelope is `schema_version: 2` — additive over the per-OS report, with `outcome` and `kill` fields added.

## Artifact lifecycle and ephemeral mode

A run-and-die container is disposable by design — `--rm` removes it the moment the watchdog exits. The artifacts a run leaves on the host follow the same philosophy.

The authoritative record of a run is its **stdout**: the human-readable render plus the final machine verdict line (ADR-0023). The files written under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/` — `FAILURES.md`, `failures.json`, `junit.xml`, the JSONL event stream — are a convenience copy of that same information, not a database you are expected to curate. Left unmanaged they accumulate forever, one directory per run, with no signal to the operator.

So by default `gsd-test` treats them as transient scratch and releases them once they have been consumed:

- After `gsd-test wait` has rendered the result, the artifacts have served their purpose — the caller is holding the full output on stdout. `wait` therefore deletes the run's state file and artifact directory, and emits a verdict line with the artifact paths omitted (there is nothing left to point at). This is "consume on read".
- A blocking `gsd-test run` cannot delete in place — the caller may still open `FAILURES.md` after the process exits. Those runs are reclaimed instead by a retention sweep at the start of the *next* run, bounded by `artifact_ttl` and `keep_last_runs`.
- `gsd-test status` is a progress check, not a consumer, so it never releases anything.

You opt out when you genuinely need the files to persist — comparing two runs side by side, attaching artifacts to a CI record, or debugging the harness itself. `--keep` does this for a single run; `keep_artifacts = true` does it permanently. Both also disable the retention sweep, returning to keep-everything behavior.

The trade-off is deliberate. The default keeps a busy Bench's disk bounded and self-managing, at the cost of artifacts being short-lived: if you treat stdout as the record — as the verdict contract intends — you never notice they are gone. If you need the directory to be durable storage, set `[storage]` accordingly (see the [configuration reference](configuration.md#storage)).

## Trade-offs and limits

- **`isolation: "none"` is faster but less precise.** Under process isolation a wedged test is a contained child the watchdog reaps with exact attribution. Under `none` all tests share one process, so a hang wedges everything and the kill record marks attribution best-effort. Use `none` only for suites you know are clean.
- **The estimate is load-bearing.** A wildly low estimate reaps healthy runs; the telemetry median fallback and the 30-second floor blunt this, but a good estimate is still better than none.
- **Windows kill is verified by integration, not signals.** The `taskkill /T` path is implemented, but its orphan-free guarantee is asserted by a Bench integration test rather than by signal semantics — see [ADR-0021 Decision 4](adr/0021-run-and-die-execution-and-two-tier-reaping.md).
- **Attribution is best-effort, and weakest exactly when you most want it.** `kill.last_active_test` needs the reporter to have emitted a `test:start` before the kill. A test that wedges the runner with a synchronous CPU loop blocks the reporter too, so the kill record may not name the culprit even though the container was reaped. The teardown guarantee always holds; the *pointing finger* does not.
