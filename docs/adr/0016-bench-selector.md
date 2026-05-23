# 0016 — bench.Selector: policy/mechanism split for Bench selection

Status: Accepted (2026-05-23)

## Context

ADR-0008 dec 4 promised a `bench.Selector` driven by "the Benches registry config and a selection policy: round-robin, pinned, exclude-list" but did not design it. The Selector blocks the future `internal/plan` and `cmd/gsd-test/main.go` implementations; without a designed seam the next implementer will inline selection in `main.go` or design under deadline pressure. Six concrete decisions were left open:

1. **What type carries Selector behavior?** (function value, struct with method, Go interface)
2. **How do the three named policies (round-robin, pinned, exclude-list) compose?**
3. **Where does reachability validation live — inside Selector or upstream?**
4. **What does Pick return when no Bench in the registry matches the requested OS?**
5. **What's the round-robin cursor's scope and persistence?**
6. **How are misconfiguration cases (Pin in Exclude, Pin not in registry) reported?**

## Decision

**1. `type Selector struct{...}` with `Pick(os string) (Bench, error)` method.** Matches the codebase's plain-struct pattern (Pipeline, Worktree). No Go interface — consistent with ADR-0011 dec 4's preference for function-variable-or-struct over interfaces when only one production implementation exists. `NewSelector` constructs from a registry plus Options; Selector owns per-OS round-robin cursors under a mutex for concurrent Pipeline construction (one goroutine per OS).

**2. Composable filters plus single base policy.** Pin and Exclude are FILTERS applied to the registry at `NewSelector` time. Round-robin is the only base policy. `NewSelector(registry, Options{Pin: "foo", Exclude: []string{"bar"}})` filters the registry then round-robins across what survives. Rejected: separate policy types (each combination becomes a new type — pinned-with-exclude is not a clean composition); strategy-interface chaining (over-engineered for three known cases). Future policies (load-aware, latency-aware) add to the base-policy enum without disturbing filters.

**3. Reachability validation is upstream of Selector.** `internal/config`'s registry loader optionally probes Benches at load time and excludes unreachable ones. Selector receives a pre-validated `[]Bench` and trusts it. Selector stays pure (deterministic, no I/O, unit-testable without subprocess). Bench-down-mid-run failures surface as Pipeline `LegError` (the docker call against that Bench fails through `dockerexec.Run` per ADR-0014), not as a Selector concern.

**4. Pick returns `*NoBenchForOSError{OS}` when zero post-filter Benches match.** Typed error so the aggregator can `errors.As` it to produce exit code 2 (infra/inconclusive per ADR-0009) and a precise message: `"No Bench available for OS=linux"`. Distinct from `*PinnedBenchNotInRegistryError` which surfaces at `NewSelector` time (decision 6).

**5. Round-robin cursor is per-OS, no cross-invocation persistence.** Linux Benches round-robin independently of Windows Benches. Each `gsd-test` invocation starts all cursors at zero — Selector is constructed fresh per run. Cross-run balancing (sticky preferences, last-used tracking) is not Selector's job; if a user wants it, they pin explicitly.

**6. Misconfiguration surfaces at `NewSelector`, not `Pick`.** Two typed errors: `*PinnedBenchNotInRegistryError{Pin string, Available []string}` (Pin was set but no bench with that name exists pre-exclude), `*PinExcludeConflictError{Pin string}` (Pin is also in Exclude). Constructor returns one of these before any `Pick` is possible. `Pick` stays simple: only `*NoBenchForOSError` from the runtime side.

The Selector signature:

```go
package bench

type Selector struct {
    registry []Bench         // post-filter
    cursor   map[string]int  // per-OS round-robin cursor
    mu       sync.Mutex
}

type Options struct {
    Pin     string   // "" disables pin; non-empty filters registry to one named bench
    Exclude []string // bench names removed from registry
}

func NewSelector(registry []Bench, opts Options) (*Selector, error)
// Errors: *PinnedBenchNotInRegistryError, *PinExcludeConflictError.

func (s *Selector) Pick(os string) (Bench, error)
// Errors: *NoBenchForOSError.

type NoBenchForOSError struct{ OS string }
type PinnedBenchNotInRegistryError struct{ Pin string; Available []string }
type PinExcludeConflictError struct{ Pin string }
```

## Consequences

+ Bench selection is policy; Pipeline execution is mechanism — ADR-0008 dec 4's promise is satisfied with a concrete shape.
+ Selector is unit-testable with synthetic `[]Bench` slices. Every policy and filter combination, plus every error path, is covered without docker, SSH, or filesystem.
+ The transitional `pick_host` randomization and `--host` pin can be re-expressed as `Options{Pin: ...}` on a registry-then-RoundRobin Selector without touching pipeline code.
+ The Benches registry shape becomes the Selector's input invariant. `internal/config`'s author has a target to build to before that package lands.
+ Selector → `pipeline.New` plumbing is exactly one line per OS in `internal/plan`/`cmd/gsd-test/main.go`. The thinness of the plumbing is proof the seam is in the right place.
+ Future policies (load-aware, latency-aware, region-aware) extend the base-policy enum without disrupting Pin/Exclude filter semantics.
- Implementation is deferred until `internal/plan` and `cmd/gsd-test/main.go` materialize. Shipping Selector with no caller would violate the "two adapters = real seam" rule (only one hypothetical caller exists). ADR captures the design so the next implementer plumbs through rather than designing under pressure.
- Selector trusts registry validity. A misconfigured registry loader that yields unreachable Benches will surface as Pipeline `LegError` on the first docker call instead of at `Pick` time. Acceptable: reachability is a load-time concern (decision 3) and per-pick probing was rejected for the latency it adds.
- The per-OS round-robin cursor not persisting across invocations means workload-balancing depends on user-supplied Pin/Exclude policy across runs, not Selector internals. Acceptable: cross-run state is a separate domain.

## Alternatives considered

- **Function-value Selector (`func(os) (Bench, error)`) returned from `NewSelector`** — Rejected: stateful closure obscures the mutex requirement and round-robin cursor; harder to test in isolation; harder to read at the call site.
- **Go interface (`Selector interface { Pick(os) (Bench, error) }`)** — Rejected: contradicts ADR-0011 dec 4's function-variable-no-interface pattern when only one production implementation exists. Adds mock-pattern ceremony with no leverage.
- **Separate policy types (`RoundRobinPolicy`, `PinnedPolicy`, `ExcludeListPolicy` as siblings)** — Rejected: every combination becomes a new type. Pin-with-exclude is not a clean policy; it is a filter pair over the base policy.
- **Strategy interface with chained middleware (`Exclude("x")(Pin("y")(RoundRobin()))`)** — Rejected: over-engineered for three known cases; reader friction for the small payoff.
- **Selector probes reachability via `--probe` flag** — Rejected: makes Selector impure (does SSH); per-pick latency; mid-run Bench failures surface elsewhere anyway (Pipeline `LegError`).
- **Registry-pre-filter AND per-pick probe (belt-and-suspenders)** — Rejected: highest complexity, smallest marginal benefit over upstream-only validation.
- **Single `SelectError` type with `Reason` field** — Rejected: loses `errors.As` filtering for distinct triage paths (configuration error vs runtime error).
- **Cursor persists across invocations (state file or env var)** — Rejected: cross-run state is a separate domain; complicates testing; users who want sticky behavior can pin explicitly.
- **Cursor is global, not per-OS** — Rejected: round-robin across a mixed-OS registry produces wrong-OS Benches in the rotation; per-OS is the only correct shape.
- **Pick returns `(Bench, bool)` instead of a typed error** — Rejected: loses error-type information the aggregator needs for ADR-0009 exit-code logic.
