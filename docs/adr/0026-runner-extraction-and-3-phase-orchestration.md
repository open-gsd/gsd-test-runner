# 0026 — Amendment to ADR-0018: 3-phase orchestration + runner extraction

Status: Accepted (2026-07-10)

## Context

ADR-0018 Decision 5 prescribes a 5-phase orchestrator structure:

1. **Load** — `config.Load`
2. **Plan** — `plan.Build`
3. **EnsureImages** — parallel pre-phase, `images.EnsurePresent` for all (Bench, ImageID) pairs
4. **RunPipelines** — parallel per-OS, `Pipeline.RunAll`
5. **Aggregate + Render** — collect Reports, map to exit code

Decision 1 of the same ADR states: *"EnsurePresent runs as a pre-phase, parallel across all (Bench, ImageID) pairs, before any Pipeline starts."*

The Node matrix enhancement (#108) introduced capacity-aware, pull-based bench assignment via `internal/schedule`. Benches are no longer statically assigned per OS — the scheduler dynamically assigns each (OS × Node) cell to the least-loaded Bench at dispatch time. This makes the pre-phase EnsureImages structure impossible: the Engine cannot guarantee an image is present on a specific Bench before the scheduler decides which Bench will run that cell.

The code already folded EnsurePresent into the scheduler worker (each worker calls `images.EnsurePresent` on its assigned Bench before constructing the Pipeline). This amendment formalizes that change and records the extraction of the orchestration into `internal/runner`.

## Decision

**The orchestrator has 3 phases, not 5:**

1. **Load** — `config.Load` reads config, resolves targets/pin/exclude against config defaults
2. **Plan** — `bench.NewSelector` + `plan.Build` resolve (OS × Node) Runs
3. **Schedule** — `schedule.Run` dispatches Runs across Benches with per-Bench capacity (least-loaded, pull-based). Each worker:
   - Calls `images.EnsurePresent` on its assigned Bench (folds the old EnsureImages phase)
   - Constructs the Pipeline and calls `RunAll`
   - Returns a `pipelineResult`

Aggregation, rendering, artifact emission, and verdict emission happen after the scheduler returns.

**The orchestration lives in `internal/runner`, not `cmd/gsd-test/main.go`.**

The runner module owns the full lifecycle from `config.Load` through verdict emission. Its interface is:

```go
func Run(ctx context.Context, opts Options) int
```

`Options` carries raw flag values + `io.Writer` for stdout/stderr. The runner writes the live renderer stream, the failure-first digest artifacts, and the machine-readable verdict as the last stdout line (ADR-0023 Decision 2). It returns the exit code (0/1/2 per ADR-0009).

The runner also exports the shared verdict-emission helpers (`WriteVerdict`, `WriteInconclusiveVerdict`, `WriteRunArtifacts`, `EmitRunDieArtifacts`) used by the run-and-die path in `cmd/gsd-test/main.go` (`dispatchRun`, `runSubmit`, `runRun`, `waitRun`, `runWorker`).

`cmd/gsd-test/main.go` retains: subcommand dispatch, flag parsing, `--version`, context creation, and the full run-and-die path (`dispatchRun` + the `submit`/`run`/`wait`/`status`/`__run-worker` subcommands).

## Why EnsurePresent can't be a pre-phase anymore

ADR-0018's 5-phase structure assumed one Bench per OS — the Engine knew which Bench would run which OS before any pipeline started, so it could ensure the image was present on that specific Bench in a pre-phase.

The Node matrix (#108) introduced capacity-aware fan-out: multiple cells for the same OS can run on different Benches, and the scheduler assigns cells to Benches dynamically based on capacity at dispatch time. The Engine no longer knows which Bench will run which cell until the scheduler decides. Pre-phase EnsurePresent would need to ensure the image on ALL candidate Benches for each OS — wasting pull bandwidth and build work on Benches that never run a cell.

Folding EnsurePresent into the worker is the natural consequence: the worker ensures the image on its assigned Bench only, immediately before constructing the Pipeline. This adds per-cell image-management latency to the first cell on a cold-cache Bench, but subsequent cells on the same Bench hit the cached image. The latency is dominated by the first pull; warm-cache cells see no overhead.

## Consequences

+ The runner module concentrates the orchestration behind one interface — testable through `Run(ctx, opts)` without capturing stdout/stderr from a string-array CLI entry point.
+ `cmd/gsd-test/main.go` shrinks from 1271 to 881 lines. The default path is a single `runner.Run()` call.
+ The runner's test surface includes pre-pipeline error paths (bad config, no targets, no bench), capacity resolver unit tests, commaSplit, and output/artifact tests — all previously in `cmd/gsd-test/*_test.go`.
+ Per-cell image-management latency shifts from a parallel pre-phase to sequential-per-Bench. On cold caches, the first cell on each Bench pays the pull cost; subsequent cells are warm.
+ The `internal/runner` package imports `pipeline`, `plan`, `renderer`, and `schedule` — these are no longer imported by `cmd/gsd-test/main.go`.

## What this amends in ADR-0018

- **Decision 1** (EnsurePresent as pre-phase) → superseded: EnsurePresent runs inside each scheduler worker.
- **Decision 5** (5-phase structure) → amended to 3 phases: Load → Plan → Schedule.
- The illustrative Go pseudocode in ADR-0018 is superseded by `internal/runner/runner.go`.
