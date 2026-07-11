package pipeline

import (
	"fmt"
	"time"
)

// EventKind discriminates the variants of Event. Per ADR-0008, the
// Event struct uses optional fields rather than a sealed-interface
// hierarchy for ease of JSON serialization (when the renderer adds
// it).
type EventKind int

const (
	EventLegStart EventKind = iota
	EventLegSuccess
	EventLegFailure
	EventLegSkipped  // leg intentionally skipped (not a failure, not a silent success)
	EventChildOutput // a line of subprocess stdout/stderr
	EventTestPass
	EventTestFail
	// EventDroppedOutput is a synthetic marker emitted once when the bounded
	// event queue drops old EventChildOutput events to protect memory under a
	// slow consumer. Detail contains the drop count. Failure events are never
	// dropped. (B-7 fix.)
	EventDroppedOutput
)

func (k EventKind) String() string {
	switch k {
	case EventLegStart:
		return "leg_start"
	case EventLegSuccess:
		return "leg_success"
	case EventLegFailure:
		return "leg_failure"
	case EventLegSkipped:
		return "leg_skipped"
	case EventChildOutput:
		return "child_output"
	case EventTestPass:
		return "test_pass"
	case EventTestFail:
		return "test_fail"
	case EventDroppedOutput:
		return "dropped_output"
	}
	return fmt.Sprintf("event(%d)", int(k))
}

// Event is a single emission on the Pipeline's event channel. Per
// ADR-0008, each event is tagged with its OS (from the Pipeline's
// Bench.OS) so parallel pipelines' events interleave legibly in the
// renderer. Unused fields for a given Kind are left zero-valued.
type Event struct {
	Kind EventKind
	OS   string
	Time time.Time

	// Leg is set for Leg* event kinds.
	Leg Leg

	// Line is set for ChildOutput (the subprocess line),
	// TestPass/TestFail (the test name).
	Line string

	// Stream is set for EventChildOutput: "stdout" or "stderr".
	// Empty for all other event kinds. Per ADR-0017 dec 4.
	Stream string

	// Detail is set for LegFailure (error message), TestFail
	// (one-line error message).
	Detail string

	// File, ErrorClass, and FailLine are set for EventTestFail to power the
	// real-time "✗ FAIL <file>:<line> · <class> · <msg>" line (Option I, #84).
	// All best-effort: FailLine is 0 when it can't be derived. Additive per the
	// ADR-0017 amendment (like Stream).
	File       string
	ErrorClass string
	FailLine   int
}
