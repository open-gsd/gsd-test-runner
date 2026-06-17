// Package runrender turns a run-and-die report.Report into node:test-style
// output a coding agent recognises, plus the matching process exit code. It is
// the rendering core of `gsd-test run` (issue #67, ADR-0022 Decision 2): the
// agent runs `gsd-test run` instead of `node --test`, and gets back a verdict
// it can parse exactly as it would node's own output — with a reaped run shown
// as a loud, attributed failure rather than a silent pass or an ambiguous flake.
package runrender

import (
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// Render returns the node:test-style text for rep and the exit code:
// 0 passed, 1 failed or reaped, 2 infra/spec error. It derives counts and
// failing-test names from PerTest (the run-and-die envelope populates that, not
// Total/Failures), and uses Failures for error detail when present.
func Render(rep report.Report) (string, int) {
	var b strings.Builder

	var passed, failed, killed int
	for _, t := range rep.PerTest {
		switch t.Status {
		case "failed":
			failed++
		case "killed":
			killed++
		default:
			passed++
		}
		mark := "✔"
		if t.Status == "failed" || t.Status == "killed" {
			mark = "✖"
		}
		fmt.Fprintf(&b, "%s %s (%sms)\n", mark, label(t.File, t.Name), trimDur(t.DurationMs))
		// A passing-but-leaky test (exited_clean=false) is the independent leak
		// signal (ADR-0021 §F) — surface it as a warning, not a failure.
		if t.ExitedClean == false && t.Status == "passed" {
			fmt.Fprintf(&b, "  ⚠ left a handle open at exit (exited_clean=false)\n")
		}
	}

	// Error detail for failures, when the report carries it (pipeline path);
	// the run-and-die envelope names failures via PerTest above instead.
	for _, f := range rep.Failures {
		fmt.Fprintf(&b, "✖ %s\n", label(f.File, f.Name))
		if f.Error != "" {
			fmt.Fprintf(&b, "  %s\n", f.Error)
		}
	}

	if rep.Outcome == report.OutcomeReaped {
		renderReaped(&b, rep.Kill, killed)
	}

	total := len(rep.PerTest)
	if total == 0 {
		// No per-test telemetry (e.g. infra error before any test ran); fall back
		// to the parsed counts if the report carries them.
		total, passed, failed = rep.Total, rep.Passed, rep.Failed
	}
	fmt.Fprintf(&b, "ℹ tests %d\n", total)
	fmt.Fprintf(&b, "ℹ pass %d\n", passed)
	fmt.Fprintf(&b, "ℹ fail %d\n", failed+killed)
	if killed > 0 {
		fmt.Fprintf(&b, "ℹ reaped %d\n", killed)
	}

	return b.String(), exitCode(rep.Outcome)
}

// renderReaped writes the loud, attributed kill block. A reap is a real failure
// the agent must act on (fix the runaway), not raise the estimate.
func renderReaped(b *strings.Builder, k *report.KillRecord, killed int) {
	b.WriteString("\n")
	b.WriteString("✖✖ REAPED — the run exceeded its deadline and was killed in its container.\n")
	if k == nil {
		b.WriteString("   (no kill record available)\n")
		return
	}
	fmt.Fprintf(b, "   killed after %dms (deadline %dms, reason %s)\n", k.ElapsedMs, k.EffectiveDeadlineMs, k.Reason)
	if k.LastActiveTest != nil {
		fmt.Fprintf(b, "   runaway: %s\n", label(k.LastActiveTest.File, k.LastActiveTest.Name))
	} else if len(k.InFlightTests) > 0 {
		f := k.InFlightTests[0]
		fmt.Fprintf(b, "   runaway: %s (running %dms when killed)\n", label(f.File, f.Name), f.StartedMsAgo)
	} else {
		b.WriteString("   runaway: unattributed (a synchronous wedge can block the reporter)\n")
	}
	b.WriteString("   This is a real failure — fix the leaked timer/socket/loop; do not just raise the estimate.\n")
}

// label renders "file > name", "name", or "file" depending on what is present.
func label(file, name string) string {
	switch {
	case file != "" && name != "":
		return file + " > " + name
	case name != "":
		return name
	default:
		return file
	}
}

// trimDur formats a duration without trailing-zero noise (3 → "3", 3.5 → "3.5").
func trimDur(ms float64) string {
	s := fmt.Sprintf("%.1f", ms)
	s = strings.TrimSuffix(s, ".0")
	return s
}

func exitCode(o report.Outcome) int {
	switch o {
	case report.OutcomePassed:
		return 0
	case report.OutcomeFailed, report.OutcomeReaped:
		return 1
	default: // infra_error or unknown
		return 2
	}
}
