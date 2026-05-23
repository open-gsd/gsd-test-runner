// Package report defines the per-OS Report shape (ADR-0013, schema_version=1).
// The Report is produced by the Pipeline after a complete run and consumed by
// the orchestrator's renderer and by the machine-readable JSONL export.
package report

import "time"

// SchemaVersion is the fixed schema version for all Reports produced by this
// package. Consumers must reject Reports with unknown schema versions.
const SchemaVersion = 1

// Kind discriminates a per-OS run outcome.
type Kind string

const (
	// KindPass means all tests passed (Failures is empty).
	KindPass Kind = "pass"
	// KindFail means at least one test failed (Failures is non-empty).
	KindFail Kind = "fail"
)

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

	// DurationMs is the wall-clock duration of the test in milliseconds.
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
	Kind Kind `json:"kind"`

	// OS is the Bench.OS value ("linux", "windows", "macos-container").
	OS string `json:"os"`

	// Bench is the Bench.Name (e.g. "bench-linux-1").
	Bench string `json:"bench"`

	// ImageID is the fully-qualified Tester Image reference used for this run.
	ImageID string `json:"image_id"`

	// ImageVersion is the image-version sentinel value verified during
	// LegCheckImageVersion.
	ImageVersion string `json:"image_version"`

	// StartedAt is the wall-clock time at Pipeline construction (UTC).
	StartedAt time.Time `json:"started_at"`

	// DurationMs is the wall-clock duration from StartedAt to Finalize (ms).
	// Set by Finalize.
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

// New constructs a Report with SchemaVersion=1 and all run-context fields
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
	} else {
		r.Kind = KindFail
	}
}
