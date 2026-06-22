# 0017 — Event emission contract for the Per-OS Pipeline

Status: Accepted (2026-05-23). Decision 4 (non-blocking, drop-on-full emit) amended by ADR-0023 — emit is now lossless via an unbounded queue + pump goroutine.

## Context

`internal/pipeline/pipeline.go` defines 7 `EventKind` values but only ever emits 3 of them: `EventLegStart`, `EventLegSuccess`, and `EventLegFailure` are emitted by `runLeg`; `EventChildOutput`, `EventTestPass`, `EventTestFail`, and `EventReport` are defined and have `String()` cases but zero call sites pass them to `Pipeline.emit`. The renderer (per ADR-0009) will consume the Event channel; without a locked emission contract, each subprocess leg implementer will make ad-hoc choices about what to emit, when, and from where. Four concrete decisions must be resolved before the first subprocess leg lands:

1. **Which legs emit `EventChildOutput`, and how is subprocess output captured?**
2. **Where do test events (`EventTestPass`/`EventTestFail`) come from — live during RunTests, or post-hoc during Parse?**
3. **Is `EventReport` emitted by the Pipeline or pulled from `Pipeline.Report()` after RunAll?**
4. **Does `Event` need additional fields to support the locked emission shape?**

## Decision

**1. Subprocess legs (NpmCI, Build, RunTests) emit `EventChildOutput` line-by-line via a stdout/stderr tee goroutine.** Each subprocess leg starts the child via the docker-exec wrapper with stdout/stderr piped to a goroutine that reads via `bufio.Scanner` and emits one `EventChildOutput` per line. Live progress reaches the renderer immediately; verbose diagnostic output is preserved for log capture. Subprocess legs are the only emitters of this event kind — non-subprocess legs (CheckImageVersion, CopyWorktree, Drain, Parse) do not emit it.

**2. Test events (`EventTestPass`, `EventTestFail`) stream live during `RunTests` via a JSONL-tail goroutine, not post-hoc during `Parse`.** While the test container runs, a separate goroutine on the Dev Workstation tails the JSONL file (via `docker exec ... tail -f` or equivalent polling) and emits per-test events as the Reporter writes them. Parse — running after Drain copies the final JSONL — still populates `Report.Failures` for the aggregator and JSON export but emits no test events. Two consumers of the same JSONL at two times: live event stream for the developer's terminal, batch parse for the JSON contract.

**3. `EventReport` is removed from the `EventKind` enum.** The Report value is already returned from `Pipeline.RunAll(ctx) (Report, error)` and accessible via `Pipeline.Report()`. The aggregator in `cmd/gsd-test/main.go` reads it from the per-pipeline result channel; the renderer reads it through the aggregator's render pass. Emitting Report as a redundant Event is two surfaces for one piece of data — pure cost.

**4. `Event` struct gains a `Stream string` field** (values: `"stdout"` | `"stderr"` | `""`) to distinguish source stream for `EventChildOutput`. Other event kinds leave `Stream` empty. No breaking change for existing emitters; additive only.

The updated Event types:

```go
package pipeline

// EventKind enumerates Pipeline event categories. EventReport was removed
// per ADR-0017 dec 3 — Report values are returned from RunAll and pulled
// via Pipeline.Report(), not emitted on the event channel.
type EventKind int

const (
	EventLegStart    EventKind = iota
	EventLegSuccess
	EventLegFailure
	EventChildOutput // emitted by NpmCI/Build/RunTests; per-line tee
	EventTestPass    // emitted by RunTests JSONL-tail goroutine
	EventTestFail    // emitted by RunTests JSONL-tail goroutine
)

type Event struct {
	Kind   EventKind
	OS     string
	Time   time.Time
	Leg    Leg    // empty for EventChildOutput; confirm in implementation
	Line   string // EventChildOutput: one captured line; test events: test name
	Stream string // EventChildOutput: "stdout" | "stderr"; otherwise ""
	Detail string // existing field, leg-specific metadata
}
```

## Consequences

+ Renderer can be written once against a stable emission contract instead of being rewritten as each subprocess leg lands.
+ Live test progress visible during the slow RunTests leg, which often takes the most wall-clock time.
+ Live subprocess output (`npm ci` progress, build output) visible during NpmCI/Build/RunTests — the long-running legs where silent waits are most painful.
+ The same JSONL serves both consumers without double-work: live tail for human progress; batch parse for machine consumption. Parsing is fast (file scan, ~100ms even at 10k events).
+ `parseJSONLLine(line []byte) (TestEvent, error)` becomes a natural shared helper between the JSONL-tail goroutine and the Parse leg's `parseJSONL` — both consumers feed off one source-of-truth line parser.
- RunTests grows complexity: owns a JSONL-tail goroutine with lifecycle, cleanup on cancel, and sync with leg completion. New failure modes: tail goroutine outlives container, container exits before tail starts, tail emits events after Parse has already populated Report.
- Subprocess legs grow complexity: tee goroutine for stdout and tee goroutine for stderr per running subprocess, with lifecycle management alongside `cmd.Wait()`.
- Event consumers must handle `EventChildOutput` volume (one event per line; a typical `npm ci` emits hundreds). Buffered channel or non-blocking emit becomes more important.
- Removing `EventReport` from the enum is a breaking change for anyone who was switch-casing on it (no one is today; safe).

## Alternatives considered

- **`EventChildOutput` emitted as one buffered event per leg (batch)** — Rejected: loses live progress affordance; subprocess legs with multi-second runtime show no signs of life until completion.
- **`EventChildOutput` removed entirely; subprocess output only in `LegError` on failure** — Rejected: successful legs leave the developer staring at a spinner; ADR-0004's fail-loud spirit extends to "make-progress-visible," not just "make-failures-visible."
- **Test events emitted post-hoc during Parse (batch)** — Rejected: renderer cannot show live test-by-test progress during RunTests, which is the slowest leg and the one developers most want progress for. Defeats the user-facing reason to have separate test events at all.
- **Test events removed entirely; renderer reads only `Report.Failures`** — Rejected: same rationale as post-hoc batch; gives the renderer nothing to display while RunTests runs.
- **`EventReport` emitted as the last event before channel close** — Rejected: two surfaces for one piece of data (Report return value AND EventReport); event-channel consumers and return-value consumers must reconcile.
- **`Event` struct unchanged (no `Stream` field)** — Rejected: renderer cannot visually distinguish stdout/stderr in `EventChildOutput`; loses a high-signal display affordance.
