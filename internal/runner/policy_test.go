package runner

import (
	"errors"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// t0 is a fixed timestamp so Aggregate's synthetic NotRun reports are
// deterministic (Aggregate itself uses time.Time{}).
var t0 = time.Time{}

// ── Aggregate ──────────────────────────────────────────────────────────────

func TestAggregate_AllPass_ExitZero(t *testing.T) {
	agg := Aggregate([]Result{
		{OS: "linux", Report: report.New("linux", "b", "img", "v1", t0)},
		{OS: "windows", Report: report.New("windows", "b", "img", "v1", t0)},
	}, 0)
	if agg.ExitCode != ExitAllPass {
		t.Errorf("ExitCode = %d, want %d (all pass)", agg.ExitCode, ExitAllPass)
	}
	if len(agg.Reports) != 2 {
		t.Fatalf("Reports len = %d, want 2", len(agg.Reports))
	}
	// Passing reports pass through unmodified.
	for _, r := range agg.Reports {
		if r.Outcome == report.OutcomeInfraError {
			t.Errorf("passing report wrongly marked infra: %+v", r)
		}
	}
}

func TestAggregate_OneFail_ExitOne(t *testing.T) {
	fail := report.New("linux", "b", "img", "v1", t0)
	fail.Kind = report.KindFail
	fail.Outcome = report.OutcomeFailed
	pass := report.New("windows", "b", "img", "v1", t0)
	agg := Aggregate([]Result{{OS: "linux", Report: fail}, {OS: "windows", Report: pass}}, 0)
	if agg.ExitCode != ExitSomeFailed {
		t.Errorf("ExitCode = %d, want %d (some failed)", agg.ExitCode, ExitSomeFailed)
	}
}

func TestAggregate_LegError_MarkedInfra_ExitTwo(t *testing.T) {
	rep := report.New("linux", "b", "img", "v1", t0) // a zero/partial report from a failed RunAll
	agg := Aggregate([]Result{{OS: "linux", NodeMajor: "22", ImageID: "img", Version: "v1", Report: rep, Err: errors.New("leg exploded")}}, 0)
	if agg.ExitCode != ExitInconclusive {
		t.Errorf("ExitCode = %d, want %d (leg error)", agg.ExitCode, ExitInconclusive)
	}
	if len(agg.Reports) != 1 {
		t.Fatalf("Reports len = %d, want 1", len(agg.Reports))
	}
	if agg.Reports[0].Outcome != report.OutcomeInfraError {
		t.Errorf("Report.Outcome = %q, want %q", agg.Reports[0].Outcome, report.OutcomeInfraError)
	}
	if agg.Reports[0].OS != "linux" || agg.Reports[0].NodeMajor != "22" {
		t.Errorf("infra report lost OS/NodeMajor: %+v", agg.Reports[0])
	}
}

func TestAggregate_NotRun_SyntheticInfra_ExitTwo(t *testing.T) {
	agg := Aggregate([]Result{{
		OS: "linux", NodeMajor: "22", ImageID: "img", Version: "v1",
		NotRun: errors.New("no bench"),
	}}, 0)
	if agg.ExitCode != ExitInconclusive {
		t.Errorf("ExitCode = %d, want %d (not run)", agg.ExitCode, ExitInconclusive)
	}
	if agg.Reports[0].Outcome != report.OutcomeInfraError {
		t.Errorf("synthetic report Outcome = %q, want %q", agg.Reports[0].Outcome, report.OutcomeInfraError)
	}
	if agg.Reports[0].ImageID != "img" || agg.Reports[0].ImageVersion != "v1" {
		t.Errorf("synthetic report lost identity: %+v", agg.Reports[0])
	}
}

func TestAggregate_Skipped_ForcesInconclusiveEvenIfAllPass(t *testing.T) {
	agg := Aggregate([]Result{
		{OS: "linux", Report: report.New("linux", "b", "img", "v1", t0)},
	}, 1)
	if agg.ExitCode != ExitInconclusive {
		t.Errorf("ExitCode = %d, want %d (skipped present)", agg.ExitCode, ExitInconclusive)
	}
}

func TestAggregate_LegErrorWinsOverFail(t *testing.T) {
	// Inconclusive (leg error) must outrank "some failed" — ADR-0009.
	fail := report.New("linux", "b", "img", "v1", t0)
	fail.Kind = report.KindFail
	agg := Aggregate([]Result{
		{OS: "linux", Report: fail},
		{OS: "windows", Report: report.New("windows", "b", "img", "v1", t0), Err: errors.New("leg error")},
	}, 0)
	if agg.ExitCode != ExitInconclusive {
		t.Errorf("ExitCode = %d, want %d (inconclusive outranks failed)", agg.ExitCode, ExitInconclusive)
	}
}

func TestAggregate_Empty_ExitZero(t *testing.T) {
	agg := Aggregate(nil, 0)
	if agg.ExitCode != ExitAllPass {
		t.Errorf("ExitCode = %d, want %d (no results, no skipped)", agg.ExitCode, ExitAllPass)
	}
	if len(agg.Reports) != 0 {
		t.Errorf("Reports len = %d, want 0", len(agg.Reports))
	}
}

// ── ResolveEffective ───────────────────────────────────────────────────────

func TestResolveEffective_FlagsOverrideConfig(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.Defaults{Targets: []string{"from-config"}, Pin: "config-pin", Exclude: []string{"c-excl"}},
	}
	opts := Options{Targets: "linux,windows", Pin: "flag-pin", Exclude: "w-excl"}
	targets, pin, exclude, err := ResolveEffective(cfg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 || targets[0] != "linux" || targets[1] != "windows" {
		t.Errorf("targets = %v, want [linux windows]", targets)
	}
	if pin != "flag-pin" {
		t.Errorf("pin = %q, want flag-pin", pin)
	}
	if len(exclude) != 1 || exclude[0] != "w-excl" {
		t.Errorf("exclude = %v, want [w-excl]", exclude)
	}
}

func TestResolveEffective_ConfigDefaultsWhenFlagsEmpty(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.Defaults{Targets: []string{"linux"}, Pin: "config-pin", Exclude: []string{"a", "b"}},
	}
	targets, pin, exclude, err := ResolveEffective(cfg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 || targets[0] != "linux" {
		t.Errorf("targets = %v, want config default [linux]", targets)
	}
	if pin != "config-pin" {
		t.Errorf("pin = %q, want config-pin", pin)
	}
	if len(exclude) != 2 {
		t.Errorf("exclude = %v, want config default [a b]", exclude)
	}
}

func TestResolveEffective_NoTargets_Error(t *testing.T) {
	// Neither flag nor config default supplies a target → error (nothing to run).
	_, _, _, err := ResolveEffective(&config.Config{}, Options{Targets: ""})
	if err == nil {
		t.Fatal("expected error when no targets resolved, got nil")
	}
}
