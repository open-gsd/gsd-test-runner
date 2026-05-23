package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// parseJSONL reads JSONL test events from r and produces aggregate counts
// and per-failure details for inclusion in a report.Report. Caller is
// responsible for populating run-context fields (os/bench/image_id/etc.).
//
// Returns one of *ZeroEventsError, *MalformedJSONLError, *EventSchemaError
// on failure, or (passed, total, failures, nil) on success.
//
// Per ADR-0015 dec 4, malformed JSON is fail-fast — no skip+log.
func parseJSONL(r io.Reader) (passed, total int, failures []report.FailedTest, err error) {
	scanner := bufio.NewScanner(r)
	// 4MB max line buffer to accommodate large stack traces.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	lineNum := 0
	sawAnyTestEvent := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse generic envelope first to check the type field.
		var envelope struct {
			Type string `json:"type"`
		}
		if jsonErr := json.Unmarshal(line, &envelope); jsonErr != nil {
			return 0, 0, nil, &MalformedJSONLError{
				Line:    lineNum,
				Snippet: snippetOf(line, 80),
				Cause:   jsonErr,
			}
		}

		// Only consume test_event records — Reporter may emit other types
		// (pass-through events) that we don't aggregate.
		if envelope.Type != "test_event" {
			continue
		}

		sawAnyTestEvent = true

		var ev struct {
			Type       string  `json:"type"`
			Kind       string  `json:"kind"`        // "pass" | "fail"
			File       string  `json:"file"`
			Name       string  `json:"name"`
			Error      string  `json:"error"`
			ErrorClass string  `json:"error_class"`
			Output     string  `json:"output"`
			Stack      string  `json:"stack"`
			DurationMs float64 `json:"duration_ms"`
			RetryCount int     `json:"retry_count"`
		}
		if jsonErr := json.Unmarshal(line, &ev); jsonErr != nil {
			return 0, 0, nil, &MalformedJSONLError{
				Line:    lineNum,
				Snippet: snippetOf(line, 80),
				Cause:   jsonErr,
			}
		}

		// Schema validation: required fields.
		if ev.Kind == "" {
			return 0, 0, nil, &EventSchemaError{Line: lineNum, Field: "kind"}
		}
		if ev.Name == "" {
			return 0, 0, nil, &EventSchemaError{Line: lineNum, Field: "name"}
		}

		total++
		switch ev.Kind {
		case "pass":
			passed++
		case "fail":
			failures = append(failures, report.FailedTest{
				File:       ev.File,
				Name:       ev.Name,
				Error:      ev.Error,
				ErrorClass: report.ErrorClass(ev.ErrorClass),
				Output:     ev.Output,
				Stack:      ev.Stack,
				DurationMs: ev.DurationMs,
				RetryCount: ev.RetryCount,
			})
		default:
			return 0, 0, nil, &EventSchemaError{
				Line:  lineNum,
				Field: "kind",
				Cause: fmt.Errorf("expected pass|fail, got %q", ev.Kind),
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, nil, &MalformedJSONLError{
			Line:  lineNum,
			Cause: scanErr,
		}
	}

	if !sawAnyTestEvent {
		return 0, 0, nil, &ZeroEventsError{}
	}

	return passed, total, failures, nil
}

// LiveTestEvent is the subset of fields needed for live event emission by the
// JSONL-tail goroutine in RunTests. parseJSONL still builds the full
// report.FailedTest from JSONL; this is the lighter shape the tail goroutine
// consumes.
type LiveTestEvent struct {
	Kind string // "pass" | "fail"
	Name string
	File string
}

// parseLiveTestEvent attempts to parse a single JSONL line as a test_event.
// Returns (event, true) if the line is a recognized test_event with required
// fields; (zero, false) otherwise (silently dropped — non-test_event lines,
// malformed lines, etc.). The tail goroutine uses this; parseJSONL uses a
// stricter version for the final report.
func parseLiveTestEvent(line []byte) (LiveTestEvent, bool) {
	var envelope struct {
		Type string `json:"type"`
		Kind string `json:"kind"`
		Name string `json:"name"`
		File string `json:"file"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return LiveTestEvent{}, false
	}
	if envelope.Type != "test_event" {
		return LiveTestEvent{}, false
	}
	if envelope.Kind == "" || envelope.Name == "" {
		return LiveTestEvent{}, false
	}
	return LiveTestEvent{Kind: envelope.Kind, Name: envelope.Name, File: envelope.File}, true
}

// snippetOf returns up to maxLen bytes of b as a string for error messages.
func snippetOf(b []byte, maxLen int) string {
	if len(b) > maxLen {
		return string(b[:maxLen]) + "…"
	}
	return string(b)
}

// --- Typed errors ---

// ZeroEventsError is returned when the JSONL contained no test_event records.
// Per ADR-0004, an empty file is treated as failure rather than "0 tests passed."
type ZeroEventsError struct{}

func (e *ZeroEventsError) Error() string {
	return "no test events found in JSONL (zero-events rule per ADR-0004)"
}

// MalformedJSONLError is returned on the first unparseable JSON line. Per
// ADR-0015 dec 4, parsing is fail-fast — no skip+log.
type MalformedJSONLError struct {
	Line    int
	Snippet string
	Cause   error
}

func (e *MalformedJSONLError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("malformed JSON at line %d: %v (snippet: %q)",
			e.Line, e.Cause, e.Snippet)
	}
	return fmt.Sprintf("malformed JSON at line %d: %v", e.Line, e.Cause)
}

func (e *MalformedJSONLError) Unwrap() error { return e.Cause }

// EventSchemaError is returned when a JSON line parses but is missing a
// required field or has a malformed enum value.
type EventSchemaError struct {
	Line  int
	Field string
	Cause error // optional, for malformed-enum-value cases
}

func (e *EventSchemaError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("event schema error at line %d, field %q: %v",
			e.Line, e.Field, e.Cause)
	}
	return fmt.Sprintf("event schema error at line %d: missing field %q",
		e.Line, e.Field)
}

func (e *EventSchemaError) Unwrap() error { return e.Cause }

// Ensure the typed errors satisfy the error interface (compile-time check).
var (
	_ error = (*ZeroEventsError)(nil)
	_ error = (*MalformedJSONLError)(nil)
	_ error = (*EventSchemaError)(nil)
)
