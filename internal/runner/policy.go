package runner

import (
	"errors"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// Result is one scheduled unit's outcome — the pure data Aggregate classifies,
// free of renderer/artifact side effects. Run collects these from the
// scheduler results and hands them to Aggregate.
type Result struct {
	OS        string
	NodeMajor string
	ImageID   string
	Version   string
	Report    report.Report
	Err       error // the pipeline's error (e.g. a *pipeline.LegError); nil on success
	// NotRun is non-nil when the scheduler could not run this cell at all
	// (e.g. schedule.ErrNoBench). Such a result is an infra error.
	NotRun error
}

// Aggregation is Aggregate's output: the final classified reports
// (infra-marked where a leg error or not-run occurred) and the run exit code.
type Aggregation struct {
	Reports  []report.Report
	ExitCode int
}

// Aggregate classifies scheduler results into the final reports and the run's
// exit code (ADR-0009/0023), free of side effects. It is the testable policy
// core of the multi-OS run (ADR-0028).
//
// Classification:
//   - a result with NotRun != nil or Err != nil is an infra error: the report is
//     marked OutcomeInfraError and forces exit code 2 (inconclusive).
//   - otherwise a result whose Report.Kind == KindFail forces exit code 1.
//   - a non-empty skipped count also forces exit code 2.
//
// The returned Reports carry the infra marking so verdict/artifact emission
// (which consumes them) sees the corrected outcome. Run feeds the renderer the
// ORIGINAL reports via AddResult — the renderer and the verdict legitimately
// differ on infra-marked runs.
func Aggregate(results []Result, skipped int) Aggregation {
	reps := make([]report.Report, 0, len(results))
	var sawLegError, sawFail bool
	for _, res := range results {
		switch {
		case res.NotRun != nil:
			sawLegError = true
			infra := report.New(res.OS, "", res.ImageID, res.Version, time.Time{})
			infra.NodeMajor = res.NodeMajor
			infra.Outcome = report.OutcomeInfraError
			reps = append(reps, infra)
		case res.Err != nil:
			sawLegError = true
			infra := res.Report
			infra.OS = res.OS
			infra.NodeMajor = res.NodeMajor
			infra.Outcome = report.OutcomeInfraError
			reps = append(reps, infra)
		default:
			reps = append(reps, res.Report)
			if res.Report.Kind == report.KindFail {
				sawFail = true
			}
		}
	}
	exit := ExitAllPass
	switch {
	case sawLegError || skipped > 0:
		exit = ExitInconclusive
	case sawFail:
		exit = ExitSomeFailed
	}
	return Aggregation{Reports: reps, ExitCode: exit}
}

// ResolveEffective resolves the "CLI flag → config default → error" precedence
// for the run's target OSes, pin, and exclude list (ADR-0028). It is the
// testable policy core of Run's config-resolution phase — the rule whose
// calling policy was previously untested while its commaSplit helper was.
//
// For each field a non-empty flag value wins; otherwise the config default is
// used. Targets has an additional rule: if both flag and default are empty it
// is an error (there is nothing to run).
func ResolveEffective(cfg *config.Config, opts Options) (targets []string, pin string, exclude []string, err error) {
	targets = commaSplit(opts.Targets)
	if len(targets) == 0 {
		targets = cfg.Defaults.Targets
	}
	if len(targets) == 0 {
		return nil, "", nil, errors.New("no target OSes specified (use --targets or set defaults.targets in config)")
	}
	pin = opts.Pin
	if pin == "" {
		pin = cfg.Defaults.Pin
	}
	exclude = commaSplit(opts.Exclude)
	if len(exclude) == 0 {
		exclude = cfg.Defaults.Exclude
	}
	return targets, pin, exclude, nil
}
