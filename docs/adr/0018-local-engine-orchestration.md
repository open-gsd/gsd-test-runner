# 0018 — Local Engine orchestration: phase structure, plan + config shapes, EnsurePresent placement

Status: Accepted (2026-05-23)

## Context

`internal/plan/doc.go`, `internal/config/doc.go`, and `cmd/gsd-test/main.go` are all skeletons. The previous ADRs name a Selector (0016), an EnsurePresent (0012), an aggregator (0009), and a per-OS Pipeline (0008), but no ADR resolves how these compose into a single `gsd-test` invocation. The next implementer to touch any of these packages will design the integration under deadline pressure, and the choices ripple across all of them. Six concrete decisions must be resolved before implementation begins:

1. **Where in the run lifecycle does `images.EnsurePresent` get called — pre-phase, per-pipeline, or inside `plan.Build`?**
2. **What is the shape of `plan.Plan` and `plan.Run` — what does `plan.Build` return?**
3. **What configuration file format does `internal/config` use, and where does it live on disk?**
4. **When does `config.Load` probe Bench reachability, and what does it do on probe failure?**
5. **What is the orchestrator's top-level phase structure in `cmd/gsd-test/main.go`?**
6. **How does this reconcile with ADR-0011 dec 3's deferred `versions.json` source-of-truth?**

## Decision

**1. `EnsurePresent` runs as a pre-phase, parallel across all (Bench, ImageID) pairs, before any Pipeline starts.** The orchestrator launches a goroutine pool that calls `images.EnsurePresent` for each pair from `plan.Plan.Runs`. It aggregates failures and fails loud per ADR-0009 (exit 2) if any image is unpullable and has no fallback build path. Pipelines are dispatched only after all images are confirmed present on their respective Benches. This removes per-pipeline image-management latency from the parallel startup and produces clear failure attribution — image errors do not masquerade as leg errors.

**2. `plan.Plan{Runs []Run, Skipped []SkipReason}` where `Run = {Bench, ImageID, OS}`.** Matches ADR-0009's pseudocode. `Run` is fully resolved — one per target OS, with Selector already applied. `Skipped` is a typed list (not `[]string`) carrying `{OS, Reason, Detail}` so the renderer can produce informative output (e.g., `"linux skipped: no_bench_for_os"`). The aggregator's exit-code logic per ADR-0009 maps any non-empty `Skipped` to exit 2 when fail-loud is the default; tunable via a future `--allow-skip` flag.

**3. Configuration is TOML at `~/.config/gsd-test/config.toml`.** Single file with `[defaults]`, `[[benches]]` table-array, and `[versions]` map. Chosen for human-friendliness (comments allowed, minimal punctuation) and single-source-of-truth (one file to edit, no per-OS file fan-out). Closes ADR-0011 dec 3's deferred `versions.json` question by absorbing it into the `[versions]` table inside `config.toml`. Example:

```toml
[defaults]
targets = ["linux", "windows"]

[[benches]]
name = "lab-rig-1"
host = "lab-rig-1.local"
os = "linux"

[[benches]]
name = "lab-rig-2"
host = "lab-rig-2.local"
os = "windows"

[versions]
linux = "v1.4.0"
windows = "v1.4.0"
```

**4. Reachability probing is opt-in via `--probe-benches` flag.** Default: trust the registry. With `--probe-benches`: `config.Load` probes each Bench via a fast `docker version` call over SSH (through `dockerexec.Run` per ADR-0014). Reachable Benches populate `Config.Registry`; unreachable ones populate `Config.Unreachable []UnreachableBench` with the probe error attached. `config.Load` succeeds even when all Benches are unreachable. `plan.Build` sees only the reachable registry; unreachable OSes appear in `Plan.Skipped` with `Reason: "bench_unreachable"`. Always-probing was rejected as too slow for the common edit-loop run; never-probing was rejected because it loses the fail-before-any-pipeline-starts affordance for users who want it.

**5. Orchestrator has 5 sequential phases:**

1. **Load** — `config.Load` reads `~/.config/gsd-test/config.toml`, optionally probes (`--probe-benches`). Returns `*Config`.
2. **Plan** — `plan.Build(cfg, selector, targets)` returns `*Plan{Runs, Skipped}`. Pure, no I/O.
3. **EnsureImages** — parallel goroutine pool calls `images.EnsurePresent` for each `(Run.Bench, Run.ImageID)` pair. Aggregates errors. Fail-loud on any failure.
4. **RunPipelines** — parallel goroutine per `Run` calls `Pipeline.RunAll(ctx)`. Renderer subscribes to multiplexed event channels for live progress.
5. **Aggregate + Render** — collect Reports and LegErrors per OS. Map to exit code: 0 (all results pass), 1 (any result fail), 2 (any LegError or non-empty `Plan.Skipped`). Renderer prints final per-OS summary block.

Each phase has typed inputs and outputs with a clear failure mode. Phases 3 and 4 are the only ones with I/O; phases 1, 2, and 5 are CPU/memory only.

**6. The `Config` and `Plan` shapes:**

```go
package config

type Config struct {
	Registry    []bench.Bench      // reachable Benches (post-probe if --probe-benches)
	Unreachable []UnreachableBench // populated only if --probe-benches
	Targets     []string           // OS list from defaults or CLI flag
	Versions    map[string]string  // OS -> expected image version
	Defaults    Defaults           // CLI flag defaults from config
}

type UnreachableBench struct {
	Bench bench.Bench
	Cause error
}

type Defaults struct {
	Targets []string
	Pin     string   // default --bench value
	Exclude []string // default --exclude values
}

type LoadOptions struct {
	Probe bool // true when --probe-benches is set
}

func Load(path string, opts LoadOptions) (*Config, error)
```

```go
package plan

type Plan struct {
	Runs    []Run
	Skipped []SkipReason
}

type Run struct {
	Bench   bench.Bench
	ImageID images.ImageID
	OS      string
}

type SkipReason struct {
	OS     string
	Reason string // "no_bench_for_os" | "bench_unreachable" | "no_image_version" | ...
	Detail string // free-text for renderer
}

func Build(cfg *config.Config, selector *bench.Selector, targets []string) (*Plan, error)
```

## Consequences

+ The orchestrator's design is captured before any of `config`, `plan`, or `main.go` are written — no implementer designs under deadline pressure.
+ Each phase is independently testable: `config.Load` against fixture TOML files; `plan.Build` against synthetic `Config` and `Selector`; EnsureImages against stubbed `EnsurePresent`; `Pipeline.RunAll` already tested; the aggregator against canned `[]Report` and `[]error` slices.
+ Pre-phase EnsurePresent means parallel pipelines start without per-OS image-pull latency. Cold-cache runs incur the pull cost up front, then test events stream from all Pipelines roughly simultaneously.
+ TOML closes the ADR-0011 dec 3 deferred question. One file, one source of truth.
+ Opt-in probing gives developers a "doctor mode" affordance without slowing the common path.
+ `Plan.Skipped` typed reasons let the renderer produce informative output ("linux skipped: bench_unreachable [lab-rig-1]: ssh: connection refused") instead of "missing 1 OS."
+ `Config.Unreachable` separately surfaces probe failures so the user can fix their setup without grepping logs.
- Pre-phase EnsurePresent on cold caches delays time-to-first-test-event by the longest pull duration. Acceptable: cold-cache runs are rare; warm-cache runs see negligible EnsurePresent overhead.
- TOML adds a parser dependency (`github.com/BurntSushi/toml`). Adds one indirect dependency.
- The 5-phase structure couples phases via explicit data types. When a new phase concept arises (e.g., "validate config schema version"), it slots between existing phases with a clear contract instead of bolting on.
- Probing semantics depend on `dockerexec.Run` (existing) for the probe command itself. Probe failures classify as `*dockerexec.ExecError` wrapped in `UnreachableBench.Cause`.

## Alternatives considered

- **EnsurePresent inside each Pipeline goroutine as a new leg** — Rejected: every pipeline waits on pull/build before starting; loses parallel-startup affordance; failure attribution blurs (LegError vs ImageError). Conflicts with ADR-0012 dec 3 (EnsurePresent stays outside Pipeline).
- **EnsurePresent inside `plan.Build` sequentially** — Rejected: mixes I/O into a function that should be pure (data shape → Plan); slow sequential pulls on cold caches; failures abort planning when they should classify a Run as skippable.
- **`Plan` as flat `[]Run` with no `Skipped` list** — Rejected: caller must diff requested OSes against `Plan.Runs` OSes to derive skipped; loses skip-reason fidelity.
- **`Plan` as `map[string]Run` keyed by OS** — Rejected: Go maps are unordered; aggregator cannot render in deterministic order.
- **Configuration as JSON** — Rejected: no comments, more punctuation noise; loses readability for a file users will hand-edit.
- **Configuration as YAML** — Rejected: adds dependency on `gopkg.in/yaml.v3`; YAML's whitespace-significant syntax is fragile for hand-editing.
- **Per-OS config files at `~/.config/gsd-test/benches/<os>`** — Rejected: multiple files for cross-OS settings (versions, defaults) means no single source of truth; pattern incompatible with a `[versions]`-style mapping in a single file.
- **Always-probe at `config.Load`** — Rejected: adds 1–2 seconds × N Benches to every run; the common case (the developer's own machines, known reachable) should not pay the probe cost.
- **Never-probe (only Pipeline LegError surfaces unreachable Benches)** — Rejected: loses the "doctor mode" affordance; first failure surfaces only after dispatching pipelines, wasting setup work.
- **Probe-failure as hard error (`config.Load` fails entirely)** — Rejected: a single down Bench blocks all runs even when other Benches for the same OS exist; conflicts with the design that `plan.Build` chooses Benches per OS.
- **Silent-filter probe failure (no record of unreachable Benches)** — Rejected: silent failure mode is anti-pattern under ADR-0004; user has no way to learn why their config produced fewer runs than expected.
- **Fused EnsureImages + RunPipelines as one phase per OS** — Rejected: each OS goroutine does EnsurePresent then `Pipeline.RunAll` inline; blurs failure attribution; loses the parallel-startup benefit.
- **Lazy EnsurePresent (Pipeline's first leg calls it)** — Rejected: conflicts with ADR-0012 dec 3.
