# 0025 — Capacity-aware pull scheduler for OS×Node fan-out (extends ADR-0016)

Status: Accepted (2026-07-03)

## Context

ADR-0016 designed `bench.Selector` as a per-OS, one-Bench-per-pick policy: `plan.Build` assigns exactly one Bench to each OS-target `Run` at plan time, and Selector round-robins across the registry for that OS. ADR-0016 explicitly reserved future base policies ("load-aware, latency-aware") without designing them.

ADR-0024 adds a Node-major dimension: `plan.Build` must now produce one `Run` per (OS × Node major) cell, not one per OS. With multiple Node majors per OS and potentially multiple Benches per OS each with different hardware capacity, plan-time Bench assignment (ADR-0016's model) forces a choice before any Bench's real-time load is known — a fast 16-core Bench and a slow 4-core Bench for the same OS get equal shares under round-robin, and a Bench sitting idle for other OSes' work can't pick up slack for an overloaded one. Four concrete decisions follow:

1. **Where does bench assignment happen** — plan time (ADR-0016's existing model) or dispatch time?
2. **How does a Bench's capacity get determined** when the config doesn't set it explicitly?
3. **What load-balancing mechanism distributes (OS × Node) cells across candidate Benches** without hand-rolled load tracking?
4. **How does exit-code/report aggregation change** now that a Run has two matrix axes (OS, Node) instead of one?

## Decision

**1. Bench assignment moves to dispatch time; `plan.Build` Runs are Bench-agnostic.** `plan.Build` now emits one `Run` per (OS × Node major) cross-product; `Run` carries `NodeMajor` but no `Bench` field. This EXTENDS ADR-0016's Selector model rather than replacing it — Selector's Pin/Exclude filters and per-OS registry still gate which Benches are eligible for an OS; what changes is *when* a specific Bench is bound to a specific Run. Plan-time assignment (ADR-0016's original mechanism) is preserved for the single-Bench-per-OS case; multi-Bench-per-OS fan-out is where dispatch-time binding takes over.

**2. New `internal/schedule` package: pull-based work-stealing over a shared channel, one channel per OS.** All (OS × Node) Units for a given OS go into one shared channel. Each eligible Bench for that OS spawns `max(1, capacity(bench))` worker goroutines that pull from the shared channel. This structurally caps per-Bench concurrency at its capacity while requiring no explicit load counter: a higher-capacity or faster Bench simply drains more Units from the channel because its workers return for more sooner. Least-loaded-first behavior emerges from the pull model rather than being computed.

**3. Per-Bench `capacity` (config `[[benches]].capacity`): explicit value wins; unset (`0`) probes that Bench's own daemon via `docker info -f '{{.NCPU}}'`, cached once per Bench per run, floored to `1` on any probe error.** The probe runs against the specific Bench's Docker host (respecting `DOCKER_HOST=ssh://` per ADR-0011 dec 2), not the Dev Workstation's own core count. Caching it once per run (not once per pick) avoids paying SSH+docker round-trip latency on every Unit dispatch. Defaulting to NCPU rather than a flat `1` makes a Bench auto-parallelize on capable hardware with zero config; the trade-off — a Bench that's also doing other work becomes oversubscribed — is intentional and documented as the reason to set `capacity` explicitly when a Bench isn't dedicated to `gsd-test`.

**4. The separate EnsureImages phase folds into each scheduler worker.** Because bench assignment is now dynamic, images can no longer be pre-pulled per Bench before dispatch (ADR-0012's EnsureImages ran once per Bench at plan time, when the Bench-to-Run mapping was fixed). Each worker goroutine ensures its own image is present immediately before running its pulled Unit's Pipeline. The local fallback build (ADR-0012) passes `--build-arg NODE_VERSION=<major>` (ADR-0024 dec 1) so a fallback build bakes the correct Node major for whichever cell it was dispatched to handle.

**5. `--node <majors>` CLI flag overrides the Node set for every target OS.** Precedence: `--node` flag (if non-empty) → per-OS `[node]` config entry → `config.DefaultNodeLTS()`. This mirrors the existing `--targets`/`defaults.targets` precedence shape (flags always win, per `docs/configuration.md`'s stated precedence rule) rather than introducing a new precedence pattern.

**6. Exit-code rollup is unchanged in spirit (ADR-0009); aggregation and the `per_os` digest key are now per-cell.** Reports across all (OS, Node) cells are aggregated together; any failing cell fails the whole run, exactly as any failing OS failed the whole run pre-ADR-0025. The digest/verdict key that used to be plain OS (`linux`) is now `report.StreamKey(os, nodeMajor)` (e.g. `linux-node22`, `linux-node24`) wherever more than one Node major ran for that OS; `StreamKey` collapses to the plain OS string for legacy single-Node-major reports so existing single-version digests are unaffected.

Verified end-to-end: a local Bench configured with `capacity = 4` and `[node] linux = ["22", "24"]` ran both cells concurrently, each pinned to its own Node major (v22.23.1 and v24.18.0 observed inside the containers), both passed, run exit code 0.

## Consequences

+ A Bench's real hardware determines its share of work automatically; no manual round-robin tuning needed when Benches have unequal capacity.
+ Idle capacity on one Bench can absorb Units originally "destined" for a slower or busier Bench of the same OS — something plan-time assignment structurally cannot do.
+ `internal/schedule` is unit-testable with synthetic capacity functions and a fake work channel (per `schedule_test.go`'s pattern) — no docker daemon required to test the distribution logic itself.
+ The NCPU-probe-once-per-run caching keeps per-Unit dispatch latency independent of Bench count or SSH round-trip cost.
+ ADR-0016's Selector, Pin, and Exclude semantics are preserved unchanged for single-Bench-per-OS configs — this ADR only activates dynamic binding where more than one Bench competes for the same OS.
- Bench assignment is no longer visible in the `Plan` before a run starts — a `Run`'s eventual Bench is only known once dispatch happens, which makes plan-time dry-run tooling (if any is ever built) less precise about "which Bench will run what."
- A Bench with `capacity` unset and doing unrelated work outside `gsd-test` can be oversubscribed by the NCPU default; this is a documented trade-off, not a bug, but it is a footgun for shared hardware — operators must set `capacity` explicitly on non-dedicated Benches.
- Folding EnsureImages into each worker means image-presence checks now happen per-Unit rather than once per Bench per run; a Bench handling many Units for the same image pays a redundant (cheap, metadata-only) `docker image inspect` per Unit instead of one that fans out. Acceptable given the dynamic-assignment requirement, and inspect cost is small relative to a full test run.

## Alternatives considered

- **Keep Bench assignment at plan time (ADR-0016's original model), just widen it to (OS × Node)** — Rejected: prevents least-loaded distribution entirely, since the Bench-to-Run mapping is fixed before any Bench's actual load or speed is observed; a slow Bench and fast Bench for the same OS get equal static shares.
- **Static round-robin assignment across (OS × Node) cells at dispatch time (no capacity awareness)** — Rejected: ignores real hardware differences between Benches of the same OS; produces worse balancing than a capacity-aware pull model for the same implementation cost.
- **Probe each Bench's NCPU on every pick (not cached)** — Rejected: adds an SSH+docker round trip to every Unit dispatch; the Bench's core count does not change mid-run, so caching once per Bench per run is strictly better with no correctness cost.
- **Push-based scheduling (a central dispatcher assigns Units to Benches based on tracked load counters)** — Rejected: requires explicit mutable load-tracking state and synchronization the pull-channel model gets for free; more code for an equivalent (or worse, due to staleness) outcome.
- **Flat default capacity of `1` when unset (matching the pre-ADR-0025 comment in config)** — Rejected: leaves capable multi-core Benches running one container at a time by default; NCPU-default gives auto-parallelism out of the box while still allowing an explicit `1` for operators who want to bound it.
- **Fold Node major into the report's existing OS key instead of a new `StreamKey` helper** — Rejected: would silently break every existing single-Node-major digest/verdict consumer parsing a plain OS string; `StreamKey`'s collapse-to-OS-when-single-major behavior keeps backward compatibility explicit and centralized in one function.
