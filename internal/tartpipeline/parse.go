package tartpipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// parseJSONL reads JSONL test events from r and produces aggregate counts
// and per-failure details for inclusion in a report.Report.
//
// This is a deliberate near-duplicate of internal/pipeline's unexported
// parseJSONL: that function is not exported, and internal/pipeline is
// explicitly out of scope for modification in this PR (see the package doc
// comment on tartpipeline.go). The JSONL schema and aggregation rules are
// shared (same Reporter, same ADR-0004 zero-events-is-a-failure rule), so
// this stays byte-for-byte aligned with pipeline.parseJSONL's behavior
// minus the live-tail-specific LiveTestEvent path, which tartpipeline has
// no use for (RunTests does not tail JSONL live in this PR).
func parseJSONL(r io.Reader) (passed, total int, failures []report.FailedTest, err error) {
	scanner := bufio.NewScanner(r)
	// 4MB max line buffer to accommodate large stack traces (matches
	// pipeline.parseJSONL).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	lineNum := 0
	sawAnyTestEvent := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if jsonErr := json.Unmarshal(line, &envelope); jsonErr != nil {
			return 0, 0, nil, &MalformedJSONLError{Line: lineNum, Snippet: snippetOf(line, 80), Cause: jsonErr}
		}

		if envelope.Type != "test_event" {
			continue
		}
		sawAnyTestEvent = true

		var ev struct {
			Type       string  `json:"type"`
			Kind       string  `json:"kind"`
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
			return 0, 0, nil, &MalformedJSONLError{Line: lineNum, Snippet: snippetOf(line, 80), Cause: jsonErr}
		}

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
			return 0, 0, nil, &EventSchemaError{Line: lineNum, Field: "kind", Cause: fmt.Errorf("expected pass|fail, got %q", ev.Kind)}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, nil, &MalformedJSONLError{Line: lineNum, Cause: scanErr}
	}

	if !sawAnyTestEvent {
		return 0, 0, nil, &ZeroEventsError{}
	}

	return passed, total, failures, nil
}

func snippetOf(b []byte, maxLen int) string {
	if len(b) > maxLen {
		return string(b[:maxLen]) + "…"
	}
	return string(b)
}

// ZeroEventsError is returned when the JSONL contained no test_event
// records. Per ADR-0004, an empty file is treated as failure rather than
// "0 tests passed."
type ZeroEventsError struct{}

func (e *ZeroEventsError) Error() string {
	return "no test events found in JSONL (zero-events rule per ADR-0004)"
}

// MalformedJSONLError is returned on the first unparseable JSON line.
type MalformedJSONLError struct {
	Line    int
	Snippet string
	Cause   error
}

func (e *MalformedJSONLError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("malformed JSON at line %d: %v (snippet: %q)", e.Line, e.Cause, e.Snippet)
	}
	return fmt.Sprintf("malformed JSON at line %d: %v", e.Line, e.Cause)
}

func (e *MalformedJSONLError) Unwrap() error { return e.Cause }

// EventSchemaError is returned when a JSON line parses but is missing a
// required field or has a malformed enum value.
type EventSchemaError struct {
	Line  int
	Field string
	Cause error
}

func (e *EventSchemaError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("event schema error at line %d, field %q: %v", e.Line, e.Field, e.Cause)
	}
	return fmt.Sprintf("event schema error at line %d: missing field %q", e.Line, e.Field)
}

func (e *EventSchemaError) Unwrap() error { return e.Cause }
