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

// ErrNotImplemented is the Cause of every LegError returned by the
// skeleton. Real implementations will replace it with typed Cause
// errors (ImageVersionMismatch, CopyInError, NpmCIError, ...).
var ErrNotImplemented = errors.New("not implemented")

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

// ErrLegSkipped is returned by a leg work function to signal that the leg
// was intentionally skipped (not a failure, not a silent success). runLeg
// recognizes it and emits EventLegSkipped instead of EventLegSuccess. The
// first return value (diagPath) from work is repurposed as a human-readable
// skip reason and surfaced as Event.Detail.
var ErrLegSkipped = errors.New("leg skipped")

// LegError envelopes a per-leg failure. Per ADR-0008, callers use
// errors.As(err, &legErr) to learn which Leg failed and where to
// look for diagnostics, then errors.As(legErr.Cause, &specificErr)
// for leg-specific context.
type LegError struct {
	Leg      Leg
	Cause    error
	DiagPath string // path to captured stderr/logs for this leg, if any
	ExitCode int    // distinct per leg per ADR-0004
}

func (e *LegError) Error() string {
	if e.DiagPath != "" {
		return fmt.Sprintf("pipeline leg %s failed (exit %d): %v (diagnostics: %s)", e.Leg, e.ExitCode, e.Cause, e.DiagPath)
	}
	return fmt.Sprintf("pipeline leg %s failed (exit %d): %v", e.Leg, e.ExitCode, e.Cause)
}

func (e *LegError) Unwrap() error { return e.Cause }

// ImageVersionMismatch reports that the Tester Image's
// sh.gsd-test.image-version label does not match the expected version
// (or the label is missing entirely — Actual is "").
type ImageVersionMismatch struct {
	Bench    string // Bench.Name
	Image    string // ImageID
	Expected string
	Actual   string // "" when the label is absent
}

func (e *ImageVersionMismatch) Error() string {
	if e.Actual == "" {
		return fmt.Sprintf("image %s on bench %s: expected version %q but image has no sh.gsd-test.image-version label", e.Image, e.Bench, e.Expected)
	}
	return fmt.Sprintf("image %s on bench %s: expected version %q, got %q", e.Image, e.Bench, e.Expected, e.Actual)
}

// ImageNotPresentError reports that the Tester Image is not present
// on the Bench. Distinguished from BenchDockerError by docker's
// "No such image" stderr substring.
type ImageNotPresentError struct {
	Bench  string
	Image  string
	Stderr string
}

func (e *ImageNotPresentError) Error() string {
	return fmt.Sprintf("image %s not present on bench %s: %s", e.Image, e.Bench, strings.TrimSpace(e.Stderr))
}

// BenchDockerError is an alias kept for package-local convenience; the
// canonical definition lives in internal/bench.
type BenchDockerError = bench.BenchDockerError

// Per-leg exit codes returned in LegError.ExitCode. Documented table per
// ADR-0004. Values start at 10 to leave room for the top-level aggregator's
// exit codes (0/1/2 per ADR-0009).
const (
	ExitCodeCheckImageVersion = 10
	ExitCodeCopyWorktree      = 11
	ExitCodeStartContainer    = 12
	ExitCodeNpmCI             = 13
	ExitCodeBuild             = 14
	ExitCodeRunTests          = 15
	ExitCodeDrain             = 16
	ExitCodeParse             = 17
)

// legExitCode returns the documented exit code for a given Leg. Maps the
// Leg enum to the corresponding ExitCode* constant. Single source of truth
// for the per-leg exit-code table — wrapper scripts read these values
// from LegError.ExitCode and must remain stable across leg reorders.
func legExitCode(leg Leg) int {
	switch leg {
	case LegCheckImageVersion:
		return ExitCodeCheckImageVersion
	case LegCopyWorktree:
		return ExitCodeCopyWorktree
	case LegStartContainer:
		return ExitCodeStartContainer
	case LegNpmCI:
		return ExitCodeNpmCI
	case LegBuild:
		return ExitCodeBuild
	case LegRunTests:
		return ExitCodeRunTests
	case LegDrain:
		return ExitCodeDrain
	case LegParse:
		return ExitCodeParse
	}
	panic(fmt.Sprintf("legExitCode: unknown Leg %d", leg))
}

// dockerInspect is a package-level variable per ADR-0011 (decision 4).
// Tests swap it for a stub via t.Cleanup. Real implementation delegates
// to dockerexec.Run.
var dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
	const labelFormat = `{{ index .Config.Labels "sh.gsd-test.image-version" }}`
	return dockerexec.Run(ctx, b, []string{"image", "inspect", image, "--format", labelFormat})
}

// EventKind discriminates the variants of Event. Per ADR-0008, the
// Event struct uses optional fields rather than a sealed-interface
// hierarchy for ease of JSON serialization (when the renderer adds
// it).
type EventKind int

const (
	EventLegStart EventKind = iota
	EventLegSuccess
	EventLegFailure
	EventLegSkipped  // leg intentionally skipped (not a failure, not a silent success)
	EventChildOutput // a line of subprocess stdout/stderr
	EventTestPass
	EventTestFail
)

func (k EventKind) String() string {
	switch k {
	case EventLegStart:
		return "leg_start"
	case EventLegSuccess:
		return "leg_success"
	case EventLegFailure:
		return "leg_failure"
	case EventLegSkipped:
		return "leg_skipped"
	case EventChildOutput:
		return "child_output"
	case EventTestPass:
		return "test_pass"
	case EventTestFail:
		return "test_fail"
	}
	return fmt.Sprintf("event(%d)", int(k))
}

// Event is a single emission on the Pipeline's event channel. Per
// ADR-0008, each event is tagged with its OS (from the Pipeline's
// Bench.OS) so parallel pipelines' events interleave legibly in the
// renderer. Unused fields for a given Kind are left zero-valued.
type Event struct {
	Kind EventKind
	OS   string
	Time time.Time

	// Leg is set for Leg* event kinds.
	Leg Leg

	// Line is set for ChildOutput (the subprocess line),
	// TestPass/TestFail (the test name).
	Line string

	// Stream is set for EventChildOutput: "stdout" or "stderr".
	// Empty for all other event kinds. Per ADR-0017 dec 4.
	Stream string

	// Detail is set for LegFailure (error message), TestFail
	// (failure output / stack).
	Detail string
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

// ContainerStartError is the typed Cause for LegError when StartContainer
// fails for image-specific reasons (no such image, bad image reference, etc.).
// Distinct from BenchDockerError which covers transport/daemon failures.
type ContainerStartError struct {
	Image    string
	Stderr   string
	ExitCode int
}

func (e *ContainerStartError) Error() string {
	return fmt.Sprintf("container start failed for image %s (exit=%d): %s",
		e.Image, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// CopyInError is the typed Cause for LegError when the CopyWorktree leg fails.
type CopyInError struct {
	Cause error
}

func (e *CopyInError) Error() string {
	return fmt.Sprintf("copy worktree to container failed: %v", e.Cause)
}

func (e *CopyInError) Unwrap() error { return e.Cause }

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

// DrainError is the typed Cause for LegError when the Drain leg fails.
// Stage discriminates the failure point within the leg.
type DrainError struct {
	// Stage is "create_temp" when os.CreateTemp fails, "docker_cp" when
	// docker cp fails.
	Stage string
	Cause error
}

func (e *DrainError) Error() string {
	return fmt.Sprintf("drain failed at stage %q: %v", e.Stage, e.Cause)
}

func (e *DrainError) Unwrap() error { return e.Cause }

// ParseError is the typed Cause for LegError when the Parse leg fails.
type ParseError struct {
	Cause error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse failed: %v", e.Cause)
}

func (e *ParseError) Unwrap() error { return e.Cause }

// Pipeline executes the 8 per-OS legs against one Bench using one
// Tester Image and one PR-merged worktree. One Pipeline per (Bench,
// OS) per Local Engine run. See ADR-0008 for the shape rationale.
type Pipeline struct {
	bench           bench.Bench
	image           images.ImageID
	expectedVersion string // per ADR-0011 decision 3: caller-supplied
	work            string // path to the PR-merged worktree (from worktree.Worktree.Path())
	events          chan<- Event
	// queue is the unbounded, lossless buffer between emit (producers) and the
	// events channel. A single pump goroutine drains it and is the sole closer
	// of events (#84, ADR-0017 amendment). Nil only when events is nil.
	queue  *eventQueue
	result report.Report
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
// CheckImageVersion (see ADR-0011, decision 3). Events are buffered in an
// unbounded in-pipeline queue and delivered to the events channel by a single
// pump goroutine, so emit never blocks a leg and never drops an event (#84,
// ADR-0017 amendment); the pump is the sole closer of events. Callers must
// drain events; they must NOT close it themselves. A nil events channel makes
// emit a no-op (no pump is started).
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
	// Start the single pump that drains the queue to events and closes it. With
	// no events channel emit is a no-op, so no pump is needed.
	if events != nil {
		go p.pump()
	}
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
		out, err := dockerInspect(ctx, p.bench, string(p.image))
		if err != nil {
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
		actual := strings.TrimSpace(out)
		if actual != p.expectedVersion {
			return "", &ImageVersionMismatch{
				Bench:    p.bench.Name,
				Image:    string(p.image),
				Expected: p.expectedVersion,
				Actual:   actual,
			}
		}
		return "", nil
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

// NpmCIError is the typed Cause for LegError when the NpmCI leg fails.
type NpmCIError struct {
	Stderr   string
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *NpmCIError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("npm ci failed: %v", e.Cause)
	}
	return fmt.Sprintf("npm ci failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *NpmCIError) Unwrap() error { return e.Cause }

// NpmCI runs `npm ci` inside the container.
func (p *Pipeline) NpmCI(ctx context.Context) error {
	return p.runLeg(ctx, LegNpmCI, func(ctx context.Context) (string, error) {
		if p.containerID == "" {
			return "", &NpmCIError{Cause: errors.New("StartContainer did not run; containerID is empty")}
		}
		var stderrBuf bytes.Buffer
		args := []string{"exec", "--workdir", "/work", p.containerID, "npm", "ci"}
		err := dockerStream(ctx, p.bench, args,
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegNpmCI, Line: line, Stream: "stdout"})
			},
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegNpmCI, Line: line, Stream: "stderr"})
				stderrBuf.WriteString(line + "\n")
			},
		)
		if err != nil {
			var execErr *dockerexec.ExecError
			if errors.As(err, &execErr) {
				return "", &NpmCIError{Stderr: stderrBuf.String(), ExitCode: execErr.ExitCode}
			}
			return "", &NpmCIError{Cause: err}
		}
		return "", nil
	})
}

// BuildError is the typed Cause for LegError when the Build leg fails.
type BuildError struct {
	Stderr   string
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *BuildError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("npm run build failed: %v", e.Cause)
	}
	return fmt.Sprintf("npm run build failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *BuildError) Unwrap() error { return e.Cause }

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
			return "", &BuildError{Cause: errors.New("StartContainer did not run; containerID is empty")}
		}
		hasScript, err := p.hasBuildScript(ctx)
		if err != nil {
			return "", &BuildError{Cause: err}
		}
		if !hasScript {
			return "no build script defined in package.json", ErrLegSkipped
		}
		var stderrBuf bytes.Buffer
		args := []string{"exec", "--workdir", "/work", p.containerID, "npm", "run", "build"}
		err = dockerStream(ctx, p.bench, args,
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegBuild, Line: line, Stream: "stdout"})
			},
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegBuild, Line: line, Stream: "stderr"})
				stderrBuf.WriteString(line + "\n")
			},
		)
		if err != nil {
			var execErr *dockerexec.ExecError
			if errors.As(err, &execErr) {
				return "", &BuildError{Stderr: stderrBuf.String(), ExitCode: execErr.ExitCode}
			}
			return "", &BuildError{Cause: err}
		}
		return "", nil
	})
}

// TestRunError is the typed Cause for LegError when the RunTests leg fails
// due to a runner crash (not merely test failures — exit 1 is intentionally
// not a leg error per ADR-0017).
type TestRunError struct {
	Stderr   string
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *TestRunError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("test runner failed: %v", e.Cause)
	}
	return fmt.Sprintf("test runner crashed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *TestRunError) Unwrap() error { return e.Cause }

// RunTests executes the test suite inside the container; the Reporter
// (ADR-0001) emits JSON Lines to a capture file. A JSONL-tail goroutine
// runs concurrently to emit live EventTestPass/EventTestFail events per
// ADR-0017 dec 2. Test-process exit code 1 (tests failed) is NOT a leg
// error — the Parse leg surfaces failures via Report.Failures.
func (p *Pipeline) RunTests(ctx context.Context) error {
	return p.runLeg(ctx, LegRunTests, func(ctx context.Context) (string, error) {
		if p.containerID == "" {
			return "", &TestRunError{Cause: errors.New("StartContainer did not run; containerID is empty")}
		}

		// Start JSONL-tail goroutine. Lives for the duration of RunTests.
		tailCtx, cancelTail := context.WithCancel(ctx)
		defer cancelTail()
		var tailWG sync.WaitGroup
		tailWG.Add(1)
		go p.tailJSONLForLiveEvents(tailCtx, &tailWG)

		// Run the test subprocess. Reporter writes JSONL to containerJSONLPath
		// while the JSONL-tail goroutine emits live test events from it.
		var stderrBuf bytes.Buffer
		args := append([]string{"exec", "--workdir", "/work", p.containerID}, p.runTestsCommandArgs()...)
		err := dockerStream(ctx, p.bench, args,
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegRunTests, Line: line, Stream: "stdout"})
			},
			func(line string) {
				p.emit(Event{Kind: EventChildOutput, Leg: LegRunTests, Line: line, Stream: "stderr"})
				stderrBuf.WriteString(line + "\n")
			},
		)

		// Cancel and wait for the tail goroutine.
		cancelTail()
		tailWG.Wait()

		// exit 1 == "tests failed but runner ran OK" — not a leg error.
		// Only exit > 1 or non-exec errors indicate a runner crash.
		if err != nil {
			var execErr *dockerexec.ExecError
			if errors.As(err, &execErr) {
				if execErr.ExitCode > 1 {
					return "", &TestRunError{Stderr: stderrBuf.String(), ExitCode: execErr.ExitCode}
				}
				// exit 1: tests failed — Parse leg surfaces it via Report.Failures.
				return "", nil
			}
			return "", &TestRunError{Cause: err}
		}
		return "", nil
	})
}

// tailJSONLForLiveEvents tails containerJSONLPath inside the running container
// and emits EventTestPass/EventTestFail per parsed test event. Stops when ctx
// is canceled (which RunTests does after the test subprocess exits). Errors are
// best-effort — failures here don't fail the leg (Parse reads the final file).
func (p *Pipeline) tailJSONLForLiveEvents(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	args := []string{
		"exec", p.containerID,
		"tail", "-f", "-n", "+1", containerJSONLPath,
	}
	_ = dockerStream(ctx, p.bench, args,
		func(line string) {
			if line == "" {
				return
			}
			ev, ok := parseLiveTestEvent([]byte(line))
			if !ok {
				return
			}
			kind := EventTestPass
			if ev.Kind == "fail" {
				kind = EventTestFail
			}
			p.emit(Event{Kind: kind, Leg: LegRunTests, Line: ev.Name, OS: p.bench.OS})
		},
		nil, // ignore stderr from `tail -f`
	)
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

// emit enqueues an Event for delivery to the renderer. It never blocks a leg
// and never drops: the event is appended to an unbounded in-pipeline queue that
// the pump goroutine drains to the events channel (#84, ADR-0017 amendment).
// A nil events channel makes emit a no-op (the no-consumer path).
func (p *Pipeline) emit(e Event) {
	if p.events == nil {
		return
	}
	p.queue.push(e)
}

// closeEvents signals that no more events will be emitted. It is idempotent and
// non-blocking: it only marks the queue closed. The pump then delivers any
// queued events to the consumer and closes the channel — so a caller relying on
// the channel to close MUST keep draining it until then (a stalled consumer
// leaves the pump parked mid-send). Called by RunAll on every exit path. A nil
// events channel makes it a no-op.
func (p *Pipeline) closeEvents() {
	if p.events == nil {
		return
	}
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

// eventQueue is an unbounded, mutex-guarded FIFO drained by one pump goroutine.
// Producers (emit) never block and nothing is dropped; only the pump can block,
// on a slow channel, which is bounded backpressure rather than data loss. This
// replaces the prior non-blocking select-default emit that silently discarded
// events on a full 128-item buffer (#84, ADR-0017 amendment).
type eventQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Event
	closed bool
}

func newEventQueue() *eventQueue {
	q := &eventQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends e. It is a no-op once the queue is closed (no producers after
// close), so a late tail-goroutine emit during shutdown can never panic.
func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.items = append(q.items, e)
	q.mu.Unlock()
	q.cond.Signal()
}

// close marks the queue closed and wakes the pump so it can drain and exit.
// Idempotent.
func (q *eventQueue) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop returns the next event, blocking until one is available. It returns
// ok=false exactly once the queue is both closed and fully drained.
func (q *eventQueue) pop() (Event, bool) {
	q.mu.Lock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		q.mu.Unlock()
		return Event{}, false
	}
	e := q.items[0]
	q.items[0] = Event{} // release the reference so the backing array can shrink
	q.items = q.items[1:]
	q.mu.Unlock()
	return e, true
}
