package digest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

func TestVerdict_Shape(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 5, 7,
			fail("a.test.js", "x", "assertion", "boom", "at a (a.test.js:3:1)", ""),
			fail("b.test.js", "y", "timeout", "timed out", "", ""),
		),
		makeReport("windows", 6, 7, fail("a.test.js", "x", "assertion", "boom", "at a (a.test.js:3:1)", "")),
	}
	paths := Paths{Dir: "/runs/run-x", FailuresJSON: "/runs/run-x/failures.json"}
	v := Verdict(reps, paths)

	if v.Type != "verdict" {
		t.Errorf("type = %q, want verdict", v.Type)
	}
	if v.Outcome != string(report.OutcomeFailed) {
		t.Errorf("outcome = %q, want failed", v.Outcome)
	}
	// Two unique failures (the a.test.js failure dedups across linux+windows).
	if v.UniqueFailures != 2 {
		t.Errorf("unique_failures = %d, want 2", v.UniqueFailures)
	}
	if v.TotalFailures != 3 {
		t.Errorf("total_failures = %d, want 3 (2 linux + 1 windows)", v.TotalFailures)
	}
	if _, ok := v.PerOS["linux"]; !ok {
		t.Errorf("per_os missing linux: %+v", v.PerOS)
	}
	if v.Artifacts.FailuresJSON != "/runs/run-x/failures.json" {
		t.Errorf("artifacts.failures_json = %q", v.Artifacts.FailuresJSON)
	}
}

func TestVerdict_TopCapped(t *testing.T) {
	var fails []report.FailedTest
	for _, c := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		fails = append(fails, fail(c+".test.js", c, "assertion", c, "", ""))
	}
	v := Verdict([]report.Report{makeReport("linux", 0, 7, fails...)}, Paths{})
	if len(v.Top) != VerdictTopN {
		t.Errorf("top length = %d, want %d", len(v.Top), VerdictTopN)
	}
}

func TestVerdictLine_WriteLine(t *testing.T) {
	v := Verdict([]report.Report{makeReport("linux", 7, 7)}, Paths{})
	var buf bytes.Buffer
	if err := v.WriteLine(&buf); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("verdict line must end in newline")
	}
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("verdict must be a single line, got:\n%s", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("verdict line is not valid JSON: %v", err)
	}
	if decoded["type"] != "verdict" || decoded["outcome"] != "passed" {
		t.Errorf("unexpected verdict fields: %v", decoded)
	}
}
