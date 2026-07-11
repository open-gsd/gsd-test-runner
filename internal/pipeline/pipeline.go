package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// containerJSONLPath is the path inside the Tester Image container where the
// Reporter writes per-test JSONL events. The Drain leg copies this file to the
// Dev Workstation via docker cp.
const containerJSONLPath = "/work/test-events.jsonl"
const reporterPathPlaceholder = "{{REPORTER_PATH}}"
const reporterDestPlaceholder = "{{REPORTER_DEST}}"
const defaultReporterPath = "/opt/gsd-test/reporter.mjs"

var defaultTestCommandArgs = []string{
	"node",
	"--test",
	"--test-reporter={{REPORTER_PATH}}",
	"--test-reporter-destination={{REPORTER_DEST}}",
}

// Leg names a single step of the Per-OS pipeline. Per ADR-0008 there
// are 8 legs owned by the Executor (the upstream worktree-construction
// legs are owned by package worktree, not by the Pipeline).
type Leg int

const (
	LegCheckImageVersion Leg = iota
	LegCopyWorktree
	LegStartContainer
	LegNpmCI
	LegBuild
	LegRunTests
	LegDrain
	LegParse
)

func (l Leg) String() string {
	switch l {
	case LegCheckImageVersion:
		return "check_image_version"
	case LegCopyWorktree:
		return "copy_worktree"
	case LegStartContainer:
		return "start_container"
	case LegNpmCI:
		return "npm_ci"
	case LegBuild:
		return "build"
	case LegRunTests:
		return "run_tests"
	case LegDrain:
		return "drain"
	case LegParse:
		return "parse"
	}
	return fmt.Sprintf("leg(%d)", int(l))
}

// dockerInspect is a package-level variable per ADR-0011 (decision 4).
// Tests swap it for a stub via t.Cleanup. Real implementation delegates
// to dockerexec.Run.
var dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
	const labelFormat = `{{ index .Config.Labels "sh.gsd-test.image-version" }}`
	return dockerexec.Run(ctx, b, []string{"image", "inspect", image, "--format", labelFormat})
}

// dockerCp is a package-level variable for testability (matches the
// dockerInspect pattern). Tests swap it for a stub via t.Cleanup.
// Real implementation delegates to dockerexec.Run for docker cp operations.
var dockerCp = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
	return dockerexec.Run(ctx, b, append([]string{"cp"}, args...))
}

// dockerRun is a package-level variable for testability (matches the
// dockerInspect and dockerCp pattern). Tests swap it for a stub via t.Cleanup.
// Real implementation delegates to dockerexec.Run for docker run operations.
// Per ADR-0014 decision 3: one stub per docker subcommand.
var dockerRun = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
	return dockerexec.Run(ctx, b, args)
}

// dockerRm is a package-level variable for testability. Tests swap it for a
// stub via t.Cleanup. Real implementation delegates to dockerexec.Run for
// docker rm operations.
var dockerRm = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
	return dockerexec.Run(ctx, b, args)
}

// dockerStream is the test seam for streaming subprocess legs. Per-package
// wrapper var per ADR-0014 dec 3.
var dockerStream = func(ctx context.Context, b bench.Bench, args []string, stdoutLine, stderrLine dockerexec.LineHandler) error {
	return dockerexec.Stream(ctx, b, args, stdoutLine, stderrLine)
}

// isBenchInfraFailure returns true when the ExecError's stderr indicates a
// Bench infrastructure failure (daemon down, SSH refused, permission denied)
// rather than an image-specific failure (no such image, bad tag, etc.).
func isBenchInfraFailure(e *dockerexec.ExecError) bool {
	s := strings.ToLower(e.Stderr)
	return strings.Contains(s, "cannot connect to the docker daemon") ||
		strings.Contains(s, "is the docker daemon running") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "permission denied") ||
		strings.Contains(s, "ssh:") // ssh errors include "ssh: connect to host..."
}

// Pipeline executes the 8 per-OS legs against one Bench using one
// Tester Image and one PR-merged worktree. One Pipeline per (Bench,
// OS) per Local Engine run. See ADR-0008 for the shape rationale.
type Pipeline struct {
	bench           bench.Bench
	image           images.ImageID
	expectedVersion string // per ADR-0011 decision 3: caller-supplied
	work            string // path to the PR-merged worktree (from worktree.Worktree.Path())
	events          chan<- Event
	// queue is the bounded, loss-tolerant buffer between emit (producers) and the
	// events channel. A single pump goroutine drains it and is the sole closer
	// of events (#84, ADR-0017 amendment). Nil only when events is nil.
	queue    *eventQueue
	pumpOnce sync.Once // B-8 fix: start pump lazily on first emit; prevents leak when New is called without RunAll
	result   report.Report
	// containerID is the running container ID/name set by StartContainer.
	// Drain uses it as the source for docker cp. Today StartContainer is a
	// stub; tests seed this field directly.
	containerID string
	// drainedPath is the local temp file path written by Drain and consumed
	// by Parse. Empty until Drain succeeds.
	drainedPath string
	// testCommand is the optional command template for the RunTests leg.
	// Empty means "use defaultTestCommandArgs".
	testCommand []string
}

// New constructs a Pipeline. The expectedVersion parameter is the
// Image-version sentinel value this Pipeline will verify in
// CheckImageVersion (see ADR-0011, decision 3). Events are buffered in a
// bounded in-pipeline queue and delivered to the events channel by a single
// pump goroutine. The pump is started lazily on the first emit call (B-8 fix):
// this ensures that a Pipeline constructed but never used (early return / panic
// before RunAll) does not leak a goroutine parked on cond.Wait. The pump is the
// sole closer of events; callers must drain events and must NOT close it
// themselves. A nil events channel makes emit a no-op (no pump is ever started).
func New(b bench.Bench, img images.ImageID, expectedVersion string, worktreePath string, testCommand []string, events chan<- Event) *Pipeline {
	p := &Pipeline{
		bench:           b,
		image:           img,
		expectedVersion: expectedVersion,
		work:            worktreePath,
		testCommand:     testCommand,
		events:          events,
		queue:           newEventQueue(),
		result:          report.New(b.OS, b.Name, string(img), expectedVersion, time.Now().UTC()),
	}
	// Pump is NOT started here — see emit for the lazy start (B-8 fix).
	return p
}

func (p *Pipeline) runTestsCommandArgs() []string {
	command := p.testCommand
	if len(command) == 0 {
		command = defaultTestCommandArgs
	}
	replacer := strings.NewReplacer(
		reporterPathPlaceholder, defaultReporterPath,
		reporterDestPlaceholder, containerJSONLPath,
	)
	args := make([]string, len(command))
	for i, part := range command {
		args[i] = replacer.Replace(part)
	}
	return args
}

// CheckImageVersion verifies the Image-version sentinel on the Bench
// matches what this Local Engine expects (ADRs 0001, 0011). The
// sentinel is an OCI label `sh.gsd-test.image-version` on the Tester
// Image; this leg reads it via `docker image inspect`.
func (p *Pipeline) CheckImageVersion(ctx context.Context) error {
	return p.runLeg(ctx, LegCheckImageVersion, p.checkImageVersionWork(ctx))
}

func (p *Pipeline) checkImageVersionWork(ctx context.Context) func(context.Context) (string, error) {
	return func(_ context.Context) (string, error) {
		// Adapter over the package-level dockerInspect test seam. images.VerifyImageVersion
		// passes its own --format (which extracts the version label), but we reuse
		// dockerInspect's already-formatted result so the existing stubDocker test seam
		// keeps working — both extract the same sh.gsd-test.image-version label.
		runner := reaper.Runner(func(ctx context.Context, _ ...string) ([]byte, error) {
			out, err := dockerInspect(ctx, p.bench, string(p.image))
			return []byte(out), err
		})
		err := images.VerifyImageVersion(ctx, runner, string(p.image), p.expectedVersion)
		if err == nil {
			return "", nil
		}
		// Version mismatch — attach bench context for diagnostics and pass through.
		var mm *images.ImageVersionMismatch
		if errors.As(err, &mm) {
			mm.Bench = p.bench.Name
			return "", mm
		}
		// Inspect failure — classify into typed errors (fail-loud, ADR-0004).
		var de *dockerexec.ExecError
		if errors.As(err, &de) {
			if strings.Contains(de.Stderr, "No such image") {
				return "", &ImageNotPresentError{
					Bench:  p.bench.Name,
					Image:  string(p.image),
					Stderr: de.Stderr,
				}
			}
			return "", &bench.BenchDockerError{
				Bench:    p.bench.Name,
				Args:     de.Args,
				Stderr:   de.Stderr,
				ExitCode: de.ExitCode,
			}
		}
		return "", err
	}
}

// CopyWorktree copies the PR-merged worktree into the running container
// on the Bench using `docker cp` (ADR-0002: no bind-mounts). StartContainer
// must have run first (p.containerID must be non-empty). The trailing /.
// on the source path copies directory contents into /work, preserving dotfiles.
func (p *Pipeline) CopyWorktree(ctx context.Context) error {
	return p.runLeg(ctx, LegCopyWorktree, func(_ context.Context) (string, error) {
		if p.containerID == "" {
			return "", &CopyInError{Cause: errors.New("StartContainer did not run; containerID is empty")}
		}
		if p.work == "" {
			return "", &CopyInError{Cause: errors.New("worktreePath is empty")}
		}
		// Trailing /. copies directory contents into /work, preserving dotfiles.
		src := p.work + "/."
		dst := p.containerID + ":/work"
		_, err := dockerCp(ctx, p.bench, []string{src, dst})
		if err != nil {
			return "", &CopyInError{Cause: err}
		}
		return "", nil
	})
}

// StartContainer launches a fresh idle container on the Bench from the
// Tester Image. The container runs `sleep infinity` so subsequent legs
// can docker exec into it. --rm ensures docker removes it on stop, and
// RunAll additionally defers a `docker rm -f` to handle the running case.
func (p *Pipeline) StartContainer(ctx context.Context) error {
	return p.runLeg(ctx, LegStartContainer, func(_ context.Context) (string, error) {
		imageRef := string(p.image)
		args := []string{"run", "--rm", "-d", "--workdir", "/work"}
		if p.bench.Platform != "" {
			args = append(args, "--platform", p.bench.Platform)
		}
		args = append(args, imageRef, "sleep", "infinity")
		stdout, err := dockerRun(ctx, p.bench, args)
		if err != nil {
			var execErr *dockerexec.ExecError
			if errors.As(err, &execErr) {
				if isBenchInfraFailure(execErr) {
					return "", &BenchDockerError{
						Bench:    p.bench.Name,
						Args:     execErr.Args,
						Stderr:   execErr.Stderr,
						ExitCode: execErr.ExitCode,
					}
				}
				return "", &ContainerStartError{
					Image:    imageRef,
					Stderr:   execErr.Stderr,
					ExitCode: execErr.ExitCode,
				}
			}
			return "", err
		}
		p.containerID = strings.TrimSpace(stdout)
		return "", nil
	})
}

// NpmCI runs `npm ci` inside the container.
func (p *Pipeline) NpmCI(ctx context.Context) error {
	return p.runLeg(ctx, LegNpmCI, func(ctx context.Context) (string, error) {
		if p.containerID == "" {
			return "", &streamError{Verb: "npm ci", Cause: errors.New("StartContainer did not run; containerID is empty")}
		}
		args := []string{"exec", "--workdir", "/work", p.containerID, "npm", "ci"}
		return "", p.streamAndCapture(ctx, LegNpmCI, "npm ci", args)
	})
}

// hasBuildScript probes /work/package.json inside the container and reports
// whether a "build" script is defined under "scripts". A missing or
// unreadable package.json is a hard error (NpmCI would have already
// failed). An empty "scripts" object or missing "build" key returns
// (false, nil).
func (p *Pipeline) hasBuildScript(ctx context.Context) (bool, error) {
	args := []string{"exec", "--workdir", "/work", p.containerID, "cat", "package.json"}
	out, err := dockerRun(ctx, p.bench, args)
	if err != nil {
		return false, fmt.Errorf("read package.json: %w", err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(out), &pkg); err != nil {
		return false, fmt.Errorf("parse package.json: %w", err)
	}
	_, ok := pkg.Scripts["build"]
	return ok, nil
}

// Build runs the project's build step inside the container.
func (p *Pipeline) Build(ctx context.Context) error {
	return p.runLeg(ctx, LegBuild, func(ctx context.Context) (string, error) {
		if p.containerID == "" {
			return "", &streamError{Verb: "npm run build", Cause: errors.New("StartContainer did not run; containerID is empty")}
		}
		hasScript, err := p.hasBuildScript(ctx)
		if err != nil {
			return "", &streamError{Verb: "npm run build", Cause: err}
		}
		if !hasScript {
			return "no build script defined in package.json", ErrLegSkipped
		}
		args := []string{"exec", "--workdir", "/work", p.containerID, "npm", "run", "build"}
		return "", p.streamAndCapture(ctx, LegBuild, "npm run build", args)
	})
}

// RunTests executes the test suite inside the container; the Reporter
// (ADR-0001) emits JSON Lines to a capture file. A JSONL-tail goroutine
// runs concurrently to emit live EventTestPass/EventTestFail events per
// ADR-0017 dec 2. Test-process exit code 1 (tests failed) is NOT a leg
// error — the Parse leg surfaces failures via Report.Failures.
func (p *Pipeline) RunTests(ctx context.Context) error {
	return p.runLeg(ctx, LegRunTests, func(ctx context.Context) (string, error) {
		if p.containerID == "" {
			return "", &streamError{Verb: "test runner", Cause: errors.New("StartContainer did not run; containerID is empty")}
		}

		// Start JSONL-tail goroutine. Lives for the duration of RunTests.
		tailCtx, cancelTail := context.WithCancel(ctx)
		defer cancelTail()
		var tailWG sync.WaitGroup
		tailWG.Add(1)
		go p.tailJSONLForLiveEvents(tailCtx, &tailWG)

		// Run the test subprocess. Reporter writes JSONL to containerJSONLPath
		// while the JSONL-tail goroutine emits live test events from it.
		args := append([]string{"exec", "--workdir", "/work", p.containerID}, p.runTestsCommandArgs()...)
		runErr := p.streamAndCapture(ctx, LegRunTests, "test runner", args)

		// Cancel and wait for the tail goroutine.
		cancelTail()
		tailWG.Wait()

		// exit 1 == "tests failed but runner ran OK" — not a leg error (ADR-0017).
		// Only exit > 1 or non-exec errors indicate a runner crash. streamAndCapture
		// wraps any failure into *streamError; downgrade an exit-1 to nil here so
		// the Parse leg surfaces test failures via Report.Failures instead.
		if runErr != nil {
			var se *streamError
			if errors.As(runErr, &se) && se.ExitCode == 1 {
				return "", nil
			}
			return "", runErr
		}
		return "", nil
	})
}

// streamAndCapture runs a streaming docker exec: it emits a live
// EventChildOutput per stdout/stderr line while buffering both for the error
// envelope, then returns nil on success or a *streamError on failure. On an
// ExecError the captured stdout/stderr + exit code are carried (B-14: so a
// crash that writes its fatal diagnostic to stdout is visible at Normal
// verbosity); any other error becomes the streamError's Cause.
//
// This is the shared streaming+capture+wrap mechanic for the NpmCI, Build, and
// RunTests legs. RunTests additionally downgrades an exit-1 streamError to nil
// (tests failed is not a leg error per ADR-0017) at its call site.
func (p *Pipeline) streamAndCapture(ctx context.Context, leg Leg, verb string, args []string) error {
	var stderrBuf, stdoutBuf bytes.Buffer
	err := dockerStream(ctx, p.bench, args,
		func(line string) {
			p.emit(Event{Kind: EventChildOutput, Leg: leg, Line: line, Stream: "stdout"})
			stdoutBuf.WriteString(line + "\n")
		},
		func(line string) {
			p.emit(Event{Kind: EventChildOutput, Leg: leg, Line: line, Stream: "stderr"})
			stderrBuf.WriteString(line + "\n")
		},
	)
	if err != nil {
		var execErr *dockerexec.ExecError
		if errors.As(err, &execErr) {
			return &streamError{Verb: verb, Stderr: stderrBuf.String(), Stdout: stdoutBuf.String(), ExitCode: execErr.ExitCode}
		}
		return &streamError{Verb: verb, Cause: err}
	}
	return nil
}

// tailJSONLForLiveEvents tails containerJSONLPath inside the running container
// and emits EventTestPass/EventTestFail per parsed test event. Stops when ctx
// is canceled (which RunTests does after the test subprocess exits). Errors are
// best-effort — failures here don't fail the leg (Parse reads the final file),
// but are logged via a synthetic EventChildOutput so they are visible in verbose
// mode rather than silently discarded.
//
// B-6 fix: uses "tail -F" (capital F / --retry) instead of "-f" so that tail
// waits for the JSONL file to appear rather than exiting immediately when the
// file does not yet exist at the start of the leg. The dockerStream error is
// no longer discarded — non-cancellation errors are surfaced as a diagnostic event.
func (p *Pipeline) tailJSONLForLiveEvents(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	args := []string{
		"exec", p.containerID,
		"tail", "-F", "-n", "+1", containerJSONLPath,
	}
	err := dockerStream(ctx, p.bench, args,
		func(line string) {
			if line == "" {
				return
			}
			ev, ok := parseLiveTestEvent([]byte(line))
			if !ok {
				return
			}
			e := Event{Kind: EventTestPass, Leg: LegRunTests, Line: ev.Name, File: ev.File, OS: p.bench.OS}
			if ev.Kind == "fail" {
				// Carry the evidence for the real-time failure line (Option I).
				e.Kind = EventTestFail
				e.ErrorClass = ev.ErrorClass
				e.FailLine = ev.Line
				e.Detail = ev.Error
			}
			p.emit(e)
		},
		nil, // ignore stderr from tail
	)
	// B-6 fix: surface non-cancellation errors so they are visible in verbose
	// mode. Context cancellation is the expected exit path (RunTests cancels the
	// tail goroutine's context after the main test exec returns), so we ignore it.
	if err != nil && ctx.Err() == nil {
		p.emit(Event{
			Kind:   EventChildOutput,
			Leg:    LegRunTests,
			Stream: "stderr",
			Line:   fmt.Sprintf("[tail-jsonl] %v", err),
			OS:     p.bench.OS,
		})
	}
}

// Drain pulls the JSON Lines capture file from the container to the
// Dev Workstation via docker cp. It stores the local temp file path on
// p.drainedPath for the Parse leg to consume. On docker cp failure after
// the temp file is created, the local path is returned as diagPath so the
// caller can inspect partial data; the file is left on disk for diagnosis.
func (p *Pipeline) Drain(ctx context.Context) error {
	return p.runLeg(ctx, LegDrain, func(_ context.Context) (string, error) {
		f, err := os.CreateTemp("", "gsd-test-jsonl-*.log")
		if err != nil {
			return "", &DrainError{Stage: "create_temp", Cause: err}
		}
		localPath := f.Name()
		f.Close() // we want the path only; docker cp will overwrite

		src := p.containerID + ":" + containerJSONLPath
		_, err = dockerCp(ctx, p.bench, []string{src, localPath})
		if err != nil {
			// Return localPath as diagPath: partial data may exist on disk.
			return localPath, &DrainError{Stage: "docker_cp", Cause: err}
		}

		p.drainedPath = localPath
		return "", nil
	})
}

// Parse converts the JSON Lines stream into structured test events
// and aggregates them into the Pipeline's Report. A non-empty JSONL
// file that yields zero parsed events is a Parse failure (ADR-0004:
// not a silent zero-test success).
func (p *Pipeline) Parse(ctx context.Context) error {
	return p.runLeg(ctx, LegParse, func(_ context.Context) (string, error) {
		if p.drainedPath == "" {
			return "", &ParseError{Cause: errors.New("drain leg did not run or produced no file")}
		}
		f, err := os.Open(p.drainedPath)
		if err != nil {
			return "", &ParseError{Cause: err}
		}
		defer f.Close()

		passed, total, failures, err := parseJSONL(f)
		if err != nil {
			return "", &ParseError{Cause: err}
		}

		p.result.Total = total
		p.result.Passed = passed
		p.result.Failed = len(failures)
		p.result.Failures = failures
		return "", nil
	})
}

// Report returns the per-OS final result. Only meaningful after
// successful Parse (or after RunAll returns nil).
func (p *Pipeline) Report() report.Report { return p.result }

// DrainedPath returns the local path of the JSONL capture file pulled from the
// container by Drain (empty until Drain succeeds). The orchestrator copies it
// into the per-run artifact dir so the full per-test detail is always persisted
// (Option B, #84).
func (p *Pipeline) DrainedPath() string { return p.drainedPath }

// Cleanup force-removes the running container if StartContainer ran
// successfully. Called by RunAll via defer; safe to call with an empty
// containerID (no-op). Cleanup errors are intentionally discarded — the
// container was started with --rm so it will be removed when it stops
// naturally; docker rm -f handles the running case.
func (p *Pipeline) Cleanup(ctx context.Context) {
	if p.containerID != "" {
		_, _ = dockerRm(ctx, p.bench, []string{"rm", "-f", p.containerID})
	}
}

// RunAll executes all 8 legs in order, short-circuiting on the first
// LegError. Returns the Report and nil on success, or a zero Report
// and the LegError of the first failed leg. Defers container cleanup so
// the idle container started by StartContainer is removed even on failure.
func (p *Pipeline) RunAll(ctx context.Context) (report.Report, error) {
	// Deferred so the event stream is flushed and closed on EVERY exit path —
	// normal return, leg error, or a panic — otherwise the renderer's range over
	// events never ends and r.Wait() hangs (#84).
	defer p.closeEvents()
	// Defer cleanup before legs run so it fires even if a leg panics or fails.
	// context.Background() is used so cleanup runs even after ctx is canceled.
	defer func() {
		if p.containerID != "" {
			_, _ = dockerRm(context.Background(), p.bench, []string{"rm", "-f", p.containerID})
		}
	}()

	legs := []func(context.Context) error{
		p.CheckImageVersion,
		p.StartContainer,
		p.CopyWorktree,
		p.NpmCI,
		p.Build,
		p.RunTests,
		p.Drain,
		p.Parse,
	}
	for _, run := range legs {
		if err := run(ctx); err != nil {
			return report.Report{}, err
		}
	}
	p.result.Finalize(time.Now().UTC())
	return p.result, nil
}

// runLeg orchestrates the LegStart/ctx-check/work/LegSuccess/LegFailure
// protocol every leg must follow per ADR-0008. The work function does
// the leg-specific subprocess work and returns a diagPath (path to a
// captured stderr/log file for this leg, if any) and an error. If work
// returns nil error, a LegSuccess event is emitted and runLeg returns nil.
// If work returns an error, the error is wrapped in *LegError (unless
// already wrapped) with DiagPath populated from the returned diagPath, a
// LegFailure event is emitted, and the *LegError is returned.
func (p *Pipeline) runLeg(ctx context.Context, leg Leg, work func(context.Context) (string, error)) error {
	p.emit(Event{Kind: EventLegStart, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	if err := ctx.Err(); err != nil {
		legErr := &LegError{Leg: leg, Cause: err, ExitCode: legExitCode(leg)}
		p.emit(Event{Kind: EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return legErr
	}
	diagPath, err := work(ctx)
	if errors.Is(err, ErrLegSkipped) {
		p.emit(Event{Kind: EventLegSkipped, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: diagPath})
		return nil
	}
	if err != nil {
		var legErr *LegError
		if !errors.As(err, &legErr) {
			legErr = &LegError{Leg: leg, Cause: err, ExitCode: legExitCode(leg), DiagPath: diagPath}
			err = legErr
		}
		p.emit(Event{Kind: EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return err
	}
	p.emit(Event{Kind: EventLegSuccess, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	return nil
}

// emit enqueues an Event for delivery to the renderer. It never blocks a leg;
// the bounded in-pipeline queue handles backpressure (dropping oldest
// EventChildOutput under a slow consumer — see eventQueue). The pump goroutine
// is started lazily on the first emit call so a Pipeline that is constructed
// but never used (New without RunAll) does not leak a goroutine (B-8 fix).
// A nil events channel makes emit a no-op (the no-consumer path).
func (p *Pipeline) emit(e Event) {
	if p.events == nil {
		return
	}
	// B-8 fix: start the pump goroutine lazily on the first emit, not in New.
	// pumpOnce ensures exactly one pump regardless of concurrent emits.
	p.pumpOnce.Do(func() { go p.pump() })
	p.queue.push(e)
}

// closeEvents signals that no more events will be emitted. It is idempotent and
// non-blocking: it only marks the queue closed. The pump then delivers any
// queued events to the consumer and closes the channel — so a caller relying on
// the channel to close MUST keep draining it until then (a stalled consumer
// leaves the pump parked mid-send). Called by RunAll on every exit path. A nil
// events channel makes it a no-op.
//
// B-8 fix: also ensures the pump goroutine is started (if it hasn't been
// started via emit yet), so a Pipeline that is created and immediately closed
// without any leg running still closes the events channel as expected.
func (p *Pipeline) closeEvents() {
	if p.events == nil {
		return
	}
	// Ensure the pump is running before we close the queue, so the pump can
	// drain any queued events and close the channel. This is the B-8 fix for
	// the case where New is called but RunAll never runs (or panics before emit).
	p.pumpOnce.Do(func() { go p.pump() })
	p.queue.close()
}

// pump is the single goroutine that drains the unbounded queue to the events
// channel in FIFO order and closes the channel once the queue is closed and
// fully drained. It is the sole closer of p.events. RunAll never waits on it,
// so a slow or absent consumer can never block a leg or RunAll; with no
// consumer the pump simply parks on the channel send until the process exits.
func (p *Pipeline) pump() {
	defer close(p.events)
	for {
		e, ok := p.queue.pop()
		if !ok {
			return
		}
		p.events <- e
	}
}
