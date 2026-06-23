# Failure-first Output

This document explains *why* `gsd-test` runs are quiet by default, what the
**verdict** line and the artifact directory are for, and how the pieces fit
together. For the exact schemas and flags see the
[output reference](failure-first-output-reference.md); for task recipes see the
[output how-to guides](failure-first-output-how-to.md); for the locked design
decisions read [ADR-0023](adr/0023-failure-first-run-output.md).

## The problem: the firehose buries the signal

A real cross-platform `node --test` run is long — fifteen or twenty minutes is
ordinary. Over that window the old default streamed *everything*: every passing
test, every line of `npm ci` and build output, on every OS at once. Three
platforms running in parallel tripled the noise.

The one thing you actually care about — a failure, with its stack and its
captured output — scrolled past mid-stream and was gone. Worse, the rich per-OS
result was only ever *rendered*; it was never written anywhere you could go back
and read. An agent (or a human) reviewing the run was left grepping a multi-megabyte
scrollback for a needle that had already fallen off the top of the terminal.

The failure-first contract (issue #84, [ADR-0023](adr/0023-failure-first-run-output.md))
flips the default. Instead of *stream everything and persist nothing*, `gsd-test`
now **streams a compact heartbeat, makes failures loud the instant they happen,
and persists everything addressable to disk.**

## Quiet by default, loud on failure

The live terminal stream is now **quiet by default** (the renderer calls this
verbosity level `Normal`):

- A compact per-OS **heartbeat** — `[linux]   … 25 passed` — every 25 passing
  tests, so you can see progress without reading every line.
- The **leg events** (`check_image_version`, `npm_ci`, `build`, …) that tell you
  where a run is in the pipeline.
- A **loud failure line** the moment a test fails, enriched with everything you
  need to locate it: `✗ FAIL <file>:<line> · <class> · <name> — <msg>`.

What is *suppressed* by default is the firehose: the child-output stream from
`npm ci` / build, and the per-test `✓` pass lines. Two flags move the dial:

- `--verbose` (or `GSD_TEST_VERBOSE=1`) restores the full firehose — every
  child-output line and every passing test. Reach for it when you are debugging
  the harness itself, not your tests.
- `--quiet` drops even the heartbeat, leaving only leg events and failures.

`--json-events` is unaffected by verbosity — it always emits the full typed
event stream, because a machine consumer wants every event regardless of what a
human would want to read.

The design principle underneath this is the same one that governs the rest of
`gsd-test`: **fail loud** (see [ADR-0004](adr/0004-fail-loud-at-every-pipeline-leg.md)).
Quiet-by-default is not about hiding information — it is about making the *failure*
the loudest thing in the stream instead of the quietest.

## The verdict: one line that tells you everything

Every run, in every outcome, ends with exactly one machine-readable **verdict**
line as the last line of stdout:

```json
{"type":"verdict","outcome":"failed","per_os":{"linux":{"passed":11,"failed":1,"total":12,"outcome":"failed"}},"unique_failures":1,"total_failures":1,"top":[{"class":"assertion","file":"routes.test.js","line":42,"name":"returns 404 for unknown routes"}],"artifacts":{"dir":"…","failures_json":"…","failures_md":"…","junit_xml":"…","events_jsonl":"…"}}
```

The `type:"verdict"` discriminator keeps it distinct from every other kind of
line, so a consumer can grep for it unambiguously. Its `outcome` is the **source
of truth** — it matches the process exit code — and it carries the pointers to
the artifacts on disk. One `grep '"type":"verdict"'` and you know whether the run
passed, how many tests failed, what the top few failures were, and exactly where
to read the rest.

The verdict generalizes the run-and-die watchdog envelope
([ADR-0021](adr/0021-run-and-die-execution-and-two-tier-reaping.md)) to the
standard multi-OS path, so both front doors speak the same language.

## Addressable artifacts: one cheap read, not a long scroll

Every run also writes a small, deterministic, **failure-only** artifact set to a
per-run directory outside your repo, under
`$XDG_STATE_HOME/gsd-test/runs/<run-id>/`:

- `failures.json` — the machine-readable summary plus every grouped failure with
  its **full, untruncated** error, stack, and captured output. This is the
  "full at …" target that everything else points back to.
- `FAILURES.md` — a human/agent view: the headline first, then one **bounded**
  block per unique failure. Long blobs are capped (40 lines / 8 KiB) with an
  explicit pointer back into `failures.json`.
- `failures/INDEX.md` + `failures/NN-<slug>.md` — one self-contained file per
  failure, for when you want to open just one.
- `junit.xml` — JUnit XML for CI dashboards and other tooling.
- `test-events-<os>.jsonl` — the full per-test event stream, one file per OS.

The payoff: on any failure you do **one cheap read** of one small file — or one
grep of the last-line verdict — instead of scrolling a multi-megabyte stream. No
buried signal, no lost result.

Artifact writes are **best-effort**. If a write fails it is logged to stderr and
the corresponding pointer is simply absent from the verdict; it never changes the
exit code and never suppresses the verdict. The verdict's `outcome` remains the
source of truth.

## Why failures collapse across platforms

The same logical failure usually hits more than one OS. Reporting it three times
— once per platform — is just more noise. So failures are **grouped** by their
identity (`file`, test name, error class, and a normalized message that masks
volatile tokens like hex addresses and absolute paths). The same failure on Linux
and Windows collapses into **one** entry that records both platforms, and the
digest headline reads "N failures, M unique." On the single-OS run-and-die path
this grouping is degenerate (one group per failure), which is harmless.

## How it relates to the two front doors

Failure-first output is not specific to one path. The same `internal/digest`
serializer and the same verdict back **both** the standard multi-OS `gsd-test`
run and the [run-and-die](run-and-die.md) `gsd-test run` path, so an agent reads
a reaped run-and-die result exactly the way it reads a failed multi-OS run. The
only differences are mechanical: the standard path writes a `test-events-<os>.jsonl`
per OS, and the run-and-die path uses the run's existing `RunID` for the
directory name.

## Trade-offs and limits

- **The default human stream changed.** If you relied on watching every passing
  test scroll by, that is now behind `--verbose` / `GSD_TEST_VERBOSE=1`. The
  machine stream (`--json-events`) and the exit codes (`0`/`1`/`2`) are unchanged.
- **JUnit is failures-focused for now.** `junit.xml` is generated in Go so both
  paths emit it uniformly; it lists failing testcases and counts the passing ones,
  but does not yet enumerate every passing testcase. Node-native JUnit on the
  standard path is a deferred follow-up (see
  [ADR-0023](adr/0023-failure-first-run-output.md)).
- **Lossless emission has a memory cost in theory.** Events are now queued in an
  unbounded in-pipeline buffer drained by a single pump, so a failure event can
  never be dropped (the old fixed channel silently dropped on saturation). Under a
  persistently stalled consumer the queue can grow — this is bounded backpressure,
  not data loss, and spill-to-disk is a documented future option if memory
  pressure is ever observed.
- **The artifact paths and verdict schema are a supported surface.** They are
  pinned in [ADR-0023](adr/0023-failure-first-run-output.md) and must not change
  without a new ADR — so you can safely build tooling on top of them.
