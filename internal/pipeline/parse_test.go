package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// --- parseJSONL unit tests ---

// TestParseJSONL_EmptyInput_ZeroEventsError verifies that an empty reader
// returns *ZeroEventsError per ADR-0004 (empty file is failure, not success).
func TestParseJSONL_EmptyInput_ZeroEventsError(t *testing.T) {
	_, _, _, err := parseJSONL(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ze *ZeroEventsError
	if !errors.As(err, &ze) {
		t.Fatalf("expected *ZeroEventsError, got %T: %v", err, err)
	}
}

// TestParseJSONL_OnlyNonTestEventLines_ZeroEventsError verifies that a file
// containing only non-test_event lines still triggers *ZeroEventsError per
// ADR-0004 — no test_event records means no test ran.
func TestParseJSONL_OnlyNonTestEventLines_ZeroEventsError(t *testing.T) {
	input := `{"type":"suite_start","suite":"my-suite"}
{"type":"suite_end","duration_ms":123}
`
	_, _, _, err := parseJSONL(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ze *ZeroEventsError
	if !errors.As(err, &ze) {
		t.Fatalf("expected *ZeroEventsError, got %T: %v", err, err)
	}
}

// TestParseJSONL_SinglePass returns (1, 1, nil, nil) for one passing test.
func TestParseJSONL_SinglePass(t *testing.T) {
	input := `{"type":"test_event","kind":"pass","name":"my test","file":"test/foo.test.js","duration_ms":42}
`
	passed, total, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1, got %d", total)
	}
	if passed != 1 {
		t.Errorf("expected passed=1, got %d", passed)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got %d", len(failures))
	}
}

// TestParseJSONL_SingleFail returns (0, 1, [1 failure], nil) with all
// FailedTest fields populated correctly.
func TestParseJSONL_SingleFail(t *testing.T) {
	input := `{"type":"test_event","kind":"fail","name":"bad test","file":"test/bar.test.js","error":"Expected 1 to equal 2","error_class":"assertion","output":"some output","stack":"Error: ...\n  at Context.<anonymous>","duration_ms":99.5,"retry_count":2}
`
	passed, total, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1, got %d", total)
	}
	if passed != 0 {
		t.Errorf("expected passed=0, got %d", passed)
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	ft := failures[0]
	if ft.Name != "bad test" {
		t.Errorf("Name: expected %q, got %q", "bad test", ft.Name)
	}
	if ft.File != "test/bar.test.js" {
		t.Errorf("File: expected %q, got %q", "test/bar.test.js", ft.File)
	}
	if ft.Error != "Expected 1 to equal 2" {
		t.Errorf("Error: expected %q, got %q", "Expected 1 to equal 2", ft.Error)
	}
	if ft.ErrorClass != report.ErrorClassAssertion {
		t.Errorf("ErrorClass: expected %q, got %q", report.ErrorClassAssertion, ft.ErrorClass)
	}
	if ft.Output != "some output" {
		t.Errorf("Output: expected %q, got %q", "some output", ft.Output)
	}
	if ft.Stack != "Error: ...\n  at Context.<anonymous>" {
		t.Errorf("Stack: expected %q, got %q", "Error: ...\n  at Context.<anonymous>", ft.Stack)
	}
	if ft.DurationMs != 99.5 {
		t.Errorf("DurationMs: expected 99.5, got %v", ft.DurationMs)
	}
	if ft.RetryCount != 2 {
		t.Errorf("RetryCount: expected 2, got %d", ft.RetryCount)
	}
}

// TestParseJSONL_MixedPassFail verifies correct counts with only failures in
// the returned slice.
func TestParseJSONL_MixedPassFail(t *testing.T) {
	input := `{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}
{"type":"test_event","kind":"fail","name":"test B","file":"b.test.js","error":"boom","error_class":"throw"}
{"type":"test_event","kind":"pass","name":"test C","file":"c.test.js"}
{"type":"test_event","kind":"fail","name":"test D","file":"d.test.js","error":"timeout","error_class":"timeout"}
`
	passed, total, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if total != 4 {
		t.Errorf("expected total=4, got %d", total)
	}
	if passed != 2 {
		t.Errorf("expected passed=2, got %d", passed)
	}
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %d", len(failures))
	}
	// Only failing tests appear in the failures slice.
	if failures[0].Name != "test B" {
		t.Errorf("failures[0].Name: expected %q, got %q", "test B", failures[0].Name)
	}
	if failures[1].Name != "test D" {
		t.Errorf("failures[1].Name: expected %q, got %q", "test D", failures[1].Name)
	}
}

// TestParseJSONL_WhitespaceOnlyBlankLinesAreIgnored verifies that blank lines
// (empty after scan) don't cause errors — they're skipped.
func TestParseJSONL_WhitespaceOnlyBlankLinesAreIgnored(t *testing.T) {
	// The scanner trims \n; blank lines appear as empty byte slices.
	input := `{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}

{"type":"test_event","kind":"pass","name":"test B","file":"b.test.js"}
`
	passed, total, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if passed != 2 {
		t.Errorf("expected passed=2, got %d", passed)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got %d", len(failures))
	}
}

// TestParseJSONL_PassThroughEventsSkipped verifies that non-test_event lines
// are skipped without error but also don't count as test events.
func TestParseJSONL_PassThroughEventsSkipped(t *testing.T) {
	input := `{"type":"suite_start","suite":"my-suite"}
{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}
{"type":"suite_end","duration_ms":500}
`
	passed, total, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1, got %d", total)
	}
	if passed != 1 {
		t.Errorf("expected passed=1, got %d", passed)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got %d", len(failures))
	}
}

// TestParseJSONL_MalformedJSONOnLine3 verifies fail-fast: the third line's
// invalid JSON returns *MalformedJSONLError with Line=3.
func TestParseJSONL_MalformedJSONOnLine3(t *testing.T) {
	input := `{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}
{"type":"test_event","kind":"pass","name":"test B","file":"b.test.js"}
not valid json at all {{{
{"type":"test_event","kind":"pass","name":"test C","file":"c.test.js"}
`
	_, _, _, err := parseJSONL(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var me *MalformedJSONLError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MalformedJSONLError, got %T: %v", err, err)
	}
	if me.Line != 3 {
		t.Errorf("expected Line=3, got %d", me.Line)
	}
	if me.Snippet == "" {
		t.Error("expected non-empty Snippet")
	}
	if me.Cause == nil {
		t.Error("expected non-nil Cause")
	}
}

// TestParseJSONL_MissingKindField verifies *EventSchemaError when "kind" is absent.
func TestParseJSONL_MissingKindField(t *testing.T) {
	// Valid JSON but missing the required "kind" field.
	input := `{"type":"test_event","name":"test A","file":"a.test.js"}
`
	_, _, _, err := parseJSONL(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *EventSchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected *EventSchemaError, got %T: %v", err, err)
	}
	if se.Field != "kind" {
		t.Errorf("expected Field=%q, got %q", "kind", se.Field)
	}
	if se.Line != 1 {
		t.Errorf("expected Line=1, got %d", se.Line)
	}
}

// TestParseJSONL_MissingNameField verifies *EventSchemaError when "name" is absent.
func TestParseJSONL_MissingNameField(t *testing.T) {
	input := `{"type":"test_event","kind":"pass","file":"a.test.js"}
`
	_, _, _, err := parseJSONL(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *EventSchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected *EventSchemaError, got %T: %v", err, err)
	}
	if se.Field != "name" {
		t.Errorf("expected Field=%q, got %q", "name", se.Field)
	}
}

// TestParseJSONL_UnknownKindValue verifies *EventSchemaError with Cause for an
// unknown kind value (e.g. "skipped").
func TestParseJSONL_UnknownKindValue(t *testing.T) {
	input := `{"type":"test_event","kind":"skipped","name":"test A","file":"a.test.js"}
`
	_, _, _, err := parseJSONL(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *EventSchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected *EventSchemaError, got %T: %v", err, err)
	}
	if se.Field != "kind" {
		t.Errorf("expected Field=%q, got %q", "kind", se.Field)
	}
	if se.Cause == nil {
		t.Error("expected non-nil Cause for unknown kind value")
	}
	if se.Line != 1 {
		t.Errorf("expected Line=1, got %d", se.Line)
	}
}

// TestParseJSONL_FailedTestFields verifies that all FailedTest fields are
// preserved correctly including ErrorClass enum, Stack, Output, RetryCount,
// DurationMs.
func TestParseJSONL_FailedTestFields(t *testing.T) {
	input := `{"type":"test_event","kind":"fail","name":"nested > deep > test","file":"nested.test.js","error":"Assertion failed","error_class":"assertion","output":"console output here","stack":"AssertionError: Assertion failed\n    at fn (nested.test.js:10)","duration_ms":123.456,"retry_count":3}
`
	_, _, failures, err := parseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	ft := failures[0]
	if ft.ErrorClass != report.ErrorClassAssertion {
		t.Errorf("ErrorClass: expected %q, got %q", report.ErrorClassAssertion, ft.ErrorClass)
	}
	if ft.Stack != "AssertionError: Assertion failed\n    at fn (nested.test.js:10)" {
		t.Errorf("Stack: got %q", ft.Stack)
	}
	if ft.Output != "console output here" {
		t.Errorf("Output: got %q", ft.Output)
	}
	if ft.RetryCount != 3 {
		t.Errorf("RetryCount: expected 3, got %d", ft.RetryCount)
	}
	if ft.DurationMs != 123.456 {
		t.Errorf("DurationMs: expected 123.456, got %v", ft.DurationMs)
	}
}

// TestParseJSONL_VeryLargeLineWithin4MB verifies that a line up to ~1MB is
// handled correctly within the scanner buffer limit.
func TestParseJSONL_VeryLargeLineWithin4MB(t *testing.T) {
	// Build a stack trace with ~1MB content.
	bigStack := strings.Repeat("X", 1*1024*1024)
	line := fmt.Sprintf(
		`{"type":"test_event","kind":"fail","name":"big test","file":"big.test.js","error":"OOM","error_class":"throw","stack":%q}`,
		bigStack,
	)
	_, _, failures, err := parseJSONL(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("expected nil error for ~1MB line, got: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	if failures[0].Stack != bigStack {
		t.Errorf("Stack was not preserved correctly (len=%d, expected len=%d)",
			len(failures[0].Stack), len(bigStack))
	}
}

// TestParseJSONL_ErrorClassPreservation verifies all six ErrorClass values
// are preserved by round-trip through the JSON enum.
func TestParseJSONL_ErrorClassPreservation(t *testing.T) {
	cases := []struct {
		raw      string
		expected report.ErrorClass
	}{
		{"assertion", report.ErrorClassAssertion},
		{"timeout", report.ErrorClassTimeout},
		{"throw", report.ErrorClassThrow},
		{"setup", report.ErrorClassSetup},
		{"teardown", report.ErrorClassTeardown},
		{"unknown", report.ErrorClassUnknown},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.expected), func(t *testing.T) {
			line := fmt.Sprintf(
				`{"type":"test_event","kind":"fail","name":"t","file":"f.js","error":"e","error_class":%q}`,
				tc.raw,
			)
			_, _, failures, err := parseJSONL(strings.NewReader(line + "\n"))
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			if len(failures) != 1 {
				t.Fatalf("expected 1 failure, got %d", len(failures))
			}
			if failures[0].ErrorClass != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, failures[0].ErrorClass)
			}
		})
	}
}
