package runrender_test

import (
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runrender"
)

func TestRender_Passed(t *testing.T) {
	rep := report.Report{
		Outcome: report.OutcomePassed,
		PerTest: []report.TestStat{
			{File: "a.test.mjs", Name: "parses config", DurationMs: 3, Status: "passed", ExitedClean: true},
			{File: "a.test.mjs", Name: "routes 404", DurationMs: 1, Status: "passed", ExitedClean: true},
		},
	}
	out, code := runrender.Render(rep)
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "✔ a.test.mjs > parses config") || !strings.Contains(out, "✔ a.test.mjs > routes 404") {
		t.Errorf("missing pass lines:\n%s", out)
	}
	if !strings.Contains(out, "tests 2") || !strings.Contains(out, "pass 2") || !strings.Contains(out, "fail 0") {
		t.Errorf("bad summary:\n%s", out)
	}
}

func TestRender_Failed_NamesFailingTestsFromPerTest(t *testing.T) {
	rep := report.Report{
		Outcome: report.OutcomeFailed,
		PerTest: []report.TestStat{
			{File: "a.test.mjs", Name: "ok", DurationMs: 2, Status: "passed", ExitedClean: true},
			{File: "b.test.mjs", Name: "broken thing", DurationMs: 5, Status: "failed", ExitedClean: true},
		},
	}
	out, code := runrender.Render(rep)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "✖ b.test.mjs > broken thing") {
		t.Errorf("failing test not named:\n%s", out)
	}
	if !strings.Contains(out, "pass 1") || !strings.Contains(out, "fail 1") {
		t.Errorf("bad summary:\n%s", out)
	}
}

func TestRender_Failed_UsesFailureDetailsWhenPresent(t *testing.T) {
	rep := report.Report{
		Outcome: report.OutcomeFailed,
		PerTest: []report.TestStat{{File: "b.test.mjs", Name: "broken", DurationMs: 5, Status: "failed", ExitedClean: true}},
		Failures: []report.FailedTest{
			{File: "b.test.mjs", Name: "broken", Error: "AssertionError: 404 !== 200"},
		},
	}
	out, _ := runrender.Render(rep)
	if !strings.Contains(out, "AssertionError: 404 !== 200") {
		t.Errorf("failure detail/error not rendered:\n%s", out)
	}
}

func TestRender_Reaped_LoudAttributedFailure(t *testing.T) {
	rep := report.Report{
		Outcome: report.OutcomeReaped,
		PerTest: []report.TestStat{
			{File: "hang.test.mjs", Name: "wedges", DurationMs: 2000, Status: "killed", ExitedClean: false},
		},
		Kill: &report.KillRecord{
			Reason:              report.KillReason("estimate_overrun"),
			EffectiveDeadlineMs: 2000,
			ElapsedMs:           2100,
			LastActiveTest:      &report.ActiveTest{File: "hang.test.mjs", Name: "wedges"},
			InFlightTests:       []report.InFlightTest{{File: "hang.test.mjs", Name: "wedges", StartedMsAgo: 1900}},
		},
	}
	out, code := runrender.Render(rep)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (reaped is a real failure)", code)
	}
	low := strings.ToUpper(out)
	if !strings.Contains(low, "REAPED") {
		t.Errorf("reaped block not loud:\n%s", out)
	}
	if !strings.Contains(out, "wedges") || !strings.Contains(out, "hang.test.mjs") {
		t.Errorf("runaway not attributed:\n%s", out)
	}
	if !strings.Contains(out, "deadline") {
		t.Errorf("should explain it was killed for exceeding its deadline:\n%s", out)
	}
}

func TestRender_InfraError_ExitTwo(t *testing.T) {
	rep := report.Report{Outcome: report.OutcomeInfraError}
	_, code := runrender.Render(rep)
	if code != 2 {
		t.Errorf("exit = %d, want 2 for infra_error", code)
	}
}

func TestRender_LeakedHandleNoted(t *testing.T) {
	rep := report.Report{
		Outcome: report.OutcomePassed,
		PerTest: []report.TestStat{
			{File: "leaky.test.mjs", Name: "passes but leaks", DurationMs: 4, Status: "passed", ExitedClean: false},
		},
	}
	out, code := runrender.Render(rep)
	if code != 0 {
		t.Errorf("exit = %d, want 0 (leak is a warning, not a failure)", code)
	}
	if !strings.Contains(strings.ToLower(out), "handle") {
		t.Errorf("leaked-handle warning missing:\n%s", out)
	}
}
