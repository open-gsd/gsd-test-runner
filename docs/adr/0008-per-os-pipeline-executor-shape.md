# 0008 — Per-OS Pipeline Executor: step chain, structured event stream, LegError envelope, external Bench selection

Status: Accepted (2026-05-23)

## Context

ADR-0004 mandates that every pipeline leg fails loud with structured diagnostics. ADR-0006 commits the Local Engine to Go. The Per-OS Pipeline Executor (one per target OS per run) owns 8 of the 11 pipeline legs: image-version sentinel check, copy-in, container start, npm ci, build, test run, JSONL drain, parse. The remaining three (base-fetch, PR merge, scratch-clone setup) belong upstream in the PR-merged worktree construction module — that work runs once per Local Engine invocation regardless of how many OSes are targeted, and feeds its output to N Executors.

Four independent design decisions shape the Executor. Each has real tradeoffs. None has an obviously-right answer in isolation, but together they form a coherent module shape with the testability and extensibility ADR-0004 requires.

## Decision

The Per-OS Pipeline Executor takes the following shape:

**1. Step chain, not monolithic.** Each leg is a separate exported method on a `Pipeline` struct. Callers may invoke `RunAll` for the common case or drive legs individually for `--diagnose` smoke mode and for unit tests. A `RunAll` helper sequences the legs in their canonical order and short-circuits on the first error.

**2. Structured event stream as primary output.** The Pipeline accepts a `chan<- Event` channel at construction time. Every leg start, leg success, leg failure, child-process line, test-pass, test-fail, and final report emission appears as a typed Event on the channel. The Local Engine's top-level binary owns a `tty-renderer` that consumes the channel and pretty-prints for humans; downstream consumers (CI integrations, future tooling) consume the channel directly. The Reporter inside the Tester Image continues to emit JSON Lines test events; those are translated into Pipeline Events at the drain leg.

**3. LegError envelope with typed Cause.** All legs return a `*LegError` wrapping a leg name, distinct exit code, diagnostics path, and a `Cause error` that is one of a small set of typed errors (`ImageVersionMismatch`, `MergeConflictError`, `CopyInError`, `ContainerStartError`, `NpmCIError`, `BuildError`, `TestRunError`, `DrainError`, `ParseError`). Callers use `errors.As(err, &legErr)` for uniform leg identification and diagnostics-path access; `errors.As(legErr.Cause, &specificErr)` for rich leg-specific context when needed.

**4. Bench selection lives outside the Executor.** The Executor accepts a pre-selected `Bench` value at construction. A separate `bench.Selector` (driven by the Benches registry config and a selection policy: round-robin, pinned, exclude-list) is the Local Engine's responsibility. The Executor does not read configuration files.

The interface (illustrative, not normative):

```go
package pipeline

type Pipeline struct { /* ... */ }

func New(bench Bench, image ImageID, work WorktreePath, events chan<- Event) *Pipeline

func (p *Pipeline) CheckImageVersion(ctx context.Context) error
func (p *Pipeline) CopyWorktree(ctx context.Context)     error
func (p *Pipeline) StartContainer(ctx context.Context)   error
func (p *Pipeline) NpmCI(ctx context.Context)            error
func (p *Pipeline) Build(ctx context.Context)            error
func (p *Pipeline) RunTests(ctx context.Context)         error
func (p *Pipeline) DrainAndParse(ctx context.Context)    error
func (p *Pipeline) Report() Report

func (p *Pipeline) RunAll(ctx context.Context) (Report, error)
```

## Consequences

+ Each leg is unit-testable in isolation with a mocked subprocess. The cost-of-testing curve for ADR-0004 stays low.
+ Adding a new leg (e.g., a `Lint` step between NpmCI and Build) requires one method, one typed error, one event variant. Existing legs are untouched.
+ Structured event stream gives downstream consumers (CI integrations, future diff tooling, contributor scripts) a stable machine-readable surface. The human renderer becomes one consumer among possibly many.
+ Bench selection is policy; pipeline execution is mechanism. They can evolve independently. The transitional `pick_host` randomization and `--host` pin can be re-expressed as Selector implementations without touching the Pipeline.
+ The `LegError` envelope makes the Local Engine's top-level error handler trivial: one `errors.As` to learn what failed and where the diagnostics live, optional deeper unwrap when the user wants rich context.
- More upfront code than a monolithic Executor: ~8 methods, ~9 typed errors, ~10 event variants, a renderer, a Selector. Pays for itself the first time a leg needs to be tested in isolation or skipped via a flag.
- Two output surfaces (event channel + final report value) for the Executor's callers to handle. The tty-renderer absorbs this complexity for human users; programmatic callers get more flexibility.
- The event channel is a coordination point. Callers must drain it (or close the channel deliberately) to avoid blocking the Pipeline. Standard Go discipline, but a new contributor footgun.

## Alternatives considered

- Monolithic `Run() (Report, error)` with an options struct for skip-flags — Rejected: every per-leg knob becomes an options field. Unit testing one leg requires running all preceding legs. Hurts ADR-0004's testability promise.
- Classic Unix stdout/stderr split (progress to stderr, final report to stdout) — Rejected: loses the unified leg-events + test-events stream that gsd-test-summary-docker.md's diagnostic wish list explicitly asked for. Splits one logical event stream into two text formats.
- Single LegError type with no typed Causes — Rejected: loses leg-specific context (a merge conflict wants `Files []string`; an image mismatch wants `Expected, Actual` strings). Callers fall back to string parsing.
- Typed errors only, no LegError envelope — Rejected: forces every caller to know about all N error types just to learn "which leg failed?" Defeats the uniform diagnostics-path access ADR-0004 wants.
- Bench selection inside the Executor — Rejected: fuses policy and mechanism. Recreates the transitional `pick_host` tangle inside the new code instead of replacing it.
- Event stream as JSON Lines on stdout (no Go channel) — Rejected: the Executor is a library; callers that don't want JSON parsing (the tty-renderer especially) shouldn't have to round-trip through it. The Go channel is the in-process primitive; JSON Lines is what the renderer emits if the top-level binary is invoked with `--json-events`.
