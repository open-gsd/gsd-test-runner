package renderer_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/renderer"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// makeEvent is a helper to construct a pipeline.Event with a timestamp.
func makeEvent(kind pipeline.EventKind, leg pipeline.Leg, line, stream, detail string) pipeline.Event {
	return pipeline.Event{
		Kind:   kind,
		Time:   time.Now(),
		Leg:    leg,
		Line:   line,
		Stream: stream,
		Detail: detail,
	}
}

// makePassReport returns a KindPass Report.
func makePassReport(osName string, total, passed int) report.Report {
	r := report.New(osName, "bench-"+osName+"-1", "image:latest", "v1.0.0", time.Now())
	r.Total = total
	r.Passed = passed
	r.Failed = 0
	r.Failures = []report.FailedTest{}
	r.Finalize(time.Now())
	return r
}

// makeFailReport returns a KindFail Report with one failure.
func makeFailReport(osName string, total, passed int, failures []report.FailedTest) report.Report {
	r := report.New(osName, "bench-"+osName+"-1", "image:latest", "v1.0.0", time.Now())
	r.Total = total
	r.Passed = passed
	r.Failed = len(failures)
	r.Failures = failures
	r.Finalize(time.Now())
	return r
}

// drainAndClose sends events to ch then closes it.
func drainAndClose(ch chan<- pipeline.Event, events []pipeline.Event) {
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
}

// failEvent builds an EventTestFail carrying the Option I evidence fields.
func failEvent(file string, line int, class, name, msg string) pipeline.Event {
	return pipeline.Event{
		Kind:       pipeline.EventTestFail,
		Time:       time.Now(),
		Line:       name,
		File:       file,
		FailLine:   line,
		ErrorClass: class,
		Detail:     msg,
	}
}

func TestRenderTTY_QuietSuppressesNoiseShowsFailure(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY).SetVerbosity(renderer.VerbosityQuiet)
	ch := make(chan pipeline.Event, 8)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventChildOutput, pipeline.LegNpmCI, "npm noise", "stdout", ""),
		makeEvent(pipeline.EventTestPass, 0, "passes", "", ""),
		failEvent("a.test.js", 12, "assertion", "boom > x", "expected 1, got 2"),
	})
	r.Wait()

	out := buf.String()
	if strings.Contains(out, "npm noise") {
		t.Errorf("quiet mode must suppress child output:\n%s", out)
	}
	if strings.Contains(out, "✓ passes") {
		t.Errorf("quiet mode must suppress per-test pass lines:\n%s", out)
	}
	if !strings.Contains(out, "✗ FAIL a.test.js:12 · assertion · boom > x — expected 1, got 2") {
		t.Errorf("expected the enriched real-time failure line, got:\n%s", out)
	}
}

func TestRenderTTY_VerboseShowsChildAndPass(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY).SetVerbosity(renderer.VerbosityFull)
	ch := make(chan pipeline.Event, 8)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventChildOutput, pipeline.LegNpmCI, "npm noise", "stdout", ""),
		makeEvent(pipeline.EventTestPass, 0, "passes", "", ""),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "npm noise") || !strings.Contains(out, "✓ passes") {
		t.Errorf("verbose mode must show child output + pass lines:\n%s", out)
	}
}

func TestRenderTTY_NormalHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY).SetVerbosity(renderer.VerbosityNormal)
	ch := make(chan pipeline.Event, 64)
	r.Subscribe("linux", ch)

	evs := make([]pipeline.Event, 0, 25)
	for i := 0; i < 25; i++ {
		evs = append(evs, makeEvent(pipeline.EventTestPass, 0, "p", "", ""))
	}
	drainAndClose(ch, evs)
	r.Wait()

	out := buf.String()
	if strings.Contains(out, "✓ p") {
		t.Errorf("normal mode must not print per-test pass lines:\n%s", out)
	}
	if !strings.Contains(out, "… 25 passed") {
		t.Errorf("expected a heartbeat line at 25 passes, got:\n%s", out)
	}
}

func TestNew_ReturnsRenderer(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)
	if r == nil {
		t.Fatal("New returned nil")
	}
}

func TestSubscribe_ConsumesEvents(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 10)
	r.Subscribe("linux", ch)

	events := []pipeline.Event{
		makeEvent(pipeline.EventLegStart, pipeline.LegCheckImageVersion, "", "", ""),
		makeEvent(pipeline.EventLegSuccess, pipeline.LegCheckImageVersion, "", "", ""),
		makeEvent(pipeline.EventLegStart, pipeline.LegNpmCI, "", "", ""),
	}
	drainAndClose(ch, events)
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "[linux]") {
		t.Errorf("expected [linux] prefix in output, got: %q", out)
	}
}

func TestRenderTTY_LegStart_Format(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 5)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventLegStart, pipeline.LegCheckImageVersion, "", "", ""),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "[linux]") {
		t.Errorf("expected [linux] prefix, got: %q", out)
	}
	if !strings.Contains(out, "START") {
		t.Errorf("expected START keyword, got: %q", out)
	}
	if !strings.Contains(out, "check_image_version") {
		t.Errorf("expected leg name check_image_version, got: %q", out)
	}
}

func TestRenderTTY_LegFailure_Format(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 5)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventLegFailure, pipeline.LegNpmCI, "", "", "npm install failed: exit 1"),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected FAIL keyword, got: %q", out)
	}
	if !strings.Contains(out, "npm_ci") {
		t.Errorf("expected leg name npm_ci, got: %q", out)
	}
	if !strings.Contains(out, "npm install failed: exit 1") {
		t.Errorf("expected detail in output, got: %q", out)
	}
}

func TestRenderTTY_ChildOutput_StreamTag(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 10)
	r.Subscribe("linux", ch)
	// stdout event uses stream "stdout" → tag "|1"
	// stderr event uses stream "stderr" → tag "|2"
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventChildOutput, pipeline.LegNpmCI, "stdout line", "stdout", ""),
		makeEvent(pipeline.EventChildOutput, pipeline.LegNpmCI, "stderr line", "stderr", ""),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "|1") {
		t.Errorf("expected |1 tag for stdout, got: %q", out)
	}
	if !strings.Contains(out, "|2") {
		t.Errorf("expected |2 tag for stderr, got: %q", out)
	}
	if !strings.Contains(out, "stdout line") {
		t.Errorf("expected stdout line content, got: %q", out)
	}
	if !strings.Contains(out, "stderr line") {
		t.Errorf("expected stderr line content, got: %q", out)
	}
}

func TestRenderTTY_TestPass(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 5)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventTestPass, 0, "should pass the test", "", ""),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "✓") {
		t.Errorf("expected ✓ checkmark for test pass, got: %q", out)
	}
	if !strings.Contains(out, "should pass the test") {
		t.Errorf("expected test name in output, got: %q", out)
	}
}

func TestRenderTTY_Summary_AllPass(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	// No events — just a result
	ch := make(chan pipeline.Event, 1)
	r.Subscribe("linux", ch)
	close(ch)

	rep := makePassReport("linux", 10, 10)
	r.AddResult("linux", rep, nil)
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "Summary") {
		t.Errorf("expected Summary header, got: %q", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS in summary, got: %q", out)
	}
	if !strings.Contains(out, "10/10") {
		t.Errorf("expected 10/10 tests count, got: %q", out)
	}
}

func TestRenderTTY_Summary_HasFailures(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 1)
	r.Subscribe("linux", ch)
	close(ch)

	failures := []report.FailedTest{
		{
			Name:  "auth > login > should reject invalid credentials",
			Error: "AssertionError: expected 401 but got 200",
		},
		{
			Name:  "auth > logout > should clear session",
			Error: "TimeoutError: test timed out after 5000ms",
		},
	}
	rep := makeFailReport("linux", 10, 8, failures)
	r.AddResult("linux", rep, nil)
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected FAIL in summary, got: %q", out)
	}
	if !strings.Contains(out, "auth > login > should reject invalid credentials") {
		t.Errorf("expected first failure name in output, got: %q", out)
	}
	if !strings.Contains(out, "AssertionError: expected 401 but got 200") {
		t.Errorf("expected first failure error in output, got: %q", out)
	}
	if !strings.Contains(out, "auth > logout > should clear session") {
		t.Errorf("expected second failure name in output, got: %q", out)
	}
}

func TestRenderTTY_Summary_Inconclusive(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 1)
	r.Subscribe("linux", ch)
	close(ch)

	legErr := fmt.Errorf("pipeline leg npm_ci failed (exit 13): npm install error")
	r.AddResult("linux", report.Report{}, legErr)
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "INCONCLUSIVE") {
		t.Errorf("expected INCONCLUSIVE in summary, got: %q", out)
	}
	if !strings.Contains(out, "npm install error") {
		t.Errorf("expected error message in output, got: %q", out)
	}
}

func TestRenderJSONEvents_OneLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeJSONEvents)

	ch := make(chan pipeline.Event, 10)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		makeEvent(pipeline.EventLegStart, pipeline.LegCheckImageVersion, "", "", ""),
		makeEvent(pipeline.EventLegSuccess, pipeline.LegCheckImageVersion, "", "", ""),
		makeEvent(pipeline.EventChildOutput, pipeline.LegNpmCI, "progress output", "stdout", ""),
	})
	r.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 JSON lines, got %d: %q", len(lines), buf.String())
	}

	for i, line := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line %d is not valid JSON: %v — %q", i+1, err, line)
			continue
		}
		osVal, ok := m["os"].(string)
		if !ok || osVal != "linux" {
			t.Errorf("line %d: expected os=linux, got %v", i+1, m["os"])
		}
		if _, ok := m["kind"]; !ok {
			t.Errorf("line %d: missing kind field", i+1)
		}
		if _, ok := m["time"]; !ok {
			t.Errorf("line %d: missing time field", i+1)
		}
	}
}

func TestRenderJSONEvents_FinalResults(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeJSONEvents)

	// Subscribe two channels and close both immediately
	linuxCh := make(chan pipeline.Event, 1)
	windowsCh := make(chan pipeline.Event, 1)
	r.Subscribe("linux", linuxCh)
	r.Subscribe("windows", windowsCh)
	close(linuxCh)
	close(windowsCh)

	r.AddResult("linux", makePassReport("linux", 5, 5), nil)
	r.AddResult("windows", makePassReport("windows", 5, 5), nil)
	r.Wait()

	// printJSONResults emits one line per OS with type:"result"
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	resultLines := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("invalid JSON line: %v — %q", err, line)
			continue
		}
		if m["type"] == "result" {
			resultLines++
			if _, ok := m["os"]; !ok {
				t.Errorf("result line missing os field: %q", line)
			}
			if _, ok := m["report"]; !ok {
				t.Errorf("result line missing report field: %q", line)
			}
		}
	}
	if resultLines != 2 {
		t.Errorf("expected 2 result lines, got %d; output: %q", resultLines, buf.String())
	}
}

func TestSubscribe_MultipleOSes(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	linuxCh := make(chan pipeline.Event, 10)
	windowsCh := make(chan pipeline.Event, 10)
	r.Subscribe("linux", linuxCh)
	r.Subscribe("windows", windowsCh)

	drainAndClose(linuxCh, []pipeline.Event{
		makeEvent(pipeline.EventLegStart, pipeline.LegCheckImageVersion, "", "", ""),
	})
	drainAndClose(windowsCh, []pipeline.Event{
		makeEvent(pipeline.EventLegStart, pipeline.LegBuild, "", "", ""),
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "[linux]") {
		t.Errorf("expected [linux] prefix in output, got: %q", out)
	}
	if !strings.Contains(out, "[windows]") {
		t.Errorf("expected [windows] prefix in output, got: %q", out)
	}
}

func TestRenderer_EventLegSkipped(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event, 5)
	r.Subscribe("linux", ch)
	drainAndClose(ch, []pipeline.Event{
		{
			Kind:   pipeline.EventLegSkipped,
			Leg:    pipeline.LegBuild,
			OS:     "linux",
			Time:   time.Now(),
			Detail: "no build script defined in package.json",
		},
	})
	r.Wait()

	out := buf.String()
	if !strings.Contains(out, "SKIP") {
		t.Errorf("expected SKIP keyword in output, got: %q", out)
	}
	if !strings.Contains(out, "build") {
		t.Errorf("expected leg name 'build' in output, got: %q", out)
	}
	if !strings.Contains(out, "no build script defined in package.json") {
		t.Errorf("expected detail string in output, got: %q", out)
	}
}

func TestWait_BlocksUntilAllChannelsClose(t *testing.T) {
	var buf bytes.Buffer
	r := renderer.New(&buf, renderer.ModeTTY)

	ch := make(chan pipeline.Event)
	r.Subscribe("linux", ch)

	// Track when Wait returns
	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()

	// Verify Wait hasn't returned yet (channel still open)
	select {
	case <-done:
		t.Error("Wait returned before channel was closed")
	case <-time.After(50 * time.Millisecond):
		// Good: Wait is still blocking
	}

	// Close the channel — Wait should unblock now
	close(ch)

	select {
	case <-done:
		// Good: Wait returned after channel close
	case <-time.After(500 * time.Millisecond):
		t.Error("Wait did not return after channel was closed within 500ms")
	}
}
