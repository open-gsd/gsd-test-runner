package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/plan"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/refs"
	"github.com/open-gsd/gsd-test-runner/internal/renderer"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/schedule"
	"github.com/open-gsd/gsd-test-runner/internal/worktree"
)

// Exit codes per ADR-0009.
const (
	ExitAllPass      = 0
	ExitSomeFailed   = 1
	ExitInconclusive = 2
)

// Options carries the raw flag values + writers needed to execute the multi-OS
// pipeline. The runner resolves all values internally (config loading, target
// resolution against config defaults, etc.) — callers do not pre-load config.
type Options struct {
	ConfigPath   string
	ProbeBenches bool
	Targets      string // comma-separated; falls back to config defaults.targets
	Node         string // comma-separated Node major versions
	Pin          string
	Exclude      string
	JSONEvents   bool
	Verbose      bool
	Quiet        bool
	Base         string
	Head         string
	Source       string
	Scratch      string
	Out          io.Writer
	Err          io.Writer
}

// Run executes the default multi-OS test pipeline and writes live output +
// artifacts + verdict. The last line written to opts.Out is always a
// machine-readable JSON verdict (ADR-0023 Decision 2). Returns the exit code
// (0 all pass, 1 some failed, 2 inconclusive/infra error).
func Run(ctx context.Context, opts Options) int {
	stdout, stderr := opts.Out, opts.Err

	// ── Phase 1: Load ──────────────────────────────────────────────────────
	cfg, loadErr := config.Load(opts.ConfigPath, config.LoadOptions{
		Probe: opts.ProbeBenches,
	})
	if loadErr != nil {
		fmt.Fprintf(stderr, "config.Load: %v\n", loadErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}

	targets := commaSplit(opts.Targets)
	if len(targets) == 0 {
		targets = cfg.Defaults.Targets
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "no target OSes specified (use --targets or set defaults.targets in config)")
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}

	pin := opts.Pin
	if pin == "" {
		pin = cfg.Defaults.Pin
	}
	exclude := commaSplit(opts.Exclude)
	if len(exclude) == 0 {
		exclude = cfg.Defaults.Exclude
	}

	// ── Phase 2: Plan ──────────────────────────────────────────────────────
	selector, selErr := bench.NewSelector(cfg.Registry, bench.Options{
		Pin: pin, Exclude: exclude,
	})
	if selErr != nil {
		fmt.Fprintf(stderr, "bench.NewSelector: %v\n", selErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}

	p, planErr := plan.Build(cfg, selector, targets, commaSplit(opts.Node))
	if planErr != nil {
		fmt.Fprintf(stderr, "plan.Build: %v\n", planErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}
	p.AddUnreachable(cfg.Unreachable, targets)

	// ── Construct PR-merged worktree (ADR-0010: refs.Resolve first) ────────
	baseSHA, rErr := refs.Resolve(ctx, opts.Source, opts.Base)
	if rErr != nil {
		fmt.Fprintf(stderr, "refs.Resolve(%q): %v\n", opts.Base, rErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}
	headSHA, rErr := refs.Resolve(ctx, opts.Source, opts.Head)
	if rErr != nil {
		fmt.Fprintf(stderr, "refs.Resolve(%q): %v\n", opts.Head, rErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}

	scratchDir := opts.Scratch
	if scratchDir == "" {
		scratchDir, rErr = os.MkdirTemp("", "gsd-test-")
		if rErr != nil {
			fmt.Fprintf(stderr, "create scratch dir: %v\n", rErr)
			WriteInconclusiveVerdict(stdout, stderr)
			return ExitInconclusive
		}
	}

	wt, wErr := worktree.Construct(ctx, worktree.Options{
		SourceRepo: opts.Source,
		BaseSHA:    baseSHA,
		PRSHA:      headSHA,
		ScratchDir: scratchDir,
	})
	if wErr != nil {
		fmt.Fprintf(stderr, "worktree.Construct: %v\n", wErr)
		WriteInconclusiveVerdict(stdout, stderr)
		return ExitInconclusive
	}
	defer wt.Close()

	// ── Phase 3: Fan out (OS × Node) Runs across Benches ──────────────────
	mode := renderer.ModeTTY
	if opts.JSONEvents {
		mode = renderer.ModeJSONEvents
	}
	r := renderer.New(stdout, mode)
	verbosity := renderer.VerbosityNormal
	if opts.Verbose || os.Getenv("GSD_TEST_VERBOSE") == "1" {
		verbosity = renderer.VerbosityFull
	} else if opts.Quiet {
		verbosity = renderer.VerbosityQuiet
	}
	r.SetVerbosity(verbosity)

	type jobPayload struct {
		run       plan.Run
		streamKey string
		events    chan pipeline.Event
	}
	units := make([]schedule.Unit, 0, len(p.Runs))
	for _, run := range p.Runs {
		streamKey := report.StreamKey(run.OS, run.NodeMajor)
		events := make(chan pipeline.Event, 128)
		r.Subscribe(streamKey, events)
		units = append(units, schedule.Unit{
			OS:      run.OS,
			Payload: &jobPayload{run: run, streamKey: streamKey, events: events},
		})
	}

	benchesByOS := make(map[string][]bench.Bench, len(targets))
	for _, osName := range targets {
		benchesByOS[osName] = selector.BenchesForOS(osName)
	}
	capResolver := newCapacityResolver(ctx)

	type pipelineResult struct {
		streamKey string
		rep       report.Report
		err       error
		drained   string
	}
	work := func(ctx context.Context, b bench.Bench, u schedule.Unit) any {
		jp := u.Payload.(*jobPayload)
		if ensureErr := images.EnsurePresent(ctx, b, jp.run.ImageID, images.EnsurePresentOptions{
			FallbackDockerfile: "dockerfiles/" + jp.run.OS + ".Dockerfile",
			FallbackContextDir: ".",
			FallbackBuildArgs:  map[string]string{"NODE_VERSION": jp.run.NodeMajor},
		}); ensureErr != nil {
			fmt.Fprintf(stderr, "EnsurePresent(bench=%s, image=%s): %v\n", b.Name, jp.run.ImageID, ensureErr)
			return pipelineResult{streamKey: jp.streamKey, err: ensureErr}
		}
		pl := pipeline.New(b, jp.run.ImageID, jp.run.Version, wt.Path(), cfg.Testing.Command, jp.events)
		rep, runErr := pl.RunAll(ctx)
		rep.NodeMajor = jp.run.NodeMajor
		return pipelineResult{streamKey: jp.streamKey, rep: rep, err: runErr, drained: pl.DrainedPath()}
	}

	schedResults := schedule.Run(ctx, units, benchesByOS, capResolver.capacity, work)

	// ── Aggregate + Render ─────────────────────────────────────────────────
	var sawLegError, sawFail bool
	reps := make([]report.Report, 0, len(p.Runs))
	osJSONL := make(map[string]string, len(p.Runs))
	for _, sr := range schedResults {
		jp := sr.Unit.Payload.(*jobPayload)
		res, ok := sr.Value.(pipelineResult)
		if !ok {
			close(jp.events)
			fmt.Fprintf(stderr, "%s: not run: %v\n", jp.streamKey, sr.Value)
			sawLegError = true
			infra := report.New(jp.run.OS, "", string(jp.run.ImageID), jp.run.Version, time.Now().UTC())
			infra.NodeMajor = jp.run.NodeMajor
			infra.Outcome = report.OutcomeInfraError
			r.AddResult(jp.streamKey, infra, fmt.Errorf("not run: %v", sr.Value))
			reps = append(reps, infra)
			continue
		}
		r.AddResult(res.streamKey, res.rep, res.err)
		osJSONL[res.streamKey] = res.drained
		if res.err != nil {
			sawLegError = true
			infra := res.rep
			infra.OS = jp.run.OS
			infra.NodeMajor = jp.run.NodeMajor
			infra.Outcome = report.OutcomeInfraError
			reps = append(reps, infra)
			continue
		}
		reps = append(reps, res.rep)
		if res.rep.Kind == report.KindFail {
			sawFail = true
		}
	}
	r.Wait()

	emitRunArtifacts(reps, osJSONL, stdout, stderr)

	if sawLegError || len(p.Skipped) > 0 {
		return ExitInconclusive
	}
	if sawFail {
		return ExitSomeFailed
	}
	return ExitAllPass
}

// commaSplit splits s on commas and trims whitespace from each part.
// Returns nil when s is empty (not an empty slice).
func commaSplit(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
