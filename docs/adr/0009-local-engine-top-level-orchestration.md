# 0009 — Local Engine top-level orchestration: parallel per-OS, structured exit codes, opt-in skip

Status: Accepted (2026-05-23)

## Context

ADR-0008 fixes the Per-OS Pipeline Executor shape but ends at one OS. The Local Engine is invoked once per developer pre-push gate and typically targets multiple OSes (Linux Bench, Windows Bench, future macOS Container Bench). The top-level orchestration must decide concurrency, output sequencing, exit-code semantics, and behavior when one Bench is unreachable. It must also close the cardinal sin documented in `gsd-test-summary-docker.md`: exit 0 when no tests actually ran.

ADR-0002's copy-in plus ADR-0008's per-Bench Executor mean that running OSes in parallel costs the Dev Workstation almost nothing — each Bench is a separate remote machine, and the local cost is one SSH session and one event-channel consumer per OS. The transitional code's serial-only rule was a workaround for shared-Mirror corruption; that corruption class no longer exists.

## Decision

**Concurrency: parallel per-OS by default.** The Local Engine spawns one Per-OS Pipeline goroutine per target OS, each driving its own Bench. A `--sequential` flag forces one-at-a-time execution for edge cases (single-Bench setups, debugging inter-Bench heisenbugs).

**Exit-code aggregation:**
| Code | Meaning |
|---|---|
| 0 | All targeted OSes ran the full suite AND all tests passed. |
| 1 | All targeted OSes ran the full suite; at least one OS had test failures. |
| 2 | At least one OS had an infrastructure failure (image-version mismatch, Bench unreachable, merge conflict, copy-in failed, npm ci crashed, JSONL parse failed, etc.). The suite did not run as designed. |
| 3 | Local Engine could not start: no targets configured, worktree construction failed, GHCR auth missing without local-build fallback enabled, etc. |

Mixed outcomes (e.g., Linux infra-failed, Windows tests-failed) exit 2. Infrastructure dominates: if one OS could not be verified cleanly, the run is untrustworthy even where tests reported pass/fail. "No events emitted from any leg of any OS" is treated as exit 2, not exit 0 — closes the cardinal sin.

**Bench-unreachable behavior: default fail-loud with opt-in skip.** If any configured Bench for a targeted OS is unreachable at planning time (before any pipeline runs), the Local Engine exits 2 with a structured diagnostic naming the unreachable Bench. A `--allow-skip-os <os>` flag opts into best-effort behavior for the named OS: the OS is reported as `skipped` in the summary, and its absence does not contribute to the infra-failure exit code.

**Output sequencing:**
- Pipeline progress events (leg starts, child-process output, individual test pass/fail) interleave live on the structured event stream, each tagged with its OS.
- Per-OS Reports print as each pipeline finishes, headed by a banner: `=== Linux (bench-linux-1) ===`. The fastest-failing OS surfaces first; the developer reads its failure while slower OSes complete.
- An aggregate summary prints at the end: `Linux: passed 247/247 | Windows: failed 3/247 | macOS: skipped` plus the resolved exit code.

**Worktree handle ownership.** The PR-merged worktree is exposed as a Go value with a `Close() error` method (idiomatic Go, matches `*os.File`, `*sql.DB`). `defer worktree.Close()` cleans up the scratch directory. Eliminates the orphaned-scratch-dir leakage class.

The top-level shape (illustrative, not normative):

```go
func main() {
    cfg := config.Load()

    if len(cfg.Targets) == 0 {
        exit(3, "no targets configured")
    }

    work, err := worktree.Construct(ctx, cfg.BaseRef, cfg.PRRef)
    if err != nil {
        exit(3, fmt.Sprintf("worktree construction failed: %v", err))
    }
    defer work.Close()

    plan, err := plan.Build(cfg.Targets, cfg.Benches, cfg.AllowSkip)
    if err != nil {
        exit(2, fmt.Sprintf("bench planning failed: %v", err))
    }

    events := make(chan pipeline.Event, 1024)
    go renderer.Render(events, os.Stdout)

    results := make(chan PipelineResult, len(plan.Runs))
    for _, run := range plan.Runs {
        go func(r plan.Run) {
            p := pipeline.New(r.Bench, r.Image, work.Path(), events)
            report, err := p.RunAll(ctx)
            results <- PipelineResult{OS: r.OS, Report: report, Err: err}
        }(run)
    }

    code := aggregate(results, len(plan.Runs), plan.Skipped)
    close(events)
    exit(code)
}
```

## Consequences

+ Wall-clock time for the pre-push gate scales with the slowest OS, not the sum of all OSes. For three OSes this is ~3x faster than sequential.
+ The "exit 0 with no events" cardinal sin from `gsd-test-summary-docker.md` becomes structurally impossible — a zero-event run exits 2 by aggregation rule.
+ Exit codes are programmatically distinguishable for CI integration and shell-script wrappers: `0` means ship; `1` means fix tests; `2` means fix infra; `3` means fix your local setup.
+ `--allow-skip-os` lets developers proceed when one of their Benches is down for maintenance, without making partial verification the silent default.
+ Worktree cleanup is automatic via `defer Close()`. The transitional `/tmp` accumulation problem cannot recur.
+ Print-as-they-finish sequencing matches developer expectation: act on the first failure without waiting for the slowest pipeline.
- Live-interleaved progress output requires the renderer to label every event with its OS. A small one-time cost that pays for the parallelism win.
- Parallel pipelines mean N concurrent SSH sessions and N concurrent Docker invocations across N Benches. Each Bench must tolerate one in-flight test run (already true — Benches are typically dedicated per OS).
- Mixed outcomes that include infra failures hide some test results behind exit 2. Documented behavior; the per-OS Reports still print, so the data is not lost, only the exit code is dominated.

## Alternatives considered

- Sequential default with parallel opt-in — Rejected: gives up the ~3x wall-clock win on the common case. Parallelism is the architectural unlock the new design earned by eliminating shared-Mirror corruption (ADR-0002).
- Parallel-only with no `--sequential` flag — Rejected: single-Bench setups and inter-Bench-heisenbug debugging are real edge cases that benefit from sequential mode. The flag costs little.
- Single exit code (0 = all pass, non-zero = anything else) — Rejected: a CI wrapper or shell script cannot distinguish "tests failed, fix them" from "Bench unreachable, fix your setup." Losing that distinction repeats one of the transitional architecture's worst failure modes.
- Treat mixed outcomes as exit 1 (test failures dominate) — Rejected: trusting test results from OSes that ran clean while another OS's infra failed silently is exactly the silent-skip pattern ADR-0004 rejects.
- Default best-effort (skip unreachable Benches automatically) — Rejected: silently degrading verification coverage is the failure mode the project most wants to prevent. Opt-in skip preserves the discipline; default fail-loud preserves the user's trust in a clean run.
- Plain `WorktreePath string` with manual cleanup — Rejected: every caller has to remember to clean up; the orphaned-scratch-dir class recurs. Idiomatic Go has a `Close()`-pattern answer here.
- Wait-for-all output sequencing — Rejected: forces the developer to wait for the slowest OS before reading any failure, even when a fast OS failed first. Defeats the point of parallel execution.
