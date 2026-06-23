# Failure-first Output Reference

Field-by-field reference for the failure-first output contract: verbosity levels,
the verdict line, and the run artifact directory. For tasks see the
[output how-to guides](failure-first-output-how-to.md); for the concepts see
[Failure-first Output](failure-first-output.md); for the locked decisions see
[ADR-0023](adr/0023-failure-first-run-output.md).

This contract is shared by the standard multi-OS `gsd-test` path and the
[run-and-die](run-and-die-reference.md#result-artifacts-and-verdict-adr-0023)
`gsd-test run` path. The same `internal/digest` serializer backs both; per-path
differences are noted inline.

## Verbosity

The live terminal stream has three verbosity levels. The default is **Normal**.

| Level | Selected by | Shows | Suppresses |
|-------|-------------|-------|------------|
| **Normal** | (default) | Per-OS heartbeat, leg events, loud failures | Child output (`npm ci` / build), per-test pass lines |
| **Full** | `--verbose` or `GSD_TEST_VERBOSE=1` | Everything: child output + every passing test + failures + leg events | — |
| **Quiet** | `--quiet` | Leg events and failures only | The heartbeat as well |

`--json-events` is independent of verbosity — it always emits the full typed
event stream regardless of these flags. The verdict line (below) is still printed
as the last line of stdout in `--json-events` mode.

If both `--verbose` and `--quiet` are passed, `--verbose` wins.

### Heartbeat line

In Normal verbosity a heartbeat is printed once every **25** passing tests, per OS:

```text
[linux]   … 25 passed
```

It is suppressed entirely in Quiet verbosity, and replaced by individual `✓`
per-test lines in Full verbosity.

### Failure line

A failing test is surfaced in real time, the instant it happens, in Normal and
Quiet verbosity (and in Full, alongside the pass lines):

```text
[linux]   ✗ FAIL routes.test.js:42 · assertion · returns 404 for unknown routes — AssertionError: 404 !== 200
```

The format is `✗ FAIL <file>:<line> · <class> · <name> — <msg>`, prefixed with
the `[<os>]` tag. It degrades gracefully when evidence fields are absent: the
`<file>:<line>`, `· <class>`, and `— <msg>` segments are each omitted when the
underlying data is missing. The fully-qualified test name is always present.

## Verdict line

The **final** line of stdout, in every verbosity, mode, and outcome, is one
compact JSON object:

```json
{"type":"verdict","outcome":"failed","per_os":{"linux":{"passed":11,"failed":1,"total":12,"outcome":"failed"}},"unique_failures":1,"total_failures":1,"top":[{"class":"assertion","file":"routes.test.js","line":42,"name":"returns 404 for unknown routes"}],"artifacts":{"dir":"/home/you/.local/state/gsd-test/runs/abc123","failures_json":"…/failures.json","failures_md":"…/FAILURES.md","junit_xml":"…/junit.xml","events_jsonl":"…/test-events-linux.jsonl"}}
```

### Top-level fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Always `"verdict"`. The discriminator that distinguishes this line from `type:"result"` (per-OS result lines) and `kind`-keyed `--json-events` lines. |
| `outcome` | string | Worst-of across all OSes: `passed`, `failed`, `reaped`, or `infra_error`. Matches the exit code and is the **source of truth**. |
| `per_os` | object | Map of OS name → per-OS counts (below). |
| `unique_failures` | integer | Number of distinct failures after cross-OS grouping. |
| `total_failures` | integer | Total failure count before grouping (sums duplicates across OSes). |
| `top` | array | Up to **5** of the most relevant failures (below). Always an array; `[]` on a passing run. |
| `artifacts` | object | Pointers to the files on disk (below). |

### `per_os` entry

| Field | Type | Description |
|-------|------|-------------|
| `passed` | integer | Passing test count for this OS. |
| `failed` | integer | Failing test count for this OS. |
| `total` | integer | Total test count for this OS. |
| `outcome` | string | This OS's outcome: `passed`, `failed`, `reaped`, or `infra_error`. |

### `top` entry

| Field | Type | Description |
|-------|------|-------------|
| `class` | string | The error class (e.g. `assertion`, `timeout`). |
| `file` | string | Test file. |
| `line` | integer | Line number derived from the stack. |
| `name` | string | Fully-qualified test name. |

### `artifacts` object

Each field is the absolute path to the corresponding file, and is **omitted**
when that file was not written (a best-effort write failure, or a file that does
not apply to this path).

| Field | Description |
|-------|-------------|
| `dir` | The per-run artifact directory. |
| `failures_json` | Path to `failures.json`. |
| `failures_md` | Path to `FAILURES.md`. |
| `junit_xml` | Path to `junit.xml`. |
| `events_jsonl` | Path to `test-events-<os>.jsonl`. Standard path only. |

### Outcomes

| `outcome` | Meaning | Exit code |
|-----------|---------|-----------|
| `passed` | All tests passed on all OSes. | `0` |
| `failed` | At least one test failed. | `1` |
| `reaped` | A run exceeded its deadline and was killed (run-and-die). | `1` |
| `infra_error` | A leg failed, a Bench was skipped, or the run could not start. | `2` |

A pre-pipeline failure (bad config, no Bench, planning error) still prints a
minimal verdict with `outcome: "infra_error"`, an empty `per_os`, and no
artifacts — the verdict is never skipped.

## Artifact directory

Each run writes its artifacts to a per-run directory, a sibling of the run's
`<run-id>.json` state file:

```
$XDG_STATE_HOME/gsd-test/runs/<run-id>/      (fallback ~/.local/state/gsd-test/runs/<run-id>/)
```

`<run-id>` is assigned per run; read it from the `artifacts.dir` field of the
verdict line. The directory lives **outside your repository**, so artifacts never
pollute your working tree or git status.

| File | Written | Contents |
|------|---------|----------|
| `failures.json` | Always | Machine-readable summary + grouped failures with **full, untruncated** error / stack / output. |
| `FAILURES.md` | Always | Headline first, then one **bounded** block per unique failure, each capped with a pointer into `failures.json`. |
| `junit.xml` | Always (both paths) | JUnit XML: one `<testsuite>` per OS, failures as `<testcase><failure>`, passing tests counted in the attributes. |
| `failures/INDEX.md` | When there are failures | Table of contents for the per-failure files. |
| `failures/NN-<slug>.md` | When there are failures | One self-contained, bounded file per unique failure (`NN` is a zero-padded index; `<slug>` derives from file + test name). |
| `test-events-<os>.jsonl` | Standard path, per OS, when non-empty | The full per-test event stream as captured from the reporter. |

Writes are best-effort: a failure is logged to stderr (`warning: …`) and never
changes the exit code or suppresses the verdict.

### Truncation

Error, stack, and captured-output blobs in `FAILURES.md` and the per-failure
files are capped at **40 lines or 8 KiB**, whichever binds first. Cuts land on a
line boundary and are UTF-8-safe; captured output is tail-biased (the end is kept).
`failures.json` always holds the **full, untruncated** text.

Each truncated block carries an explicit pointer to the full text, an RFC-6901
JSON Pointer into `failures.json`:

```text
… (truncated 1,240 lines · full at failures.json#/failures/0/stack)
```

`FAILURES.md` lists at most **100** failure blocks; beyond that a footer reads
`… N more failures omitted; see failures.json`.

### Cross-OS grouping

Failures are grouped by `(file, name, error_class, normalized_message)`, where
normalization lowercases the message and masks volatile tokens (hex addresses,
absolute paths, integer / line:column runs) so the same logical failure collapses
across OSes despite run-to-run noise. Each group records the platforms it hit.
The digest reports `N failures, M unique`. On the single-OS run-and-die path the
grouping is degenerate (one group per failure).

## Exit codes

The exit code is determined by the run outcome, independently of whether any
artifact write succeeded:

| Code | Meaning |
|------|---------|
| `0` | All OSes passed. |
| `1` | At least one OS had failing tests (or a run-and-die run was reaped). |
| `2` | Infrastructure problem — a leg failed, a Bench was skipped, or the run could not start. |

## Related references

- [Configuration Reference](configuration.md) — the `--verbose`, `--quiet`, and
  `--json-events` flags in the full CLI flag table.
- [Run-and-die Reference](run-and-die-reference.md#result-artifacts-and-verdict-adr-0023)
  — the same contract as seen from the `gsd-test run` path, plus the result
  envelope and `kill` record.
