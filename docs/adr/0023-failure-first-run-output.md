# 0023 — Failure-first run output: artifact dir, loud verdict, truncation, grouping, lossless emit

Status: Accepted (2026-06-22)

## Context

Over a long `node --test` run (15–20 min) the harness streamed thousands of
pass/build/npm lines; a `test:fail` scrolled past mid-stream and the rich
per-OS `report.Report` was only *rendered* (it scrolled too), never persisted as
an addressable artifact. An agent reviewing the run greps the captured stream,
the window is too short, and the real failure — stack + captured output — is
missed. Worse, `pipeline.emit` did a non-blocking send into a fixed 128-item
channel and **silently dropped** events on saturation, so a failure event could
vanish before it was ever shown — the opposite of the "fail loud" philosophy
(ADR-0004).

Issue #84 proposed a menu of nine independently-shippable options (A–I) to shift
the default contract from "stream everything" to "stream a compact heartbeat,
persist everything, and make failures loud, addressable, and bounded." All nine
shipped. This ADR pins the resulting contracts so they don't churn.

## Decision

**1. Artifact directory (Options A/D).** Each run writes a deterministic,
failure-first artifact set under the per-run XDG dir, reusing the existing
`runstate.Dir()` resolution (so artifacts live outside the repo):

```
$XDG_STATE_HOME/gsd-test/runs/<run-id>/   (fallback ~/.local/state/...)
  failures.json         # machine: summary + grouped failures, FULL untruncated text
  FAILURES.md           # human/agent: headline first, one CAPPED block per failure
  failures/INDEX.md     # table of contents
  failures/NN-<slug>.md # one bounded, self-contained file per failure
  test-events-<os>.jsonl# full per-test JSONL, persisted per OS (standard path)
  junit.xml             # JUnit XML (both paths)
```

The `<run-id>` is `runspec.NewRunID()` on the standard multi-OS path and the
run's existing `spec.RunID` on the run-and-die path. The dir is a sibling of the
`runs/<run-id>.json` state file. A new `internal/digest` package owns the
serializer and operates on a *slice* of `report.Report` (N for the multi-OS
standard path, 1 for run-and-die), so both execution paths share one contract.

**2. Loud last-line verdict (Option C).** In every mode and outcome
(passed/failed/reaped/infra_error) the **final** stdout line is one compact JSON
object:

```json
{"type":"verdict","outcome":"…","per_os":{"linux":{"passed":..,"failed":..,"total":..,"outcome":".."}},
 "unique_failures":N,"total_failures":M,"top":[{"class":"..","file":"..","line":N,"name":".."}],
 "artifacts":{"dir":"..","failures_json":"..","failures_md":"..","junit_xml":"..","events_jsonl":".."}}
```

The `type:"verdict"` discriminator keeps it distinct from `ModeJSONEvents` lines
(keyed by `kind`) and the per-OS result lines (`type:"result"`). It generalizes
the run-and-die watchdog envelope (ADR-0021) to the standard path. Artifact
writes are best-effort: a write error is logged to stderr and never changes the
exit code or suppresses the verdict — the verdict's `outcome` is the source of
truth.

**3. Truncation with pointers (Option E).** Error/stack/output blobs in
`FAILURES.md` and the per-failure files are capped at **40 lines / 8 KiB**
(whichever binds first), cut on a line boundary, UTF-8-safe, tail-biased for
captured output. `failures.json` holds the **full untruncated** text — it is the
"full at …" target. Each truncated block carries an explicit pointer:
`… (truncated 1,240 lines · full at failures.json#/0/output)`.

**4. Cross-OS grouping (Option G).** Failures are grouped by
`(file, name, error_class, normalized_message)`, where normalization lowercases
and masks volatile tokens (hex addresses, absolute paths, integer runs / line:col)
so the same logical failure collapses across OSes despite run-to-run noise. The
digest reports "N failures, M unique" plus the platforms each hit. Degenerate
(one group per failure) for the single-OS run-and-die path, which is harmless.

**5. Lossless emit — amends ADR-0017 decision 4.** The non-blocking,
drop-on-full `emit` is replaced by an **unbounded in-pipeline queue drained by a
single pump goroutine** that is the sole closer of the events channel. `emit`
never blocks a leg and never drops; `RunAll` **defers** `closeEvents` so the
stream is flushed and closed on every exit path (normal, leg error, or panic);
`RunAll` never waits on the pump, so a slow or absent consumer can never block a
leg or `RunAll` (preserving the property ADR-0017 dec 4 valued). `cmd/gsd-test`
no longer closes the channel — the pump owns it.

**6. Quiet-by-default stream + real-time failures (Options B/I).** The renderer
gains a `Verbosity` (Full/Normal/Quiet). The CLI defaults to **Normal**: a
compact per-OS heartbeat (every 25 passes) + leg events + loud failures;
`EventChildOutput` and per-test `✓` lines are suppressed. `--verbose` /
`GSD_TEST_VERBOSE=1` restores the full firehose; `--quiet` drops even the
heartbeat. `--json-events` is **unchanged** — it always emits the full typed
stream. Failures surface in real time, enriched, the instant they happen:
`✗ FAIL <file>:<line> · <class> · <name> — <msg>` (the live JSONL-tail parser
now carries the one-line error, `error_class`, and a stack-derived line).

**7. JUnit (Option H).** `junit.xml` is generated in Go (`digest.JUnitFromReports`)
for **both** paths — one `<testsuite>` per OS, failures as `<testcase><failure>`,
passing tests counted in the attrs. Generating in Go (rather than node's built-in
reporter) keeps the two paths uniform and sidesteps the run-and-die
`docker --rm` + stdout-reserved-for-the-envelope constraint, where a post-hoc
`docker cp` of a node-written file is impossible. node-native junit (richer — it
lists passing testcases) on the standard path is a deferred follow-up; the Tester
Images run Node 22, which supports the built-in reporter.

## Consequences

- On any failure an agent does **one cheap read** of one small, deterministic,
  failure-only file — or one grep of the last-line verdict — instead of scrolling
  a multi-MB stream. No buried signal, no dropped events, no 3× cross-platform
  duplicate noise.
- The artifact paths and verdict schema are a **supported surface**, pinned here;
  they must not churn without a new ADR.
- The ADR-0013 schema freeze is intact: all new shapes live in `internal/digest`;
  `pipeline.Event` and `runstate.State` gained **additive** fields only.
- The lossless-emit change **supersedes the non-blocking semantics of ADR-0017
  decision 4**. The unbounded queue can grow under a persistently stalled
  consumer; this is bounded backpressure, not data loss, and spill-to-disk is a
  documented future option if memory pressure is ever observed.
- The **default human TTY stream changes** from firehose to compact (Option B's
  intent); `--verbose` / `GSD_TEST_VERBOSE=1` restores prior behavior. The
  machine stream (`--json-events`) and exit codes 0/1/2 are unchanged.
- junit is failures-focused until node-native emission lands.

## Status

Accepted. Relates to ADR-0004 (fail loud), ADR-0013 (frozen per-OS Report
schema), ADR-0017 (event emission — decision 4 amended here), ADR-0021
(run-and-die envelope, schema v2), and ADR-0022 (runstate / run-id).
