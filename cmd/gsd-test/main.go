// Command gsd-test is the Dev Workstation entry point for the Local Engine.
//
// It implements the 5-phase orchestration per ADR-0018 decision 5:
//
//  1. Load     — config.Load reads ~/.config/gsd-test/config.toml
//  2. Plan     — plan.Build resolves target OSes to (Bench, ImageID) Runs
//  3. EnsureImages — parallel images.EnsurePresent for each unique (Bench, ImageID)
//  4. RunPipelines — one goroutine per Run; renderer subscribes to Event channels
//  5. Aggregate+Render — collect Reports; map to exit code 0/1/2
//
// Exit codes per ADR-0009:
//
//	0 = all per-OS Reports are KindPass
//	1 = at least one Report is KindFail
//	2 = any Pipeline returned a LegError, any planning step failed, or any
//	    Plan.Skipped entry exists (infra failure — suite did not run as designed)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/plan"
	"github.com/open-gsd/gsd-test-runner/internal/refs"
	"github.com/open-gsd/gsd-test-runner/internal/renderer"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/worktree"
)

// version is overridden at build time via -ldflags="-X main.version=v1.2.3".
var version = "dev"

// Exit codes per ADR-0009:
//
//	0 = all per-OS Reports are KindPass
//	1 = at least one Report is KindFail
//	2 = at least one Pipeline returned LegError, or any planning step failed,
//	    or any Plan.Skipped entry exists (no per-OS Report could be produced)
const (
	exitAllPass      = 0
	exitSomeFailed   = 1
	exitInconclusive = 2
)

// cliFlags holds the parsed CLI flag values.
type cliFlags struct {
	printVersion bool
	configPath   string
	probeBenches bool
	targets      string
	pin          string
	exclude      string
	jsonEvents   bool
	base         string
	head         string
	source       string
	scratch      string
}

// parseFlags parses args using a fresh FlagSet and returns the populated
// cliFlags or the first parse error. Uses flag.ContinueOnError so the
// caller (run) can intercept the error rather than os.Exit being called.
func parseFlags(args []string) (cliFlags, error) {
	fs := flag.NewFlagSet("gsd-test", flag.ContinueOnError)
	var f cliFlags
	fs.BoolVar(&f.printVersion, "version", false, "print version and exit")
	fs.StringVar(&f.configPath, "config", "", "path to config.toml (default: $XDG_CONFIG_HOME/gsd-test/config.toml or ~/.config/gsd-test/config.toml)")
	fs.BoolVar(&f.probeBenches, "probe-benches", false, "probe each Bench for reachability during config.Load")
	fs.StringVar(&f.targets, "targets", "", "comma-separated OS targets (default: from config defaults.targets)")
	fs.StringVar(&f.pin, "bench", "", "pin to a specific Bench by name (default: from config defaults.pin)")
	fs.StringVar(&f.exclude, "exclude", "", "comma-separated Bench names to exclude (default: from config defaults.exclude)")
	fs.BoolVar(&f.jsonEvents, "json-events", false, "emit events as JSON Lines instead of human-readable TTY output")
	fs.StringVar(&f.base, "base", "main", "base git ref to fetch + checkout (per ADR-0010)")
	fs.StringVar(&f.head, "head", "HEAD", "PR git ref to merge into base")
	fs.StringVar(&f.source, "source", ".", "source git repo path (default: current directory)")
	fs.StringVar(&f.scratch, "scratch", "", "scratch directory for worktree construction (default: system temp dir)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, nil
}

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// run is the testable entry point. stdout receives renderer output; stderr
// receives diagnostic errors from each phase. Returns an exit code (0/1/2).
func run(args []string, stdout, stderr *os.File) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	flags, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitInconclusive
	}

	if flags.printVersion {
		fmt.Fprintln(stdout, version)
		return exitAllPass
	}

	// ── Phase 1: Load ──────────────────────────────────────────────────────────
	cfg, err := config.Load(flags.configPath, config.LoadOptions{
		Probe: flags.probeBenches,
	})
	if err != nil {
		fmt.Fprintf(stderr, "config.Load: %v\n", err)
		return exitInconclusive
	}

	targets := commaSplit(flags.targets)
	if len(targets) == 0 {
		targets = cfg.Defaults.Targets
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "no target OSes specified (use --targets or set defaults.targets in config)")
		return exitInconclusive
	}

	pin := flags.pin
	if pin == "" {
		pin = cfg.Defaults.Pin
	}
	exclude := commaSplit(flags.exclude)
	if len(exclude) == 0 {
		exclude = cfg.Defaults.Exclude
	}

	// ── Phase 2: Plan ──────────────────────────────────────────────────────────
	selector, err := bench.NewSelector(cfg.Registry, bench.Options{
		Pin: pin, Exclude: exclude,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bench.NewSelector: %v\n", err)
		return exitInconclusive
	}

	p, err := plan.Build(cfg, selector, targets)
	if err != nil {
		fmt.Fprintf(stderr, "plan.Build: %v\n", err)
		return exitInconclusive
	}
	p.AddUnreachable(cfg.Unreachable, targets)

	// ── Construct PR-merged worktree ────────────────────────────────────────────
	// Per ADR-0010: refs.Resolve before worktree.Construct.
	baseSHA, err := refs.Resolve(ctx, flags.source, flags.base)
	if err != nil {
		fmt.Fprintf(stderr, "refs.Resolve(%q): %v\n", flags.base, err)
		return exitInconclusive
	}
	headSHA, err := refs.Resolve(ctx, flags.source, flags.head)
	if err != nil {
		fmt.Fprintf(stderr, "refs.Resolve(%q): %v\n", flags.head, err)
		return exitInconclusive
	}

	scratchDir := flags.scratch
	if scratchDir == "" {
		scratchDir, err = os.MkdirTemp("", "gsd-test-")
		if err != nil {
			fmt.Fprintf(stderr, "create scratch dir: %v\n", err)
			return exitInconclusive
		}
	}

	wt, err := worktree.Construct(ctx, worktree.Options{
		SourceRepo: flags.source,
		BaseSHA:    baseSHA,
		PRSHA:      headSHA,
		ScratchDir: scratchDir,
	})
	if err != nil {
		fmt.Fprintf(stderr, "worktree.Construct: %v\n", err)
		return exitInconclusive
	}
	defer wt.Close()

	// ── Phase 3: EnsureImages (parallel) ──────────────────────────────────────
	if ensureErr := ensureImagesParallel(ctx, p.Runs, stderr); ensureErr != nil {
		return exitInconclusive
	}

	// ── Phase 4: RunPipelines (parallel) + renderer subscription ──────────────
	mode := renderer.ModeTTY
	if flags.jsonEvents {
		mode = renderer.ModeJSONEvents
	}
	r := renderer.New(stdout, mode)

	type pipelineResult struct {
		os  string
		rep report.Report
		err error
	}
	results := make(chan pipelineResult, len(p.Runs))
	var pwg sync.WaitGroup
	for _, run := range p.Runs {
		run := run
		pwg.Add(1)
		// Buffer generously to absorb burst test events (ADR-0017 dec 4).
		events := make(chan pipeline.Event, 128)
		r.Subscribe(run.OS, events)
		go func() {
			defer pwg.Done()
			pl := pipeline.New(run.Bench, run.ImageID, run.Version, wt.Path(), cfg.Testing.Command, events)
			rep, err := pl.RunAll(ctx)
			close(events) // signal renderer the channel is done
			results <- pipelineResult{os: run.OS, rep: rep, err: err}
		}()
	}
	pwg.Wait()
	close(results)

	// ── Phase 5: Aggregate + Render ────────────────────────────────────────────
	var sawLegError, sawFail bool
	for res := range results {
		r.AddResult(res.os, res.rep, res.err)
		if res.err != nil {
			sawLegError = true
			continue
		}
		if res.rep.Kind == report.KindFail {
			sawFail = true
		}
	}
	r.Wait() // blocks for renderer event consumers + emits final summary

	// Inconclusive if any Pipeline failed OR any Plan.Skipped entry exists.
	if sawLegError || len(p.Skipped) > 0 {
		return exitInconclusive
	}
	if sawFail {
		return exitSomeFailed
	}
	return exitAllPass
}

// ensureImagesParallel runs images.EnsurePresent for each unique (Bench, ImageID)
// pair from the Plan.Runs concurrently. Returns non-nil if any EnsurePresent
// failed — caller treats this as exit-2 inconclusive.
func ensureImagesParallel(ctx context.Context, runs []plan.Run, stderr *os.File) error {
	type pair struct {
		b       bench.Bench
		imageID images.ImageID
		os      string
	}
	// Dedup by (bench.Name, imageID) so we do not pull the same image twice.
	seen := make(map[string]bool, len(runs))
	var pairs []pair
	for _, r := range runs {
		key := r.Bench.Name + "|" + string(r.ImageID)
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, pair{b: r.Bench, imageID: r.ImageID, os: r.OS})
	}

	type result struct {
		p   pair
		err error
	}
	results := make(chan result, len(pairs))
	var wg sync.WaitGroup
	for _, pp := range pairs {
		pp := pp
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := images.EnsurePresent(ctx, pp.b, pp.imageID, images.EnsurePresentOptions{
				FallbackDockerfile: "dockerfiles/" + pp.os + ".Dockerfile",
				FallbackContextDir: ".",
			})
			results <- result{p: pp, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var firstErr error
	for res := range results {
		if res.err != nil {
			fmt.Fprintf(stderr, "EnsurePresent(bench=%s, image=%s): %v\n",
				res.p.b.Name, res.p.imageID, res.err)
			if firstErr == nil {
				firstErr = res.err
			}
		}
	}
	return firstErr
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
