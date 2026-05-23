# 0013 — Per-OS Report shape and JSON schema

Status: Accepted (2026-05-23)

## Context

`Pipeline.Report()` currently returns `struct{}` with an explicit TODO at pipeline.go:222 marking it "deepening candidate #4". Replacing that stub requires locking the Report shape before any downstream consumer (aggregator, renderer, CI integration) can be written. That forces eight concrete decisions the architecture ADRs did not pre-commit:

1. **Consumer set** — who reads a Report?
2. **Inconclusive** (infra failure before tests ran) — separate channel or in Report?
3. **Kind discrimination** — explicit field or implicit from data?
4. **FailedTest fields** — what's in the v1 stable schema?
5. **Schema evolution** — how do consumers know what version they're parsing?
6. **Run context** — which fields beyond pass/fail does Report carry?
7. **Package home** — where do Report and FailedTest live?
8. **Reporter changes scope** — bundled or deferred?

## Decision

**1. Consumer set is published.** Report has three classes of consumer: ADR-0009's aggregator (exit-code logic in `cmd/gsd-test/main.go`), JSON output for machine consumers (CI integration, other GSD tools), and a future observability/log sink. The renderer consumes the same JSON. The schema is a stable contract from first ship: additive changes only; breaking changes require a version bump.

**2. Inconclusive stays in `RunAll`'s error return (`LegError`).** Report is binary (pass | fail). `Pipeline.RunAll(ctx) (Report, error)` already has two channels; `LegError` is the inconclusive channel. This matches ADR-0003's binary phrasing literally; the aggregator maps `LegError` → exit 2.

**3. Explicit `Kind: "pass" | "fail"` field.** The field is self-documenting in JSON; observability filters (`kind == "fail"`) are discoverable without reading schema docs; and it matches ADR-0003's binary phrasing directly. The invariant — `Kind="pass"` with a non-empty `Failures` slice is impossible — lives in a constructor or marshal hook.

**4. Detailed `FailedTest` fields.** Each failed test carries: `File` (repo-relative path), `Name` (fully-qualified, `" > "` separator), `Error` (one-line message), `ErrorClass` (enum), `Output` (captured stdout+stderr), `Stack` (raw `Error.stack`), `DurationMs`, and `RetryCount`. The `ErrorClass` enum has six values — `assertion | timeout | throw | setup | teardown | unknown` — which lets triage distinguish hook failures (setup, teardown) from test-body failures (assertion, throw, timeout) without parsing free-form strings.

**5. Top-level `schema_version: 1`.** An explicit integer version field with additive evolution as the rule. The version bumps only on breaking field changes. Consumers check `schema_version` before reading; unknown versions are rejected loudly.

**6. Rich self-identifying context.** Report carries `os`, `bench`, `image_id`, `image_version`, `started_at`, and `duration_ms`. Each Report is transport-independent: dump `[]Report` as JSON anywhere and consumers interpret without sidecar metadata. The Pipeline already knows all these values at construction (`bench.OS`, `ImageID`, `expectedVersion`).

**7. New `internal/report` package.** Renderer, aggregator, and pipeline all import `internal/report`; the renderer no longer needs to import `internal/pipeline` to consume a Report. This is the rule-of-three seam (three importers). Pipeline depends on report; report depends on nothing internal.

**8. Reporter changes bundled with this slice.** `INSTALL_GSD_TEST.sh:77–86` (the reporter.mjs heredoc) is extended to emit new fields: strip `/work/` prefix for `file`, walk test nesting for fully-qualified `name`, classify into the six `ErrorClass` values, and emit `retry_count: 0` (no retry layer yet). This is a transitional code change that spec-sources the future Tester Image Reporter.

The Go type declaration:

```go
package report

type Kind string
const (
    KindPass Kind = "pass"
    KindFail Kind = "fail"
)

type ErrorClass string
const (
    ErrorClassAssertion ErrorClass = "assertion"
    ErrorClassTimeout   ErrorClass = "timeout"
    ErrorClassThrow     ErrorClass = "throw"
    ErrorClassSetup     ErrorClass = "setup"
    ErrorClassTeardown  ErrorClass = "teardown"
    ErrorClassUnknown   ErrorClass = "unknown"
)

type Report struct {
    SchemaVersion int          `json:"schema_version"`
    Kind          Kind         `json:"kind"`
    OS            string       `json:"os"`
    Bench         string       `json:"bench"`
    ImageID       string       `json:"image_id"`
    ImageVersion  string       `json:"image_version"`
    StartedAt     time.Time    `json:"started_at"`
    DurationMs    int64        `json:"duration_ms"`
    Total         int          `json:"total"`
    Passed        int          `json:"passed"`
    Failures      []FailedTest `json:"failures"`
}

type FailedTest struct {
    File       string     `json:"file"`
    Name       string     `json:"name"`
    Error      string     `json:"error"`
    ErrorClass ErrorClass `json:"error_class"`
    Output     string     `json:"output"`
    Stack      string     `json:"stack"`
    DurationMs int64      `json:"duration_ms"`
    RetryCount int        `json:"retry_count"`
}
```

## Consequences

+ The published JSON contract enables external GSD-tool integration from first ship.
+ Three test surfaces are immediately unlocked: `internal/report` unit tests for the type invariants, `ParseJSONL` pure-function tests against `strings.NewReader`, and renderer/aggregator tests on canned `[]Report` values.
+ The renderer is fully decoupled from `internal/pipeline`; it imports `internal/report` only.
- The `schema_version: 1` freeze obligates additive-only evolution. Any field rename or type change requires a version bump and a migration path for existing consumers.
- The Reporter spec change carries an obligation to the future Tester Image reimplementation: when the Tester Image is rewritten, the new Reporter must produce all eight `FailedTest` fields or the Parse leg will return `*EventSchemaError`.
- The `Stack` field carries raw `node_modules` noise until a v2 filter lands. Consumers must handle multi-kilobyte stack strings from day one.

## Alternatives considered

- **Implicit `Kind` from `len(failures)`** — Rejected: consumers must know the `len == 0 → pass` rule, which is implicit knowledge baked into every reader. Encoding it in a field is zero cost at the schema level and eliminates the implicit rule entirely.
- **Report absorbs an `inconclusive` Kind** — Rejected: weakens ADR-0003's binary phrasing; the existing two-channel `(Report, error)` signature already models this correctly and costs nothing.
- **ADR-0003-minimum `FailedTest`** (name + error string only) — Rejected: `ErrorClass` triage and `DurationMs` tracking pay for themselves on first real failure investigation. The schema freeze makes adding them later expensive; adding them now is cheap.
- **No `schema_version` field** — Rejected: painful to retrofit. Consumers that appear before a version field lands would have to infer version from field presence, which is fragile and undocumented.
- **Minimal Report context** (pass/fail + failures only) — Rejected: observability cannot group results by OS, Bench, or image version without these fields. Every consumer would need a sidecar to reconstruct the context.
- **Report stays in `internal/pipeline`** — Rejected: couples the renderer and aggregator to pipeline-internal types. Any pipeline refactor breaks two unrelated consumers.
- **Report lives in `internal/renderer`** — Rejected: inverts the dependency; the pipeline would gain a renderer import to produce its own output type.
- **Reporter rewrite deferred to Tester Image phase** — Rejected: the Parse leg tests cannot exercise the real JSONL shape without the Reporter producing it. Deferring breaks the "test surface unlocked" consequence above.
- **Free-form `error_class` strings** — Rejected: observability tools cannot reliably filter on strings they don't know in advance. The six-value enum is the stable surface.
