# ADR-0021 — Run-and-die execution and two-tier reaping

**Date**: 2026-06-16
**Status**: Accepted (2026-06-16)
**Context**: Issue #60 — "run-and-die" containerized `node --test` execution: agent-facing run-spec front door, estimate-aware reaper/watchdog, and telemetry to fingerprint runaway tests.

## Context

When a coding agent (Claude Code or Codex) runs `node --test` directly on the Dev Workstation, a hanging or misbehaving test leaks orphaned `node` children. `node --test` defaults to `--test-timeout=Infinity` and process isolation spawns one child per test file, so a test that leaks a handle (open socket, dangling timer, child process, watcher) keeps a child alive indefinitely. The agent moves on, orphans accumulate, and the Workstation eventually falls over.

The target architecture already offloads execution to Benches (the Dev Workstation orchestrates; Benches execute — see CONTEXT.md, ADR-0007). Issue #60 extends that pipeline with three additive capabilities: an agent-facing "submit a run" contract so an agent never spawns a local `node` process, an estimate-aware watchdog/reaper that kills runaways and reports *where* they died, and telemetry rich enough to find the bugged test. This ADR ratifies the five open design questions left in #60 plus the two schema/transport decisions they force, so implementation can proceed against locked decisions (consistent with how ADR-0001–0020 front-run code).

This is additive: a new front door, a watchdog, and a richer result envelope on machinery that already exists (PR-merged worktree per ADR-0002, dockerexec transport per ADR-0014, JSONL drain per ADR-0015, image-version sentinel per ADR-0011). It is not a rewrite.

## Decisions

### Decision 1 — Estimate-aware deadline and fallback (Q1)

`overrunFactor` defaults to `1.5`. The effective deadline is `min(estimateMs × overrunFactor, hardCapMs)`, with `hardCapMs` defaulting to 1h. When the agent supplies no `estimateMs`, fall back to the **telemetry median** for that suite once telemetry exists (Decision 3); until then, fall back to the **1h hard cap only** — never a fabricated tight default that would false-positive-kill a legitimately long suite.

The watchdog **arms at the start of the RunTests leg** (first runner spawn / first test event), not at container start. `npm ci` and `build` are separate legs with their own cold-cache variance (ADR-0008), and the agent's estimate is for the *test run*; counting install/build time against it would reap suites for the wrong reason. The 1h hard cap still bounds the whole run as the absolute ceiling. A `minDeadlineMs` floor (≈30s) prevents a tiny estimate from killing during runner warmup.

### Decision 2 — The Local Engine is the external reaper; no resident Bench process (Q2)

Reaping is two-tier:

- **Tier 1 — in-container watchdog.** Cheapest and most precise; knows test-level context. Arms at the effective deadline, snapshots state, emits a kill record, then escalates the kill (Decision 4).
- **Tier 2 — the Local Engine itself, acting as remote reaper.** The Engine already drives every run over SSH with a cancellable `context` (`dockerexec.Run`/`Stream`, ADR-0014 — `ctx.Err()` propagates cleanly on cancel). It arms a deadline timer and on expiry issues `docker kill <run-container>` over SSH and synthesizes the kill record marked `reapedBy:"external"`. The killer lives on a *different machine* than the wedged container — defeating the exact failure mode we fear (a wedged Tier-1 watchdog) without any sidecar.

We reject both a long-lived per-Bench daemon and a per-run sidecar container. Benches are personal hardware that reboot/sleep and are deliberately kept stateless — pull tagged images, run `--rm` containers (ADR-0002), hold nothing. A daemon adds an install/upgrade/version-drift surface that contradicts that posture; a sidecar adds lifecycle complexity for no gain over reusing the Engine's existing transport and context.

For durability without a daemon: every run container is labeled `sh.gsd-test.run-id` and `sh.gsd-test.deadline=<epoch>` (the reverse-DNS convention from ADR-0011). If the Workstation dies mid-run, the **next** Engine invocation against that Bench sweeps and kills any labeled container past its deadline — "reap on next contact" gives daemon-like guarantees with zero resident process.

### Decision 3 — Telemetry aggregates on the Dev Workstation, per-repo (Q3)

The per-run telemetry envelope is produced in-container and drained back exactly like JSONL today (ADR-0015), then appended to a Workstation-side per-repo log at `~/.local/state/gsd-test/<repo>/telemetry.jsonl` (append-only, schema-versioned like the Per-OS report per ADR-0013). The Bench stays stateless.

The consumer — the agent that reads the "runaway leaderboard" and surfaces it in the Per-OS report — runs on the Workstation, so storage is co-located with the reader. Telemetry is repo-scoped ("which test in *this* repo is bugged"); a Bench serves multiple repos and workstations, so storing there would mix scopes and burden a machine designed to hold nothing.

### Decision 4 — Container teardown is the kill primitive; per-OS signals are the Tier-1 precision path only (Q4)

Correctness does not depend on Windows signal semantics. Windows has no real POSIX signals or process groups, so killing a parent does not reap `node --test`'s spawned children; Tier-1's signal-tree approach is unreliable there. `docker kill` / `docker rm -f` tears down the whole container and every process in it identically on Linux and Windows, and `--rm` (ADR-0002) makes teardown automatic — that is the guarantee.

The Tier-1 precision path is per-OS and best-effort (for a graceful Reporter flush): `SIGTERM` → grace window → `SIGKILL` of the process group on Linux; `taskkill /T /F /PID <runner>` (`/T` = whole tree) on Windows. The container-level kill (Tier 2) is always the backstop. A Windows integration test asserting **zero orphaned `node.exe`** after a reaped Windows run is a required gate (there is no Windows runtime ADR yet, so this is the empirical contract).

### Decision 5 — Default `isolation:"process"`; `none` is an opt-in fast path with degraded attribution (Q5)

`isolation` defaults to `process` (Node's default, #60's default). Under process isolation a wedged file is a contained child, so Tier-1 can kill just it and `kill.lastActiveTest` / `kill.inFlightTests` are accurate — the best telemetry.

`isolation:"none"` is allowed as an opt-in fast path for known-clean suites, but a hang wedges the single shared runner: per-file granularity is lost. When `none` is selected, the kill record must stamp `granularity:"process"` and mark `lastActiveTest` best-effort so the agent is not misled into blaming the wrong test. `--test-timeout` and `--test-force-exit` still function under `none` (individual tests can still fail-fast); only *attribution precision* degrades, not safety. `none` never weakens the container-level guarantee.

### Decision 6 — `reaped` is a first-class report outcome; bump `schema_version` 1 → 2

ADR-0013's `Report` is `kind: pass|fail` with `LegError` for infra failures. A reap is neither. The result envelope gains `outcome: passed | failed | reaped | infra_error` plus a `kill{}` block (`reason`, `effectiveDeadlineMs`, `elapsedMs`, `lastActiveTest`, `inFlightTests`, `reapedBy`, `signalChain`), and `schema_version` bumps to `2`. A reap is therefore loud and structured, never a silent hang — honoring the fail-loud contract (ADR-0004).

### Decision 7 — No new transport

Every mechanism above reuses existing machinery: `dockerexec` + `context` cancellation (ADR-0014), the JSONL drain (ADR-0015), the sentinel-label convention (ADR-0011), `--rm` teardown and copy-in worktree (ADR-0002). The feature is a new front door (`gsd-test submit`), a watchdog, and a richer envelope — not new plumbing.

## Consequences

- An agent submits a run spec (CLI flag, stdin, or `gsd-test submit`) and never spawns a local `node` process; orphans die with the `--rm` container on a Bench, so the Workstation is never the thing that falls over.
- Runs are bounded three ways — the runner's own `--test-timeout`/`--test-force-exit`, the Tier-1 watchdog, and the Engine-as-reaper plus stale-label sweep — so "nothing outlives its budget" holds even if any single tier wedges.
- Benches remain stateless: no daemon to install, upgrade, or version. The cost is that durability across a Workstation crash relies on "reap on next contact" rather than instantaneous external kill — acceptable because the container's own `--rm` and Tier-1 watchdog still apply.
- The report schema changes (`schema_version` 2). Consumers of the Per-OS report must handle the new `outcome`/`kill` fields; this is a breaking schema bump and must land with its parser and renderer changes together.
- Windows reaping correctness is asserted empirically (orphaned-`node.exe` integration test) rather than by signal semantics, which is the honest contract until a Windows runtime ADR exists.
- Telemetry accrues per-repo on the Workstation, enabling a runaway leaderboard the agent can surface ("⚠ test/integration/db.test.js tripped the reaper 4/5 recent runs") so root causes get fixed instead of estimates getting bumped.
- `isolation:"none"` trades reaper precision for speed; the degraded-attribution stamp keeps that trade-off explicit rather than silently misleading.

## Alternatives considered

- **Long-lived per-Bench reaper daemon.** Survives Bench reboots and reaps instantly, but adds a resident process with its own install/upgrade/version-drift lifecycle on a machine the architecture keeps stateless. Rejected in favor of Engine-as-reaper + stale-label sweep (Decision 2).
- **Per-run external reaper sidecar container.** Simpler than a daemon, but still spawns and manages an extra container per run for a job the Engine's existing `context`-cancelled SSH transport already does. Rejected (Decision 2).
- **Storing telemetry on the Bench.** The Bench sees all runs, but it serves multiple repos and workstations and is meant to hold nothing; co-locating with the consuming agent on the Workstation is cleaner (Decision 3).
- **Relying on in-container signal trees for the kill guarantee.** Works on Linux, unreliable on Windows where there are no POSIX process groups. Container teardown is uniform across OSes and becomes the guarantee instead (Decision 4).
- **Inventing a tight default `estimateMs` when none is supplied.** Would false-positive-kill legitimately long suites and erode trust in the reaped outcome. Falling back to telemetry median (then the 1h hard cap) avoids punishing the no-estimate case (Decision 1).

## Implementation status

Landed (TDD, verified — Go suite + `node --test` + live Docker where noted):

- `internal/runspec` — run-spec parse/validate, `Budget.EffectiveDeadlineMs` (Decision 1).
- `gsd-test submit` — the run-spec front door (accept/normalize/assign RunID).
- `internal/report` — `Outcome` + `KillRecord`, `schema_version` 2 (Decision 6).
- `internal/reaper` — `Overdue` + Docker-backed `Sweep` (Decision 2); reaped against a live container.
- `reporter/watchdog.mjs` — Tier-1 watchdog + CLI entrypoint (Decision 4); a real hanging `node --test` is reaped in a `node:22-alpine` container end-to-end.
- `internal/dispatch` — hardened `node --test` / `docker run` arg builders (§E/§B) and `dispatch.Run` (execution core: assemble → run → envelope → report).
- `internal/telemetry` — per-repo JSONL log + runaway leaderboard (Decision 3 / §F).
- Dockerfiles bake `watchdog.mjs`; `agent-integration/` routes `node --test`/`npm test` to the front door (§G).

Also landed: `gsd-test submit --execute` wires the front door through Bench selection + `images.EnsurePresent` + copy-in (`dispatch.Exec`) into the watchdog run (proven end-to-end against the real Linux Tester Image); the OCI image-version sentinel check on this path (`dispatch.VerifyImageVersion`, ADR-0011); and the telemetry-median estimate fallback with per-run append to the persistent Workstation log (`telemetry.MedianDurationMs` / `RepoLogPath` under `$XDG_STATE_HOME`).

Remaining: the Windows orphaned-`node.exe` Bench gate (Decision 4) — the `taskkill /T` path and a gated integration test (`TestE2E_Windows_WatchdogReapsViaTaskkill`) exist, but verification requires a Windows-container Bench (skips on Linux/macOS daemons).
