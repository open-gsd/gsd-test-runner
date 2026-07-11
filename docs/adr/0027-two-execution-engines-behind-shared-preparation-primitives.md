# ADR-0027 — Two execution engines behind shared preparation primitives

**Date**: 2026-07-10
**Status**: Accepted (2026-07-10)
**Context**: Architecture review — the standard multi-OS path and the run-and-die single-OS path duplicate orchestration scaffolding (config load, bench selection, image acquisition + version verification, worktree construction) across `internal/runner` and `cmd/gsd-test/main.go`.

## Context

A routine architecture review surfaced that the Local Engine's two execution paths share a near-identical preamble — Load → Select → EnsurePresent → Resolve → worktree.Construct — implemented twice, with the image-reference and version-verification policy spelled inline in both places (and already drifting: the standard path builds `ghcr.io/open-gsd/gsd-tester-<os>:<version>-node<major>` per ADR-0024, the run-and-die path builds the untagged `ghcr.io/open-gsd/gsd-tester-<os>` that resolves to the Active-LTS plain tag). The first instinct was to unify the two paths into one orchestrator.

Closer reading shows the two paths are not two orchestrations of one engine — they are **two different execution engines**:

- The **Pipeline engine** (`internal/pipeline`, standard multi-OS path) runs 8 discrete legs against an idle `sleep infinity` container, with leg-granular fail-loud control and live JSONL streaming. It has no in-container deadline.
- The **Watchdog engine** (`internal/dispatch`, run-and-die path, ADR-0021) runs a single-shot container whose PID 1 is a Node watchdog that does ci+build+test in one process under an estimate-aware deadline, with two-tier reaping and runaway-test telemetry. It does not stream live and does not fan out.

Converging on one engine would re-litigate settled ADRs: giving the multi-OS path a watchdog loses the 8-leg fail-loud granularity and live streaming; giving run-and-die the 8-leg treatment loses the in-container deadline reaping that is ADR-0021's entire value. Per the "two adapters = real seam" principle, the execution-engine split is a real seam, not accidental duplication.

The shared preamble, by contrast, earns nobody's keep being duplicated — but it is **not a linear sequence**: the Pipeline engine defers bench selection + image-ensure into the capacity scheduler worker (ADR-0026, because capacity-aware fan-out does not know which Bench runs which cell until dispatch time), while the Watchdog engine is single-shot and resolves its bench up front. Both orderings are correct for their own shape.

`internal/images/doc.go` already flags the version-sentinel-check location as "the open question deepening candidate #2 will close in a future ADR." This ADR closes it.

## Decision

**1. Do not collapse the two execution engines.** The Pipeline engine and the Watchdog engine remain separate adapters over the execution seam. No single `Run(ctx, opts)` unifies them.

**2. Consolidate the shared preparation policy into two small deep modules, not a linear spine.**

   - **Image policy** (`internal/images`):
     - `images.Ref(os, version, nodeMajor string) ImageID` encodes the ADR-0024 tag convention once. `nodeMajor == ""` → plain `:<version>` tag (Active-LTS back-compat, the run-and-die path); otherwise `:<version>-node<major>` (the matrix path). Replaces the two inline `fmt.Sprintf` sites in `internal/plan/plan.go` and `cmd/gsd-test/main.go`.
     - `images.VerifyImageVersion(ctx, runner, imageID, want) error` is the single version-sentinel check, `reaper.Runner`-based. Both the Pipeline engine's `CheckImageVersion` leg and the Watchdog engine's pre-run check call it. (Closes the open question in `internal/images/doc.go`.)
     - One `images.ImageVersionMismatch` type replaces the two divergent types (`dispatch.ImageVersionMismatch` with `Want`/`Got`, `pipeline.ImageVersionMismatch` with `Expected`/`Actual`).

   - **Worktree preparation** (`internal/worktree`):
     - `worktree.Prepare(ctx, repo, baseRef, prRef) (*Worktree, error)` owns ref resolution (via `internal/refs`) plus the conditional construct. `baseRef == ""` returns a `Worktree` backed by `repo` directly (no-op cleanup — the "run the working tree as-is" fast path); otherwise it resolves both refs and calls `Construct`. Both paths call it. `Construct` stays as the lower-level internal seam for callers that already hold resolved SHAs.

**3. `config.Load` and `bench.NewSelector` stay as ordinary call sites in each path.** They are one-liners against already-clean modules; extracting them into a shared helper concentrates nothing.

## Consequences

+ The image-reference and version-verification policy lives in one tested site. The next tag-convention change (e.g. adding Node 26 per ADR-0024) is a one-line edit to `images.Ref`, not a find-and-replace across two paths.
+ The "run repo as-is unless a base ref is given" semantic — previously buried inline in `cmd/gsd-test/main.go` with no name — becomes an explicit, tested signature (`worktree.Prepare` with empty `baseRef`).
+ `internal/worktree` gains a new import edge to `internal/refs`. Both are git-plumbing (`worktree` already shells out to `git worktree`/`git merge`; `refs` is `git rev-parse`), so the composition is coherent: "resolve refs then build a PR-merged worktree" is a worktree-level operation.
+ `internal/dispatch` loses its `ImageVersionMismatch` and `VerifyImageVersion` (moved to `images`); `internal/pipeline`'s `CheckImageVersion` leg becomes a thin caller of `images.VerifyImageVersion` and loses its divergent error type.
- This ADR does **not** address the testability of the orchestration itself (`runner.Run` remains without direct unit tests; the run-and-die handlers still take `*os.File`; `workerPIDAlive` remains non-injectable). That is the next deepening candidate and is intentionally out of scope here.
- This ADR does **not** reduce the two post-preparation code paths to one. `cmd/gsd-test` still has a standard path and a run-and-die path after preparation. That is correct — they are two execution engines — but it means the "did I update both paths?" risk survives for anything *below* the execution seam (engine-internal policy), which this ADR does not touch.

## Alternatives considered

- **Collapse into one `Run(ctx, opts) int` that both subcommands call.** Rejected: would require either giving the multi-OS path a watchdog (losing 8-leg fail-loud granularity and live streaming) or giving run-and-die the 8-leg treatment (losing in-container deadline reaping, ADR-0021's core value). Re-litigates ADR-0021 and ADR-0026.
- **A full-lifecycle or linear "spine" module owning Load → Select → Ensure → Construct → execute → verdict.** Rejected: the preamble is not linear. The Pipeline engine cannot pick its Bench or ensure its image before constructing the worktree (ADR-0026 folds EnsurePresent into the scheduler worker); the Watchdog engine picks up front. A forced linear spine would fight ADR-0026 or force run-and-die to pretend it fans out. The interface to express both "fan out N runs with live events" and "run one thing, dump an envelope, append telemetry" would be wide — the shallow-module failure mode.
- **A new `internal/runprep` package for `worktree.Prepare`.** Rejected: a one-function package is its own shallowness; `worktree` is the natural home and `refs` is a coherent import edge.
