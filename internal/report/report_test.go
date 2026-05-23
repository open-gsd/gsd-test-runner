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

// TestNew_SchemaVersion verifies New() always stamps SchemaVersion=1.
func TestNew_SchemaVersion(t *testing.T) {
	r := report.New("linux", "bench-1", "ghcr.io/gsd:v1", "v1", fixedTime)
	if r.SchemaVersion != 1 {
		t.Errorf("expected SchemaVersion=1, got %d", r.SchemaVersion)
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
