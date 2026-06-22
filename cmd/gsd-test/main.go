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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/digest"
	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/installhooks"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/plan"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/refs"
	"github.com/open-gsd/gsd-test-runner/internal/renderer"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runrender"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
	"github.com/open-gsd/gsd-test-runner/internal/telemetry"
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

// spawnFunc launches a detached worker process and returns its PID.
// The real implementation lives in spawn_unix.go / spawn_other.go; tests
// inject a fake to avoid spawning real processes (ADR-0022 Decision 3, #70).
type spawnFunc func(runID, configPath string) (pid int, err error)

// defaultSpawn is the package-level spawn seam. Tests override and restore it
// with defer. The real value is set to realSpawn (defined in spawn_unix.go or
// spawn_other.go depending on build tags).
var defaultSpawn spawnFunc = realSpawn

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

	// Subcommand dispatch. `submit` is the agent-facing run-spec front door
	// (issue #60, ADR-0021): the agent submits a run spec instead of invoking
	// node locally.
	if len(args) > 0 && args[0] == "submit" {
		return runSubmit(args[1:], stdout, stderr)
	}

	// `run` is the explicit executor agents are routed to (issue #67, ADR-0022):
	// it runs the project's tests in Docker via the front door and prints a
	// node:test-style verdict instead of JSON, so the agent treats it like a
	// normal `node --test` while never spawning a local node test process.
	if len(args) > 0 && args[0] == "run" {
		return runRun(args[1:], stdout, stderr)
	}

	// `install-agent-hooks` wires the integration (Claude hook + skill, Codex
	// shim) onto this workstation in one idempotent, reversible step (issue #71,
	// ADR-0022 Decision 5).
	if len(args) > 0 && args[0] == "install-agent-hooks" {
		return runInstallHooks(args[1:], stdout, stderr)
	}

	// `wait` blocks until an async run completes, then renders its verdict
	// (ADR-0022 Decision 3, issue #70).
	if len(args) > 0 && args[0] == "wait" {
		return waitRun(args[1:], stdout, stderr)
	}

	// `status` reports whether an async run is in-flight or done, without
	// blocking (ADR-0022 Decision 3, issue #70).
	if len(args) > 0 && args[0] == "status" {
		return statusRun(args[1:], stdout, stderr)
	}

	// `__run-worker` is the internal detached worker entry point. It is not
	// documented in help text; it is invoked exclusively by realSpawn.
	if len(args) > 0 && args[0] == "__run-worker" {
		return runWorker(args[1:], stdout, stderr)
	}

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
			// The pipeline's pump goroutine owns closing events (it flushes the
			// unbounded queue first), so we must NOT close it here (#84).
			results <- pipelineResult{os: run.OS, rep: rep, err: err}
		}()
	}
	pwg.Wait()
	close(results)

	// ── Phase 5: Aggregate + Render ────────────────────────────────────────────
	var sawLegError, sawFail bool
	reps := make([]report.Report, 0, len(p.Runs))
	for res := range results {
		r.AddResult(res.os, res.rep, res.err)
		if res.err != nil {
			sawLegError = true
			// A leg error means the suite did not run as designed; record it as
			// an infra_error per-OS report so the digest/verdict still account
			// for this OS (RunAll returns a zero Report on error).
			infra := res.rep
			infra.OS = res.os
			infra.Outcome = report.OutcomeInfraError
			reps = append(reps, infra)
			continue
		}
		reps = append(reps, res.rep)
		if res.rep.Kind == report.KindFail {
			sawFail = true
		}
	}
	r.Wait() // blocks for renderer event consumers + emits final summary

	// Failure-first artifacts + loud last-line verdict (epic #84, ADR-0023).
	// Best-effort: a write error never changes the exit code or suppresses the
	// verdict (the verdict's outcome is the source of truth).
	emitRunArtifacts(reps, stdout, stderr)

	// Inconclusive if any Pipeline failed OR any Plan.Skipped entry exists.
	if sawLegError || len(p.Skipped) > 0 {
		return exitInconclusive
	}
	if sawFail {
		return exitSomeFailed
	}
	return exitAllPass
}

// writeRunArtifacts writes the failure-first digest (FAILURES.md, failures.json,
// per-failure files) for reps under the per-run XDG dir for runID and returns
// the artifact paths (epic #84, ADR-0023). Best-effort: a write failure is
// logged to stderr and returns whatever paths succeeded, never affecting the
// caller's exit code.
func writeRunArtifacts(runID string, reps []report.Report, stderr io.Writer) digest.Paths {
	dir, err := runstate.EnsureRunDir(runID)
	if err != nil {
		fmt.Fprintf(stderr, "warning: create run artifact dir: %v\n", err)
		return digest.Paths{}
	}
	paths, err := digest.WriteDigest(dir, reps, digest.WriteOpts{PerFailureFiles: true})
	if err != nil {
		fmt.Fprintf(stderr, "warning: write run digest: %v\n", err)
		return digest.Paths{Dir: dir}
	}
	return paths
}

// writeVerdict prints the loud last-line machine verdict as the final stdout
// line (Option C). Best-effort: never changes the exit code (the verdict's
// outcome is the source of truth regardless of the artifacts).
func writeVerdict(reps []report.Report, paths digest.Paths, stdout, stderr io.Writer) {
	if err := digest.Verdict(reps, paths).WriteLine(stdout); err != nil {
		fmt.Fprintf(stderr, "warning: write verdict line: %v\n", err)
	}
}

// emitRunArtifacts is the standard multi-OS path: it mints a run-id, writes the
// digest, and prints the verdict as the final stdout line in every outcome.
func emitRunArtifacts(reps []report.Report, stdout, stderr io.Writer) {
	runID, err := runspec.NewRunID()
	if err != nil {
		runID = "run-unknown"
	}
	writeVerdict(reps, writeRunArtifacts(runID, reps, stderr), stdout, stderr)
}

// emitRunDieArtifacts is the run-and-die single-OS path: it writes the digest
// under the run's existing run-id and prints the verdict as the final stdout
// line, so `gsd-test run`/`wait` share the standard path's failure-first
// contract (Option C, epic #84).
func emitRunDieArtifacts(runID string, rep report.Report, stdout, stderr io.Writer) {
	reps := []report.Report{rep}
	writeVerdict(reps, writeRunArtifacts(runID, reps, stderr), stdout, stderr)
}

// runSubmit implements `gsd-test submit`: read a JSON run spec (from --spec-file
// or stdin), validate it via runspec.Parse, and assign a RunID if the agent
// omitted one. Without --execute it echoes the normalized spec (the accept +
// normalize front door); with --execute it dispatches the run-and-die run to a
// Bench and emits the per-OS Report (issue #60, ADR-0021).
//
// Exit codes: 0 = accepted / all passed; 1 = the run failed or was reaped;
// 2 = the spec could not be read/validated or the run could not be started
// (inconclusive), consistent with the fail-loud contract.
func runSubmit(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("gsd-test submit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	specFile := fs.String("spec-file", "-", `path to the JSON run spec, or "-" for stdin`)
	execute := fs.Bool("execute", false, "dispatch the run to a Bench (default: validate + normalize only)")
	configPath := fs.String("config", "", "path to config.toml (used with --execute)")
	if err := fs.Parse(args); err != nil {
		return exitInconclusive
	}

	var (
		data []byte
		err  error
	)
	if *specFile == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*specFile)
	}
	if err != nil {
		fmt.Fprintf(stderr, "submit: read spec: %v\n", err)
		return exitInconclusive
	}

	spec, err := runspec.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "submit: invalid run spec: %v\n", err)
		return exitInconclusive
	}

	if spec.RunID == "" {
		id, idErr := runspec.NewRunID()
		if idErr != nil {
			fmt.Fprintf(stderr, "submit: assign run id: %v\n", idErr)
			return exitInconclusive
		}
		spec.RunID = id
	}

	if *execute {
		return executeSpec(*spec, *configPath, stdout, stderr)
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(spec); encErr != nil {
		fmt.Fprintf(stderr, "submit: encode spec: %v\n", encErr)
		return exitInconclusive
	}
	return exitAllPass
}

// runRun implements `gsd-test run`: the explicit wrapper agents call instead of
// `node --test` (issue #67, ADR-0022). It builds a run spec from the current
// repo + passed test patterns, dispatches it to a Bench via dispatchRun, and
// renders the result as node:test-style output + exit code. Positional args are
// treated as test path patterns; flags configure target, config, and estimate.
// runRun implements `gsd-test run`: the explicit wrapper agents call instead of
// `node --test` (issue #67, ADR-0022). It builds a run spec from the current
// repo + passed test patterns, dispatches it to a Bench via dispatchRun, and
// renders the result as node:test-style output + exit code. Positional args are
// treated as test path patterns; flags configure target, config, and estimate.
//
// When --async is set, delegation is immediate: a detached worker process is
// spawned and the function returns exit 0 after printing a dispatched-notice
// (ADR-0022 Decision 3, issue #70).
func runRun(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("gsd-test run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "linux", "target OS: linux | windows | macos-container")
	configPath := fs.String("config", "", "path to config.toml")
	estimateMs := fs.Int64("estimate-ms", 0, "expected suite duration in ms (tightens the watchdog deadline)")
	async := fs.Bool("async", false, "submit and return immediately; use `gsd-test wait <run-id>` to collect the result")
	if err := fs.Parse(args); err != nil {
		return exitInconclusive
	}

	repo, err := repoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return exitInconclusive
	}

	// Build a minimal spec and reuse runspec.Parse for defaults + validation
	// (testCommand defaults to node --test; budget/isolation defaults applied),
	// exactly like the submit front door.
	specReq := map[string]any{"repo": repo, "target": *target}
	if patterns := fs.Args(); len(patterns) > 0 {
		specReq["testPathPatterns"] = patterns
	}
	if *estimateMs > 0 {
		specReq["budget"] = map[string]any{"estimateMs": *estimateMs}
	}
	data, _ := json.Marshal(specReq)
	spec, err := runspec.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "run: build spec: %v\n", err)
		return exitInconclusive
	}
	if spec.RunID == "" {
		id, idErr := runspec.NewRunID()
		if idErr != nil {
			fmt.Fprintf(stderr, "run: assign run id: %v\n", idErr)
			return exitInconclusive
		}
		spec.RunID = id
	}

	if *async {
		return runAsync(*spec, *configPath, stdout, stderr, defaultSpawn)
	}

	// Notify the caller the handoff is happening (ADR-0022 Decision 4) so it does
	// not re-run locally or treat the wait as a hang.
	fmt.Fprintf(stderr, "↪ gsd-test: handed off to Docker (run-id=%s, target=%s) — do not re-run locally\n", spec.RunID, spec.Target)

	rep, ok := dispatchRun(*spec, *configPath, "run", stderr)
	if !ok {
		return exitInconclusive
	}

	text, code := runrender.Render(rep)
	fmt.Fprint(stdout, text)
	// Failure-first digest + loud verdict, shared with the standard path (#84).
	emitRunDieArtifacts(spec.RunID, rep, stdout, stderr)
	return code
}

// runAsync handles `gsd-test run --async` (ADR-0022 Decision 3, issue #70).
// It writes initial runstate, spawns a detached worker via spawn, prints a
// dispatched-notice to stdout, and returns exit 0 immediately. The run
// continues in the worker process; use `gsd-test wait <run-id>` to collect
// the result.
func runAsync(spec runspec.Spec, configPath string, stdout, stderr *os.File, spawn spawnFunc) int {
	now := time.Now().UTC()
	st := runstate.State{
		RunID:     spec.RunID,
		Target:    spec.Target,
		Repo:      spec.Repo,
		Status:    runstate.StatusRunning,
		StartedAt: now,
		UpdatedAt: now,
		Spec:      spec,
	}
	if err := runstate.Save(st); err != nil {
		fmt.Fprintf(stderr, "run --async: save initial state: %v\n", err)
		return exitInconclusive
	}

	pid, err := spawn(spec.RunID, configPath)
	if err != nil {
		fmt.Fprintf(stderr, "run --async: spawn worker: %v\n", err)
		// Mark the state as done/failed so status/wait don't hang.
		st.Status = runstate.StatusDone
		st.ExitCode = exitInconclusive
		st.Err = fmt.Sprintf("spawn failed: %v", err)
		st.UpdatedAt = time.Now().UTC()
		_ = runstate.Save(st) // best-effort
		return exitInconclusive
	}

	// Fix 2 (issue #70): the parent must NOT write a second save after spawn.
	// Previously it saved st.PID=pid here, which could overwrite a done state
	// the worker had already written (lost update). The worker now claims its own
	// PID via a save immediately on startup (see runWorker), so the parent only
	// writes the initial running state above. The pid value from spawn is still
	// checked below solely to detect spawn errors.
	_ = pid // pid used only for spawn-error detection above; worker owns state from here

	fmt.Fprintf(stdout, "dispatched run-id=%s  (use `gsd-test wait %s` to collect the result, `gsd-test status %s` to check progress)\n",
		spec.RunID, spec.RunID, spec.RunID)
	return exitAllPass
}

// runWorker implements the hidden `gsd-test __run-worker` subcommand
// (ADR-0022 Decision 3, issue #70). It is invoked exclusively by realSpawn
// as a detached process. It loads the runstate, calls dispatchRun, and writes
// the final state (done + Report or done + Err) before exiting.
func runWorker(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("gsd-test __run-worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runID := fs.String("run-id", "", "run id (required)")
	configPath := fs.String("config", "", "path to config.toml")
	if err := fs.Parse(args); err != nil {
		return exitInconclusive
	}
	if *runID == "" {
		fmt.Fprintln(stderr, "__run-worker: --run-id is required")
		return exitInconclusive
	}

	st, err := runstate.Load(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "__run-worker: load state for %s: %v\n", *runID, err)
		return exitInconclusive
	}

	// Fix 2 (issue #70): claim the worker's own PID in the state immediately,
	// before dispatch, so the liveness guard in waitRun can observe it. The
	// parent no longer writes a second save after spawn (see runAsync), so this
	// is the only write that sets PID. Status stays running; only PID+UpdatedAt
	// change here.
	st.PID = os.Getpid()
	st.UpdatedAt = time.Now().UTC()
	if saveErr := runstate.Save(st); saveErr != nil {
		// Non-fatal: the liveness guard will see pid 0 but won't misbehave.
		fmt.Fprintf(stderr, "__run-worker: save pid claim: %v\n", saveErr)
	}

	rep, ok := dispatchRun(st.Spec, *configPath, "run --async", stderr)
	st.UpdatedAt = time.Now().UTC()
	if ok {
		_, code := runrender.Render(rep)
		// Persist the failure-first digest now so artifacts exist on disk as soon
		// as the async run finishes; `gsd-test wait` prints the verdict (#84).
		_ = writeRunArtifacts(st.Spec.RunID, []report.Report{rep}, stderr)
		st.Report = &rep
		st.ExitCode = code
		st.Status = runstate.StatusDone
	} else {
		st.Status = runstate.StatusDone
		st.ExitCode = exitInconclusive
		st.Err = "dispatch failed"
	}
	if saveErr := runstate.Save(st); saveErr != nil {
		fmt.Fprintf(stderr, "__run-worker: save final state: %v\n", saveErr)
	}
	return st.ExitCode
}

// waitRun implements `gsd-test wait <run-id>` (ADR-0022 Decision 3, issue #70).
// It polls until the run reaches Status=done, then renders the verdict identically
// to a blocking `gsd-test run`. It never renders a partial result.
func waitRun(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "wait: usage: gsd-test wait <run-id>")
		return exitInconclusive
	}
	runID := args[0]

	st, err := runstate.Load(runID)
	if err != nil {
		if isErrNotFound(err) {
			fmt.Fprintf(stderr, "wait: unknown run-id %s\n", runID)
			return exitInconclusive
		}
		fmt.Fprintf(stderr, "wait: load state: %v\n", err)
		return exitInconclusive
	}

	// asyncWaitCeiling is the absolute wall-clock backstop for the wait loop.
	// It is set safely beyond the run-and-die hard cap of 1h (ADR-0021), so
	// a legitimately long run is never cut short (Fix 3, issue #70).
	const asyncWaitCeiling = 90 * time.Minute

	// Compute deadline from st.StartedAt. Guard against a zero StartedAt (e.g.
	// state written by an older client) by falling back to the local loop start.
	loopStart := time.Now()
	backstopBase := st.StartedAt
	if backstopBase.IsZero() {
		backstopBase = loopStart
	}
	deadline := backstopBase.Add(asyncWaitCeiling)

	// Poll until done.
	for st.Status == runstate.StatusRunning {
		// Fix 3 (issue #70): absolute wall-clock backstop. If the run has been
		// in the running state for longer than asyncWaitCeiling, the worker
		// almost certainly died silently (pid reuse, kernel crash, etc.). Fail
		// loud and return inconclusive rather than hanging forever.
		if time.Now().After(deadline) {
			dir, _ := runstate.Dir()
			fmt.Fprintf(stderr,
				"wait: run %s did not complete within %s — the worker likely died; check %s/%s.worker.log\n",
				runID, asyncWaitCeiling, dir, runID)
			return exitInconclusive
		}

		time.Sleep(200 * time.Millisecond)

		// Fix 1 (issue #70): reload FIRST, then re-evaluate the loop condition.
		// The liveness guard must only fire when the freshly-reloaded state is
		// STILL running — if the worker wrote done and exited during the sleep,
		// the done state must always win over the liveness guard.
		st, err = runstate.Load(runID)
		if err != nil {
			fmt.Fprintf(stderr, "wait: reload state: %v\n", err)
			return exitInconclusive
		}

		// Liveness guard: only applies when the fresh state is still running.
		// If the worker PID is no longer alive at this point, the worker died
		// without writing a final state.
		if st.Status == runstate.StatusRunning && st.PID > 0 && !workerPIDAlive(st.PID) {
			fmt.Fprintf(stderr, "wait: worker for run-id %s is gone (no result written)\n", runID)
			return exitInconclusive
		}
	}

	if st.Err != "" {
		fmt.Fprintf(stderr, "wait: run %s failed: %s\n", runID, st.Err)
		return exitInconclusive
	}
	if st.Report == nil {
		fmt.Fprintf(stderr, "wait: run %s has no report\n", runID)
		return exitInconclusive
	}

	text, code := runrender.Render(*st.Report)
	fmt.Fprint(stdout, text)
	// Same failure-first digest + verdict as a blocking `gsd-test run` (#84).
	emitRunDieArtifacts(st.Spec.RunID, *st.Report, stdout, stderr)
	return code
}

// statusRun implements `gsd-test status <run-id>` (ADR-0022 Decision 3, issue #70).
// It reports in-flight vs done WITHOUT blocking. Exit is always 0 when the run-id
// is found — status is a pure reporter and must not itself fail because the run
// failed.
func statusRun(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "status: usage: gsd-test status <run-id>")
		return exitInconclusive
	}
	runID := args[0]

	st, err := runstate.Load(runID)
	if err != nil {
		if isErrNotFound(err) {
			fmt.Fprintf(stderr, "status: unknown run-id %s\n", runID)
			return exitInconclusive
		}
		fmt.Fprintf(stderr, "status: load state: %v\n", err)
		return exitInconclusive
	}

	switch st.Status {
	case runstate.StatusRunning:
		fmt.Fprintf(stdout, "state=running run-id=%s pid=%d\n", runID, st.PID)
	default: // done
		outcome := "infra_error"
		if st.Report != nil {
			outcome = string(st.Report.Outcome)
		}
		fmt.Fprintf(stdout, "state=done run-id=%s exit=%d outcome=%s\n", runID, st.ExitCode, outcome)
	}
	return exitAllPass
}

// isErrNotFound reports whether err wraps runstate.ErrNotFound.
func isErrNotFound(err error) bool {
	// Use errors.Is for proper sentinel unwrapping.
	return errors.Is(err, runstate.ErrNotFound)
}

// repoRoot returns the git toplevel of the current directory, falling back to
// the working directory when not inside a git repo.
func repoRoot() (string, error) {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine repo root: %w", err)
	}
	return wd, nil
}

// runInstallHooks implements `gsd-test install-agent-hooks` (issue #71,
// ADR-0022 D5): a one-command, idempotent, reversible installer for the agent
// integration. Defaults to both runtimes and project scope.
func runInstallHooks(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("gsd-test install-agent-hooks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	claude := fs.Bool("claude", false, "install the Claude Code hook + skill (default: both runtimes)")
	codex := fs.Bool("codex", false, "install the Codex shim (default: both runtimes)")
	global := fs.Bool("global", false, "install into $HOME instead of the current project")
	uninstall := fs.Bool("uninstall", false, "reverse a previous install")
	if err := fs.Parse(args); err != nil {
		return exitInconclusive
	}

	root := "."
	if *global {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "install-agent-hooks: home dir: %v\n", err)
			return exitInconclusive
		}
		root = home
	} else {
		r, err := repoRoot()
		if err != nil {
			fmt.Fprintf(stderr, "install-agent-hooks: %v\n", err)
			return exitInconclusive
		}
		root = r
	}

	if *uninstall {
		if err := installhooks.Uninstall(installhooks.ManifestPath(root)); err != nil {
			fmt.Fprintf(stderr, "install-agent-hooks: %v\n", err)
			return exitInconclusive
		}
		fmt.Fprintf(stdout, "gsd-test: agent integration uninstalled from %s\n", root)
		return exitAllPass
	}

	// Default to both runtimes when neither flag is given.
	wantClaude, wantCodex := *claude, *codex
	if !wantClaude && !wantCodex {
		wantClaude, wantCodex = true, true
	}

	man, err := installhooks.Install(installhooks.Options{Root: root, Claude: wantClaude, Codex: wantCodex})
	if err != nil {
		fmt.Fprintf(stderr, "install-agent-hooks: %v\n", err)
		return exitInconclusive
	}

	fmt.Fprintf(stdout, "gsd-test: agent integration installed into %s\n", root)
	for _, f := range man.Files {
		fmt.Fprintf(stdout, "  + %s\n", f)
	}
	if man.SettingsPath != "" {
		fmt.Fprintf(stdout, "  ~ %s (PreToolUse Bash guard → gsd-test run)\n", man.SettingsPath)
	}
	if wantCodex {
		fmt.Fprintf(stdout, "\nCodex: put the shim dir FIRST on Codex's exec PATH so its node/npm route\n"+
			"through gsd-test (rewrites `node --test`/`npm test` to `gsd-test run`; passes\n"+
			"everything else to the real binary). In ~/.codex/config.toml:\n"+
			"  [shell_environment_policy.set]\n  PATH = \"%s:${PATH}\"\n"+
			"This shadows node/npm only inside Codex — your interactive shell is untouched.\n",
			installhooks.CodexBinDir(root))
	}
	fmt.Fprintf(stdout, "\nReverse with `gsd-test install-agent-hooks --uninstall`.\n")
	return exitAllPass
}

// dispatchRun resolves the Bench + Tester Image for spec.Target (reusing config,
// the Bench selector, and images.EnsurePresent), then runs the copy-in
// run-and-die path under the watchdog (dispatch.RunCopyIn) via a dockerexec
// Runner, and records telemetry. It returns the resulting Report; ok is false
// when an infrastructure/inconclusive error was already reported to stderr (the
// caller returns exit 2). A reaped/failed container exit is carried in the
// Report's Outcome, not signalled by ok — so reaps surface as a loud
// OutcomeReaped Report, never a silent hang. label prefixes diagnostics so the
// caller's command name (`submit --execute` / `run`) shows in errors.
func dispatchRun(spec runspec.Spec, configPath, label string, stderr *os.File) (report.Report, bool) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(configPath, config.LoadOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: config.Load: %v\n", label, err)
		return report.Report{}, false
	}
	selector, err := bench.NewSelector(cfg.Registry, bench.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: bench.NewSelector: %v\n", label, err)
		return report.Report{}, false
	}
	b, err := selector.Pick(spec.Target)
	if err != nil {
		fmt.Fprintf(stderr, "%s: no Bench for target %q: %v\n", label, spec.Target, err)
		return report.Report{}, false
	}

	// ImageID matches the plan/pipeline convention (untagged ghcr path); the
	// expected version is verified via the OCI sentinel label, not the docker
	// tag (sentinel verification for the run-and-die path is a follow-up, like
	// the pipeline's CheckImageVersion leg).
	imageID := images.ImageID(fmt.Sprintf("ghcr.io/open-gsd/gsd-tester-%s", spec.Target))
	if err := images.EnsurePresent(ctx, b, imageID, images.EnsurePresentOptions{
		FallbackDockerfile: "dockerfiles/" + spec.Target + ".Dockerfile",
		FallbackContextDir: ".",
	}); err != nil {
		fmt.Fprintf(stderr, "%s: EnsurePresent: %v\n", label, err)
		return report.Report{}, false
	}

	// dockerexec.Run preserves stdout on a non-zero container exit (the reaped
	// envelope), which dispatch.Exec relies on to distinguish a reap from a
	// launch failure.
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		out, runErr := dockerexec.Run(ctx, b, args)
		return []byte(out), runErr
	}

	// Tier-2 reaper, "reap on next contact" (ADR-0021 Decision 2): before
	// starting, kill any run container on this Bench whose deadline has passed —
	// e.g. one whose in-container watchdog wedged on a previous run. Best-effort;
	// a sweep failure must not block this run.
	if reaped, sweepErr := reaper.Sweep(ctx, runner, time.Now().UnixMilli()); sweepErr != nil {
		fmt.Fprintf(stderr, "%s: warning: reaper sweep: %v\n", label, sweepErr)
	} else if len(reaped) > 0 {
		fmt.Fprintf(stderr, "%s: reaped %d stale container(s) before running\n", label, len(reaped))
	}

	// Verify the Tester Image's version sentinel before running, so a stale
	// image can't silently produce wrong results (ADR-0011, fail-loud).
	if err := dispatch.VerifyImageVersion(ctx, runner, string(imageID), cfg.Versions[spec.Target]); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", label, err)
		return report.Report{}, false
	}

	// Estimate fallback: when the agent gave no estimateMs, base the deadline on
	// the median of recent passing runs for this target (ADR-0021 Decision 1).
	telemetryPath := telemetry.RepoLogPath(spec.Repo)
	history, _ := telemetry.Load(telemetryPath) // missing log is normal; best-effort
	median := telemetry.MedianDurationMs(history, spec.Target)

	// Worktree: run Repo as-is, unless base+prBranch ask for a PR-merged
	// worktree built from Repo (ADR-0021 §A, reusing refs.Resolve + worktree).
	worktreeDir := spec.Repo
	if spec.Base != "" {
		baseSHA, rerr := refs.Resolve(ctx, spec.Repo, spec.Base)
		if rerr != nil {
			fmt.Fprintf(stderr, "%s: resolve base %q: %v\n", label, spec.Base, rerr)
			return report.Report{}, false
		}
		headSHA, rerr := refs.Resolve(ctx, spec.Repo, spec.PRBranch)
		if rerr != nil {
			fmt.Fprintf(stderr, "%s: resolve prBranch %q: %v\n", label, spec.PRBranch, rerr)
			return report.Report{}, false
		}
		scratch, mkErr := os.MkdirTemp("", "gsd-submit-")
		if mkErr != nil {
			fmt.Fprintf(stderr, "%s: scratch dir: %v\n", label, mkErr)
			return report.Report{}, false
		}
		wt, wErr := worktree.Construct(ctx, worktree.Options{
			SourceRepo: spec.Repo, BaseSHA: baseSHA, PRSHA: headSHA, ScratchDir: scratch,
		})
		if wErr != nil {
			fmt.Fprintf(stderr, "%s: worktree.Construct: %v\n", label, wErr)
			return report.Report{}, false
		}
		defer wt.Close()
		worktreeDir = wt.Path()
	}

	now := time.Now()
	eff := spec.Budget.EffectiveDeadlineMs(median)
	deadlineEpochMs := now.Add(time.Duration(eff) * time.Millisecond).UnixMilli()

	rep, err := dispatch.RunCopyIn(ctx, runner, spec, string(imageID), worktreeDir, deadlineEpochMs, eff, now)
	if err != nil {
		fmt.Fprintf(stderr, "%s: run: %v\n", label, err)
		return report.Report{}, false
	}

	// Record the run so the median and runaway leaderboard accumulate (D3/§F).
	rec := telemetry.RunRecord{
		RunID: spec.RunID, Target: spec.Target, Outcome: string(rep.Outcome),
		DurationMs: int64(rep.DurationMs), Reaped: rep.Outcome == report.OutcomeReaped,
	}
	if rep.Kill != nil {
		rec.ReapReason = string(rep.Kill.Reason)
	}
	for _, ts := range rep.PerTest {
		rec.PerTest = append(rec.PerTest, telemetry.TestStat{
			File: ts.File, Name: ts.Name, DurationMs: int64(ts.DurationMs),
			Status: ts.Status, ExitedClean: ts.ExitedClean,
		})
	}
	if appendErr := telemetry.Append(telemetryPath, rec); appendErr != nil {
		fmt.Fprintf(stderr, "%s: warning: telemetry append: %v\n", label, appendErr)
	}

	return rep, true
}

// executeSpec dispatches a validated run spec to a Bench (via dispatchRun) and
// emits the per-OS Report as JSON — the `submit --execute` front door.
func executeSpec(spec runspec.Spec, configPath string, stdout, stderr *os.File) int {
	rep, ok := dispatchRun(spec, configPath, "submit --execute", stderr)
	if !ok {
		return exitInconclusive
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(rep); encErr != nil {
		fmt.Fprintf(stderr, "submit --execute: encode report: %v\n", encErr)
		return exitInconclusive
	}

	if rep.Outcome == report.OutcomePassed {
		return exitAllPass
	}
	return exitSomeFailed
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
