# ADR-0028 — Runner policy tested via extraction, not dependency injection

**Date**: 2026-07-10
**Status**: Accepted (2026-07-10)
**Context**: Architecture review — `runner.Run`, the ~190-line multi-OS orchestrator, has zero direct unit tests; `runner_test.go` tests only the `commaSplit` helper.

## Context

A routine architecture review found that `runner.Run` mixes **policy** (the CLI-overrides-config resolution at lines 68-86, and the result-classification + exit-code aggregation at lines 203-246) with **effects** (driving docker over SSH, git, the filesystem, the renderer, the scheduler). The policy is the bug-prone, contract-bearing part — it encodes the ADR-0009 exit codes (0/1/2) and the ADR-0023 failure-first verdict — yet it is untested, because it is inlined in `Run` interleaved with the effects. The effects are already covered by the integration tests.

The pure helpers (`commaSplit`) are extracted and tested; the *policy that calls them* (the "CLI flag → config default → error" precedence) is not. This is a locality inversion: the test-affordance gradient pulls purity outward, leaving the decision logic bare.

The natural reflex is to make `Run` unit-testable by dependency injection — accept fakes for `config.Load`, `plan.Build`, `pipeline.New`, `schedule.Run`, etc. This ADR rejects that reflex and records why.

## Decision

**1. Test `Run`'s policy by extracting it, not by injecting collaborators into `Run`.**

   - **`Aggregate(results, skipped) (finalReports, exitCode)`** owns result classification (leg-error → `infra_error` marking, fail detection) and the ADR-0009/0023 exit-code mapping (`legError || skipped → 2`, `fail → 1`, else `0`). Pure. Tested through its interface without docker. `Run` calls it, then feeds `finalReports` to the renderer and `emitRunArtifacts`.
   - **`ResolveEffective(cfg, opts) (targets, pin, exclude, error)`** owns the "CLI flag → config default → error" precedence. Pure. Tested through its interface. Kills the inversion at its source (`commaSplit` tested, the fallback policy that calls it not).

   Both live in `internal/runner` — they are `Run`'s extracted policy.

**2. Do not add injection points to `Run` for testability.** `Run` keeps hardcoding its collaborators (`config.Load`, `bench.NewSelector`, `plan.Build`, `refs.Resolve`, `worktree.Construct`, `images.EnsurePresent`, `pipeline.New`, `schedule.Run`, `renderer.New`). It remains integration-tested. `Run`'s value is the *policy*, and the policy is now testable via the extracted modules; simulating docker to test wiring is not worth the interface widening.

**3. `workerPIDAlive` becomes a package-level `var`** (matching its twin `defaultSpawn`, which is already injectable). This completes an existing convention asymmetricmetry, not a new one: the liveness guard in `waitRun` — the single most bug-prone piece per its accumulated "Fix 1/3/N" comments — was previously tested via real PIDs plus ~50ms timing windows because the function was a direct function, not an injectable seam.

## Consequences

+ The verdict/exit-code contract (ADR-0009/0023) and the config-precedence rule are unit-tested at their true seam — the policy interface — not inferred from cmd-level tests that assert "the last stdout line is a verdict."
+ `async_test.go` can drop its timing tricks and test the liveness guard by injecting a fake-dead PID.
+ `Run`'s interface does not widen. The `Options` struct stays a 14-field bag; reshaping it is out of scope here (it matters less once policy is extracted *out* of `Run`).
- This does **not** make `Run` itself unit-testable end-to-end. If a future requirement is "run `Run` in CI without docker," this ADR does not deliver it — that would require the DI approach explicitly rejected here.
- The `*os.File` signatures on the cmd handlers are untouched. They remain a separate, deferred test-affordance fix (mechanical, does not serve the policy goal).
- Two pure functions leave `Run`'s body. A reader tracing `Run` now hops to `Aggregate`/`ResolveEffective` for the policy. This is the intended trade: locality of *policy* improves (it concentrates in the tested modules) at the cost of one extra hop when reading `Run` top-to-bottom.

## Alternatives considered

- **Inject collaborators into `Run` for end-to-end unit tests (full DI).** Rejected: `Run`'s value is its policy, not its docker driving (that is `pipeline`'s job). DI widens `Options` with injection points for marginal gain — the shallow-module failure mode (large interface, the test surface no longer matches the policy). Testing policy does not require simulating docker.
- **Extract only the 3-line exit-code mapping, leave classification in `Run`.** Rejected: the exit code is a function of the classification (you cannot decide "inconclusive" without first marking `infra_error`). Splitting them recreates a wiring gap where the real bugs (did I mark this leg-error as infra_error?) live in the un-extracted half. Extracting only the mapping *moves* complexity; extracting classify+decide *concentrates* the verdict policy.
- **Leave `resolveEffective` inline.** Rejected: it is the canonical example of the inversion this ADR exists to fix (a tested pure helper, `commaSplit`, whose calling policy is untested). Leaving it perpetuates the gradient.
