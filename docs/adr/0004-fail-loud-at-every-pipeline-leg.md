# 0004 — Every pipeline leg fails loud with structured diagnostics

Status: Accepted (2026-05-22)

## Context

The transitional pipeline silently absorbed failures at multiple legs. gsd-test-summary-docker.md documents the worst case: "neither platform produced test events" with no indication of which leg failed. A 13.7 MB populated JSONL file existed on disk, but the aggregator reported zero events. Piping the gate through `tee` masked the real exit code 2 with tee's exit code 0. Diagnostic error messages emitted generic paths (`/tmp/gsd-test-local.err`) when actual files were PID-suffixed — the path in the message pointed nowhere.

Image-version drift (§4.2) produced subtle test failures that looked like test bugs rather than infrastructure mismatches. Nothing in the pipeline identified which leg was responsible or where to look for more information.

Silent failure at any pipeline leg means the developer cannot distinguish "tests ran and failed" from "infra broke before tests ran." Both look the same: no green output.

## Decision

Every leg of the Local Engine pipeline emits a structured failure indicating the specific leg and a path to its diagnostics. Legs include: image-version sentinel check, base-branch fetch, PR merge, copy-in, container start, npm ci, build, test run, JSONL drain, JSONL parse, report emission. On any failure: exit non-zero with a distinct code per leg, print the specific leg name to stderr, and print the path to the leg's captured logs. A non-empty JSONL file that yields zero parsed events is a parse failure (not a "no tests ran" success).

## Consequences

+ The "neither platform produced events" class of silent failure becomes impossible.
+ Each failure has a single named responsible leg — debuggable without speculation.
+ Image-version sentinel mismatch produces a specific failure ("Tester Image vX expected, vY installed; pull or rebuild") rather than a downstream test-failure cascade.
- Every leg must have an associated diagnostics path. Adds discipline (and a small amount of code) per leg.
- Exit-code-per-leg requires a documented table of codes the wrapper scripts must respect.

## Alternatives considered

- Single exit code with a textual reason — Rejected: harder for orchestrators to programmatically distinguish "infra failed" from "tests failed."
- Best-effort logging without per-leg structured failures — Rejected: this is the transitional behavior. It is the problem.
