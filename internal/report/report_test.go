package report_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// fixedTime is a stable instant used across tests.
var fixedTime = time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)

// TestNew_SchemaVersion verifies New() always stamps SchemaVersion=2 (bumped
// by ADR-0021 Decision 6).
func TestNew_SchemaVersion(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	if r.SchemaVersion != 2 {
		t.Errorf("expected SchemaVersion=2, got %d", r.SchemaVersion)
	}
}

// TestNew_ContextFields verifies all run-context fields are populated.
func TestNew_ContextFields(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1.2.3", fixedTime)

	if r.OS != "linux" {
		t.Errorf("OS: want %q, got %q", "linux", r.OS)
	}
	if r.Bench != "bench-1" {
		t.Errorf("Bench: want %q, got %q", "bench-1", r.Bench)
	}
	if r.ImageID != "ghcr.io/gsd:v1" {
		t.Errorf("ImageID: want %q, got %q", "ghcr.io/gsd:v1", r.ImageID)
	}
	if r.ImageVersion != "v1.2.3" {
		t.Errorf("ImageVersion: want %q, got %q", "v1.2.3", r.ImageVersion)
	}
	if !r.StartedAt.Equal(fixedTime) {
		t.Errorf("StartedAt: want %v, got %v", fixedTime, r.StartedAt)
	}
}

// TestNew_FailuresNotNil verifies New() initializes Failures to a non-nil
// empty slice so JSON serialization produces [] not null.
func TestNew_FailuresNotNil(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	if r.Failures == nil {
		t.Error("expected Failures to be non-nil empty slice, got nil")
	}
	if len(r.Failures) != 0 {
		t.Errorf("expected Failures length 0, got %d", len(r.Failures))
	}
}

// TestFinalize_EmptyFailures_KindPass verifies Finalize sets Kind=KindPass
// when Failures is empty.
func TestFinalize_EmptyFailures_KindPass(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	later := fixedTime.Add(500 * time.Millisecond)
	r.Finalize(later)

	if r.Kind != report.KindPass {
		t.Errorf("expected Kind=%q, got %q", report.KindPass, r.Kind)
	}
}

// TestFinalize_NonEmptyFailures_KindFail verifies Finalize sets Kind=KindFail
// when Failures is non-empty.
func TestFinalize_NonEmptyFailures_KindFail(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	r.Failures = []report.FailedTest{
		{Name: "some test", Error: "assert failed", ErrorClass: report.ErrorClassAssertion},
	}
	later := fixedTime.Add(200 * time.Millisecond)
	r.Finalize(later)

	if r.Kind != report.KindFail {
		t.Errorf("expected Kind=%q, got %q", report.KindFail, r.Kind)
	}
}

// TestFinalize_DurationMs verifies Finalize computes DurationMs from StartedAt.
func TestFinalize_DurationMs(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	later := fixedTime.Add(1234 * time.Millisecond)
	r.Finalize(later)

	if r.DurationMs != 1234 {
		t.Errorf("expected DurationMs=1234, got %f", r.DurationMs)
	}
}

// TestJSONRoundTrip verifies that a Report with failures marshals and
// unmarshals back to an equal value.
func TestJSONRoundTrip(t *testing.T) {
	r := report.New("windows", "bench-win-1", "ghcr.io/gsd-win:v2", "v2.0.0", fixedTime)
	r.Total = 5
	r.Passed = 3
	r.Failed = 2
	r.Failures = []report.FailedTest{
		{
			File:       "tests/foo.test.cjs",
			Name:       "suite > nested > fails",
			DurationMs: 42.5,
			RetryCount: 1,
			Error:      "Expected true to equal false",
			ErrorClass: report.ErrorClassAssertion,
			Stack:      "AssertionError: Expected true to equal false\n    at tests/foo.test.cjs:10:5",
			Output:     "some captured output",
		},
	}
	r.Finalize(fixedTime.Add(3 * time.Second))

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got report.Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Compare key fields explicitly (time.Time requires Equal, not ==).
	if got.SchemaVersion != r.SchemaVersion {
		t.Errorf("SchemaVersion: want %d, got %d", r.SchemaVersion, got.SchemaVersion)
	}
	if got.Kind != r.Kind {
		t.Errorf("Kind: want %q, got %q", r.Kind, got.Kind)
	}
	if got.OS != r.OS {
		t.Errorf("OS: want %q, got %q", r.OS, got.OS)
	}
	if got.Bench != r.Bench {
		t.Errorf("Bench: want %q, got %q", r.Bench, got.Bench)
	}
	if got.ImageID != r.ImageID {
		t.Errorf("ImageID: want %q, got %q", r.ImageID, got.ImageID)
	}
	if got.ImageVersion != r.ImageVersion {
		t.Errorf("ImageVersion: want %q, got %q", r.ImageVersion, got.ImageVersion)
	}
	if !got.StartedAt.Equal(r.StartedAt) {
		t.Errorf("StartedAt: want %v, got %v", r.StartedAt, got.StartedAt)
	}
	if got.DurationMs != r.DurationMs {
		t.Errorf("DurationMs: want %f, got %f", r.DurationMs, got.DurationMs)
	}
	if got.Total != r.Total {
		t.Errorf("Total: want %d, got %d", r.Total, got.Total)
	}
	if got.Passed != r.Passed {
		t.Errorf("Passed: want %d, got %d", r.Passed, got.Passed)
	}
	if got.Failed != r.Failed {
		t.Errorf("Failed: want %d, got %d", r.Failed, got.Failed)
	}
	if !reflect.DeepEqual(got.Failures, r.Failures) {
		t.Errorf("Failures: want %+v, got %+v", r.Failures, got.Failures)
	}
}

// TestJSONSchemaKeys verifies the JSON representation uses snake_case field
// names as specified in ADR-0013.
func TestJSONSchemaKeys(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	r.Finalize(fixedTime.Add(time.Second))

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	requiredKeys := []string{
		"schema_version",
		"kind",
		"os",
		"bench",
		"image_id",
		"image_version",
		"started_at",
		"duration_ms",
		"total",
		"passed",
		"failed",
		"failures",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON key %q missing from Report output", key)
		}
	}
}

// TestJSONFailedTestKeys verifies the JSON representation of FailedTest uses
// snake_case field names as specified in ADR-0013.
func TestJSONFailedTestKeys(t *testing.T) {
	ft := report.FailedTest{
		File:       "tests/foo.test.cjs",
		Name:       "suite > leaf",
		DurationMs: 10,
		RetryCount: 0,
		Error:      "oops",
		ErrorClass: report.ErrorClassThrow,
		Stack:      "Error: oops\n    at ...",
		Output:     "",
	}

	b, err := json.Marshal(ft)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	requiredKeys := []string{
		"file",
		"name",
		"duration_ms",
		"retry_count",
		"error",
		"error_class",
		"stack",
		"output",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON key %q missing from FailedTest output", key)
		}
	}
}

// TestErrorClassValues verifies the six ErrorClass constants have the exact
// string values documented in ADR-0013.
func TestErrorClassValues(t *testing.T) {
	cases := []struct {
		ec   report.ErrorClass
		want string
	}{
		{report.ErrorClassAssertion, "assertion"},
		{report.ErrorClassTimeout, "timeout"},
		{report.ErrorClassThrow, "throw"},
		{report.ErrorClassSetup, "setup"},
		{report.ErrorClassTeardown, "teardown"},
		{report.ErrorClassUnknown, "unknown"},
	}
	for _, tc := range cases {
		if string(tc.ec) != tc.want {
			t.Errorf("ErrorClass %q: want string %q, got %q", tc.ec, tc.want, string(tc.ec))
		}
	}
}

// TestKindValues verifies the two Kind constants have the expected string values.
func TestKindValues(t *testing.T) {
	if string(report.KindPass) != "pass" {
		t.Errorf("KindPass: want %q, got %q", "pass", string(report.KindPass))
	}
	if string(report.KindFail) != "fail" {
		t.Errorf("KindFail: want %q, got %q", "fail", string(report.KindFail))
	}
}

// TestMarkReaped_SetsReapedOutcome verifies a reaped run is a loud, distinct
// outcome with the kill record attached (ADR-0021 Decision 6).
func TestMarkReaped_SetsReapedOutcome(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v2", "v2.0.0", fixedTime)
	r.MarkReaped(fixedTime.Add(181*time.Second), report.KillRecord{
		Reason:              report.KillReasonEstimateOverrun,
		EffectiveDeadlineMs: 180000,
		ElapsedMs:           181004,
		LastActiveTest:      &report.ActiveTest{File: "test/db.test.js", Name: "reconnects after drop"},
		ReapedBy:            report.ReapedByInContainer,
		SignalChain:         []string{"SIGTERM@180000", "SIGKILL@180010"},
	})

	if r.Outcome != report.OutcomeReaped {
		t.Errorf("Outcome: want %q, got %q", report.OutcomeReaped, r.Outcome)
	}
	if r.Kind != report.KindFail {
		t.Errorf("Kind: want %q (reaped is not a pass), got %q", report.KindFail, r.Kind)
	}
	if r.Kill == nil {
		t.Fatal("Kill: want non-nil kill record")
	}
	if r.Kill.LastActiveTest == nil || r.Kill.LastActiveTest.File != "test/db.test.js" {
		t.Errorf("Kill.LastActiveTest: want test/db.test.js, got %+v", r.Kill.LastActiveTest)
	}
	if r.Kill.Reason != report.KillReasonEstimateOverrun {
		t.Errorf("Kill.Reason: want estimate_overrun, got %q", r.Kill.Reason)
	}
}

func TestFinalize_SetsOutcome(t *testing.T) {
	pass := report.New("linux", "b", "img", "v", fixedTime)
	pass.Finalize(fixedTime.Add(time.Second))
	if pass.Outcome != report.OutcomePassed {
		t.Errorf("passed run Outcome: want %q, got %q", report.OutcomePassed, pass.Outcome)
	}

	fail := report.New("linux", "b", "img", "v", fixedTime)
	fail.Failures = []report.FailedTest{{File: "x.test.js", Name: "boom"}}
	fail.Finalize(fixedTime.Add(time.Second))
	if fail.Outcome != report.OutcomeFailed {
		t.Errorf("failed run Outcome: want %q, got %q", report.OutcomeFailed, fail.Outcome)
	}
}

func TestJSON_KillPresentOnlyWhenReaped(t *testing.T) {
	// Non-reaped: kill omitted, outcome present.
	clean := report.New("linux", "b", "img", "v", fixedTime)
	clean.Finalize(fixedTime.Add(time.Second))
	cb, _ := json.Marshal(clean)
	var cm map[string]any
	json.Unmarshal(cb, &cm)
	if _, ok := cm["kill"]; ok {
		t.Error("non-reaped report should omit the kill key")
	}
	if cm["outcome"] != "passed" {
		t.Errorf("outcome: want passed, got %v", cm["outcome"])
	}

	// Reaped: kill present with reason + reaped_by.
	reaped := report.New("linux", "b", "img", "v", fixedTime)
	reaped.MarkReaped(fixedTime.Add(time.Second), report.KillRecord{
		Reason:   report.KillReasonHardCap,
		ReapedBy: report.ReapedByExternal,
	})
	rb, _ := json.Marshal(reaped)
	var rm map[string]any
	json.Unmarshal(rb, &rm)
	if rm["outcome"] != "reaped" {
		t.Errorf("outcome: want reaped, got %v", rm["outcome"])
	}
	kill, ok := rm["kill"].(map[string]any)
	if !ok {
		t.Fatalf("reaped report should carry a kill object, got %v", rm["kill"])
	}
	if kill["reason"] != "hard_cap" || kill["reaped_by"] != "external" {
		t.Errorf("kill: want reason=hard_cap reaped_by=external, got %v", kill)
	}
}

func TestOutcomeValues(t *testing.T) {
	cases := map[report.Outcome]string{
		report.OutcomePassed:     "passed",
		report.OutcomeFailed:     "failed",
		report.OutcomeReaped:     "reaped",
		report.OutcomeInfraError: "infra_error",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("Outcome value: got %q, want %q", string(got), want)
		}
	}
}
