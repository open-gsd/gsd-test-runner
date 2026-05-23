# 0015 — Drain/Parse two-leg split and parseJSONL error taxonomy

Status: Accepted (2026-05-23)

## Context

ADR-0013 locks the Report shape. Implementing the legs that produce a Report — reading the JSONL output file from the test container — forces four concrete decisions the architecture ADRs did not pre-commit:

1. **Drain** (`docker cp container:/path → local`) and **Parse** (read file → Report) — one leg or two?
2. **Where does the pure `parseJSONL` function live?** In `internal/report`, in `internal/pipeline`, or in its own package?
3. **What error taxonomy does Parse use?** Typed errors, a single envelope, or untyped `fmt.Errorf`?
4. **How does Parse handle malformed JSON lines?** Fail-fast, skip-and-log, or threshold-based tolerance?

## Decision

**1. Drain and Parse are two separate legs.** Drain copies the JSONL output from the test container to a local temp file via `dockerexec.Run` (`docker cp`). Parse opens that file and constructs a `report.Report`. Separate `LegFailure` events per ADR-0004 let the user diagnose Drain failures (container crashed, path missing) independently from Parse failures (zero events, malformed JSON). A fused `DrainAndParse` leg would collapse this diagnostic resolution into a single undifferentiated failure.

**2. `parseJSONL(r io.Reader, ctx parseContext) (report.Report, error)` is package-private in `internal/pipeline`.** It lives next to the Drain leg, which calls it. Reporter-event-to-Report semantics are pipeline-internal (they change when the Reporter changes); the Report shape is a contract (it doesn't). Mixing them in `internal/report` would couple their evolution. A new `internal/parse` package would be over-decomposed at approximately 50 LOC. The Parse leg method opens the drained file, calls `parseJSONL` with a `parseContext` carrying `{OS, Bench, ImageID, ImageVersion, StartedAt}`, wraps any error in `LegError`, and the pipeline fills `DurationMs` at `RunAll` end.

**3. Three typed errors.** Each represents a distinct triage path reachable via `errors.As` filtering:

- `*ZeroEventsError` — the file was non-empty but contained zero recognized test events. Violates the ADR-0004 fail-loud invariant; if the Reporter ran, it must emit at least one event.
- `*MalformedJSONLError{Line int, Snippet string}` — a line of the JSONL file is not valid JSON.
- `*EventSchemaError{Line int, Field string}` — a line is valid JSON but is missing or has a wrong-typed expected field.

Each error is wrapped in `LegError{Cause: ...}` per ADR-0008.

**4. Fail-fast on malformed JSON.** Any malformed line halts `parseJSONL` with `*MalformedJSONLError`. This is aligned with ADR-0004's fail-loud principle: the Reporter should not emit malformed JSON; if it does, that is a bug to surface loudly, not a condition to silently tolerate. No "skip up to N malformed lines" threshold — partial corruption is corruption.

## Consequences

+ Drain failures surface immediately with their own `LegFailure` event ("Drain: docker cp returned exit 1"), distinguishable from Parse failures ("Parse: zero recognized test events in 4.2 KB JSONL"). The user knows exactly which stage failed without reading the full log.
+ `parseJSONL` is unit-testable with `strings.NewReader(syntheticJSONL)` — covers zero-line input, valid events, mixed valid/invalid JSON, the ADR-0004 zero-events rule, and every error path with no I/O or subprocess.
+ The JSONL → Report transformation is concentrated in one pure function with a single, clear responsibility.
+ The intermediate drained file enables post-mortem debugging: the JSONL stays on disk until cleanup, giving operators a concrete artifact to inspect when Parse fails.
- Extra disk I/O per run (one write, one read) versus streaming `docker cp` output directly into `parseJSONL` via a pipe. Acceptable at current scale; the pipe optimization is available if profiling ever justifies it.
- The intermediate temp-file lifecycle requires explicit cleanup (`defer os.Remove` or equivalent). A future refactor that moves or forks the Drain leg must carry the cleanup responsibility with it.
- A Reporter emitting a partially-corrupted run will halt diagnostics at the first bad line instead of recovering and surfacing the remaining test results.

## Alternatives considered

- **Fused `DrainAndParse` leg** — Rejected: collapses the ADR-0004 diagnostic resolution. A single `LegFailure` cannot distinguish "container vanished before cp" from "container ran but Reporter emitted nothing recognizable."
- **`parseJSONL` in `internal/report`** — Rejected: couples report-shape evolution to reporter-event-format evolution. When the Reporter changes its JSONL schema (e.g., a new event type), that change belongs in pipeline code, not in the package that defines the stable JSON contract.
- **New `internal/parse` package** — Rejected: over-decomposed at ~50 LOC. The package boundary would separate `parseJSONL` from the leg that owns its lifecycle without providing a meaningful reuse seam.
- **Single `*ParseError{Reason, Line}` typed error** — Rejected: loses `errors.As` filtering for distinct triage paths. A caller that wants to handle `ZeroEventsError` differently from `MalformedJSONLError` cannot do so without parsing the `Reason` string.
- **Untyped `fmt.Errorf` returns** — Rejected: inconsistent with the typed-Cause pattern set by ADR-0008. Observability tools and the aggregator both rely on typed causes to classify failures.
- **Skip-and-log malformed lines** — Rejected: masks Reporter bugs. A Reporter emitting 90% valid lines and 10% garbage should fail loudly, not silently discard the garbage and report a partial result as complete.
- **Threshold-based malformed tolerance** (skip up to N bad lines) — Rejected: threshold tuning is configuration debt with no clear right answer. Any chosen threshold is either too tight (fails on transient noise) or too loose (masks real corruption). Fail-fast is always correct under ADR-0004.
