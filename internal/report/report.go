// Package report defines the per-OS Report shape (ADR-0013, schema_version=2).
// The Report is produced by the Pipeline after a complete run and consumed by
// the orchestrator's renderer and by the machine-readable JSONL export.
// Schema_version 2 adds the reaped Outcome and Kill block (ADR-0021).
package report

import "time"

// SchemaVersion is the fixed schema version for all Reports produced by this
// package. Consumers must reject Reports with unknown schema versions. Bumped
// to 2 by ADR-0021 Decision 6, which adds the reaped Outcome and Kill block.
const SchemaVersion = 2

// Kind discriminates a per-OS run outcome.
type Kind string

const (
	// KindPass means all tests passed (Failures is empty).
	KindPass Kind = "pass"
	// KindFail means at least one test failed (Failures is non-empty).
	KindFail Kind = "fail"
)

// Outcome is the richer run result added in schema_version 2 (ADR-0021
// Decision 6). Unlike Kind (pass/fail), it distinguishes a reaped run — a
// runaway suite the watchdog or reaper killed — from an ordinary failure, and
// reserves infra_error for pipeline-leg failures. Always set by Finalize or
// MarkReaped, so it is never empty on a finalized Report.
type Outcome string

const (
	OutcomePassed     Outcome = "passed"
	OutcomeFailed     Outcome = "failed"
	OutcomeReaped     Outcome = "reaped"
	OutcomeInfraError Outcome = "infra_error"
)

// TestStat is per-test telemetry for the result envelope (ADR-0021 §F). Status
// is "passed", "failed", or "killed"; ExitedClean is false for a test that was
// still in flight when the run was reaped.
type TestStat struct {
	File        string  `json:"file"`
	Name        string  `json:"name"`
	DurationMs  float64 `json:"duration_ms"`
	Status      string  `json:"status"`
	ExitedClean bool    `json:"exited_clean"`
}

// HandleSamples is the periodic in-flight open-handle timeline for one test
// file (ADR-0021 §A). Populated only when the spec requests periodic sampling.
type HandleSamples struct {
	File    string         `json:"file"`
	Samples []HandleSample `json:"samples"`
}

// HandleSample is one periodic snapshot: the elapsed time into the run, the
// total open handle count, the resource types open beyond the load-time
// baseline, and — when telemetry.captureStacks is set — creation stacks grouped
// by async resource type.
type HandleSample struct {
	AtMs   int64               `json:"at_ms"`
	Open   int                 `json:"open"`
	Leaked []string            `json:"leaked,omitempty"`
	Stacks map[string][]string `json:"stacks,omitempty"`
}

// KillReason explains why a run was reaped (ADR-0021 Decision 1/2).
type KillReason string

const (
	// KillReasonEstimateOverrun: elapsed exceeded estimate*overrunFactor.
	KillReasonEstimateOverrun KillReason = "estimate_overrun"
	// KillReasonHardCap: elapsed exceeded the absolute 1h ceiling.
	KillReasonHardCap KillReason = "hard_cap"
	// KillReasonExternalReaper: the external reaper killed the container,
	// typically because the in-container watchdog itself wedged.
	KillReasonExternalReaper KillReason = "external_reaper"
)

// ReapedBy records which tier performed the kill (ADR-0021 Decision 2/4).
type ReapedBy string

const (
	// ReapedByInContainer: Tier 1, the in-container watchdog (precise).
	ReapedByInContainer ReapedBy = "in_container"
	// ReapedByExternal: Tier 2, the Local Engine reaping the container.
	ReapedByExternal ReapedBy = "external"
)

// ActiveTest names the test that was running when the kill fired.
type ActiveTest struct {
	File string `json:"file"`
	Name string `json:"name"`
}

// InFlightTest is a test still executing at kill time (process isolation can
// have several in flight at once).
type InFlightTest struct {
	File         string `json:"file"`
	Name         string `json:"name"`
	StartedMsAgo int64  `json:"started_ms_ago"`
}

// KillRecord is the "where it died" evidence attached to a reaped Report
// (ADR-0021 §C/D). Present only when Outcome == OutcomeReaped.
type KillRecord struct {
	Reason              KillReason     `json:"reason"`
	EffectiveDeadlineMs int64          `json:"effective_deadline_ms"`
	ElapsedMs           int64          `json:"elapsed_ms"`
	LastActiveTest      *ActiveTest    `json:"last_active_test,omitempty"`
	InFlightTests       []InFlightTest `json:"in_flight_tests,omitempty"`
	ReapedBy            ReapedBy       `json:"reaped_by"`
	SignalChain         []string       `json:"signal_chain,omitempty"`
	// Granularity is "process" when the run used isolation=none, signaling that
	// LastActiveTest/InFlightTests are best-effort, not per-test precise
	// (ADR-0021 Decision 5). Empty under the default process isolation.
	Granularity string `json:"granularity,omitempty"`
}

// ErrorClass classifies the nature of a test failure for machine consumers.
// The six values below are exhaustive per ADR-0013 decision body.
type ErrorClass string

const (
	// ErrorClassAssertion is a deliberate assertion failure (assert.*,
	// expect.*,  or any error whose name is AssertionError or whose code is
	// ERR_ASSERTION).
	ErrorClassAssertion ErrorClass = "assertion"

	// ErrorClassTimeout is a test that exceeded its time budget (the Node
	// test runner surfaces these as errors whose message contains "timed out").
	ErrorClassTimeout ErrorClass = "timeout"

	// ErrorClassThrow is an unhandled throw inside a test body (not a hook).
	ErrorClassThrow ErrorClass = "throw"

	// ErrorClassSetup is an error thrown inside a before/beforeEach hook.
	ErrorClassSetup ErrorClass = "setup"

	// ErrorClassTeardown is an error thrown inside an after/afterEach hook.
	ErrorClassTeardown ErrorClass = "teardown"

	// ErrorClassUnknown is the fallback for anything that does not match the
	// above classifications.
	ErrorClassUnknown ErrorClass = "unknown"
)

// FailedTest records a single test-level failure extracted from the JSONL
// stream emitted by the reporter (ADR-0013 decision 8, slice 3 parse step).
type FailedTest struct {
	// File is the repo-relative path to the test file (/work/ prefix stripped).
	File string `json:"file"`

	// Name is the fully-qualified test name: parent > child > leaf, joined by
	// " > " (space-chevron-space).
	Name string `json:"name"`

	// DurationMs is the container-internal per-test duration as reported
	// by the Node test runner. Reported in milliseconds with fractional
	// precision. NOT comparable to Report.DurationMs (which is pipeline
	// wall-clock); the two measure different things.
	DurationMs float64 `json:"duration_ms"`

	// RetryCount is the number of retries before this result was recorded.
	RetryCount int `json:"retry_count"`

	// Error is the one-line error message.
	Error string `json:"error"`

	// ErrorClass classifies the failure.
	ErrorClass ErrorClass `json:"error_class"`

	// Stack is the raw Error.stack string from the reporter.
	Stack string `json:"stack"`

	// Output is the captured stdout+stderr for the test, if available.
	Output string `json:"output"`
}

// Report is the per-OS final result of a Pipeline run (ADR-0013, schema_version=1).
// It is stamped at Pipeline construction and finalized after the Parse leg.
type Report struct {
	// SchemaVersion is always 1 for this package; consumers reject unknown values.
	SchemaVersion int `json:"schema_version"`

	// Kind is "pass" when Failures is empty, "fail" otherwise. Set by Finalize.
	// Retained for backward compatibility; Outcome is the richer discriminator.
	Kind Kind `json:"kind"`

	// Outcome is the schema_version-2 result: passed | failed | reaped |
	// infra_error (ADR-0021 Decision 6). Set by Finalize or MarkReaped.
	Outcome Outcome `json:"outcome"`

	// Kill is the kill record, present only when Outcome == OutcomeReaped.
	Kill *KillRecord `json:"kill,omitempty"`

	// PerTest is per-test telemetry derived from the reporter events the
	// watchdog observed (ADR-0021 §F). Empty for non-run-and-die reports.
	PerTest []TestStat `json:"per_test,omitempty"`

	// HandleSamples is periodic in-flight open-handle telemetry, one entry per
	// test file, captured during the run when the spec requests it
	// (telemetry.sampleHandlesMs, ADR-0021 §A). Unlike the exit-time leak
	// signal it survives a reaped run, so a hung test still leaves a trail of
	// how its handles accumulated. Empty unless sampling was enabled.
	HandleSamples []HandleSamples `json:"handle_samples,omitempty"`

	// OS is the Bench.OS value ("linux", "windows", "macos-container").
	OS string `json:"os"`

	// Bench is the Bench.Name (e.g. "bench-linux-1").
	Bench string `json:"bench"`

	// ImageID is the fully-qualified Tester Image reference used for this run.
	ImageID string `json:"image_id"`

	// ImageVersion is the image-version sentinel value verified during
	// LegCheckImageVersion.
	ImageVersion string `json:"image_version"`

	// StartedAt is the moment Pipeline.New was called. This is NOT first-
	// leg-start time; if a Pipeline is constructed before being dispatched
	// (no current production path does this, but architecturally possible),
	// StartedAt includes any wait time. Frozen as part of the schema per
	// ADR-0013; future schema changes are additive only.
	StartedAt time.Time `json:"started_at"`

	// DurationMs is the total Pipeline wall-clock duration from Pipeline.New
	// to Pipeline.RunAll completing — includes all 8 legs, Drain disk I/O,
	// and Parse file-scan time. Distinct from sum(FailedTest.DurationMs)
	// which is container-internal per-test runner time and is NOT expected
	// to equal Report.DurationMs. Set by Finalize.
	DurationMs float64 `json:"duration_ms"`

	// Total is the total number of tests found in the JSONL stream.
	Total int `json:"total"`

	// Passed is the count of passing tests.
	Passed int `json:"passed"`

	// Failed is the count of failing tests (len(Failures)).
	Failed int `json:"failed"`

	// Failures holds one entry per failing test. Populated by the Parse leg.
	Failures []FailedTest `json:"failures"`
}

// New constructs a Report with SchemaVersion=2 and all run-context fields
// populated. Kind is not set until Finalize is called. Caller may subsequently
// fill Total/Passed/Failed/Failures (the Parse leg does this in slice 3).
func New(os, bench, imageID, imageVersion string, startedAt time.Time) Report {
	return Report{
		SchemaVersion: SchemaVersion,
		OS:            os,
		Bench:         bench,
		ImageID:       imageID,
		ImageVersion:  imageVersion,
		StartedAt:     startedAt,
		Failures:      []FailedTest{},
	}
}

// Finalize sets DurationMs (wall-clock from StartedAt to now) and computes
// Kind from Failures: empty → KindPass, non-empty → KindFail. Pipeline calls
// this after the Parse leg fills counts.
func (r *Report) Finalize(now time.Time) {
	r.DurationMs = float64(now.Sub(r.StartedAt).Milliseconds())
	if len(r.Failures) == 0 {
		r.Kind = KindPass
		r.Outcome = OutcomePassed
	} else {
		r.Kind = KindFail
		r.Outcome = OutcomeFailed
	}
}

// MarkReaped finalizes the Report as a reaped run (ADR-0021 Decision 6): it
// sets DurationMs, Outcome=OutcomeReaped, and attaches the kill record. Kind is
// set to KindFail so legacy consumers keyed on Kind still treat the run as
// not-passing; the precise reaped-vs-failed distinction lives in Outcome. A
// reap is a loud, structured result, never a silent hang.
func (r *Report) MarkReaped(now time.Time, kill KillRecord) {
	r.DurationMs = float64(now.Sub(r.StartedAt).Milliseconds())
	r.Kind = KindFail
	r.Outcome = OutcomeReaped
	r.Kill = &kill
}
