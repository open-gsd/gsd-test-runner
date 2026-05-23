package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

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

// BenchDockerError reports a docker invocation failure that is NOT
// "image not present" — Bench unreachable, docker daemon down, etc.
type BenchDockerError struct {
	Bench    string
	Args     []string
	Stderr   string
	ExitCode int
}

func (e *BenchDockerError) Error() string {
	return fmt.Sprintf("docker on bench %s failed (exit %d): %s", e.Bench, e.ExitCode, strings.TrimSpace(e.Stderr))
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
	EventChildOutput // a line of subprocess stdout/stderr
	EventTestPass
	EventTestFail
	EventReport
)

func (k EventKind) String() string {
	switch k {
	case EventLegStart:
		return "leg_start"
	case EventLegSuccess:
		return "leg_success"
	case EventLegFailure:
		return "leg_failure"
	case EventChildOutput:
		return "child_output"
	case EventTestPass:
		return "test_pass"
	case EventTestFail:
		return "test_fail"
	case EventReport:
		return "report"
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

	// Detail is set for LegFailure (error message), TestFail
	// (failure output / stack).
	Detail string
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
	result          report.Report
}

// New constructs a Pipeline. The expectedVersion parameter is the
// Image-version sentinel value this Pipeline will verify in
// CheckImageVersion (see ADR-0011, decision 3). The events channel
// must be readable by some consumer; Pipeline sends are non-blocking
// (select-with-default), so a full or unread channel silently drops
// events. Real callers should buffer generously and drain in a
// goroutine.
func New(b bench.Bench, img images.ImageID, expectedVersion string, worktreePath string, events chan<- Event) *Pipeline {
	return &Pipeline{
		bench:           b,
		image:           img,
		expectedVersion: expectedVersion,
		work:            worktreePath,
		events:          events,
		result:          report.New(b.OS, b.Name, string(img), expectedVersion, time.Now().UTC()),
	}
}

// CheckImageVersion verifies the Image-version sentinel on the Bench
// matches what this Local Engine expects (ADRs 0001, 0011). The
// sentinel is an OCI label `sh.gsd-test.image-version` on the Tester
// Image; this leg reads it via `docker image inspect`.
func (p *Pipeline) CheckImageVersion(ctx context.Context) error {
	return p.runLeg(ctx, LegCheckImageVersion, p.checkImageVersionWork(ctx))
}

func (p *Pipeline) checkImageVersionWork(ctx context.Context) func() error {
	return func() error {
		out, err := dockerInspect(ctx, p.bench, string(p.image))
		if err != nil {
			var de *dockerexec.ExecError
			if errors.As(err, &de) {
				if strings.Contains(de.Stderr, "No such image") {
					return &ImageNotPresentError{
						Bench:  p.bench.Name,
						Image:  string(p.image),
						Stderr: de.Stderr,
					}
				}
				return &BenchDockerError{
					Bench:    p.bench.Name,
					Args:     de.Args,
					Stderr:   de.Stderr,
					ExitCode: de.ExitCode,
				}
			}
			return err
		}
		actual := strings.TrimSpace(out)
		if actual != p.expectedVersion {
			return &ImageVersionMismatch{
				Bench:    p.bench.Name,
				Image:    string(p.image),
				Expected: p.expectedVersion,
				Actual:   actual,
			}
		}
		return nil
	}
}

// CopyWorktree copies the PR-merged worktree into a fresh container
// on the Bench (ADR-0002: no bind-mounts).
func (p *Pipeline) CopyWorktree(ctx context.Context) error {
	return p.runLeg(ctx, LegCopyWorktree, func() error { return ErrNotImplemented })
}

// StartContainer launches the fresh container with the copied
// worktree mounted at the expected in-image path.
func (p *Pipeline) StartContainer(ctx context.Context) error {
	return p.runLeg(ctx, LegStartContainer, func() error { return ErrNotImplemented })
}

// NpmCI runs `npm ci` inside the container.
func (p *Pipeline) NpmCI(ctx context.Context) error {
	return p.runLeg(ctx, LegNpmCI, func() error { return ErrNotImplemented })
}

// Build runs the project's build step inside the container.
func (p *Pipeline) Build(ctx context.Context) error {
	return p.runLeg(ctx, LegBuild, func() error { return ErrNotImplemented })
}

// RunTests executes the test suite inside the container; the Reporter
// (ADR-0001) emits JSON Lines to a capture file.
func (p *Pipeline) RunTests(ctx context.Context) error {
	return p.runLeg(ctx, LegRunTests, func() error { return ErrNotImplemented })
}

// Drain pulls the JSON Lines capture file from the container to the
// Dev Workstation.
func (p *Pipeline) Drain(ctx context.Context) error {
	return p.runLeg(ctx, LegDrain, func() error { return ErrNotImplemented })
}

// Parse converts the JSON Lines stream into structured test events
// and aggregates them into the Pipeline's Report. A non-empty JSONL
// file that yields zero parsed events is a Parse failure (ADR-0004:
// not a silent zero-test success).
func (p *Pipeline) Parse(ctx context.Context) error {
	return p.runLeg(ctx, LegParse, func() error { return ErrNotImplemented })
}

// Report returns the per-OS final result. Only meaningful after
// successful Parse (or after RunAll returns nil).
func (p *Pipeline) Report() report.Report { return p.result }

// RunAll executes all 8 legs in order, short-circuiting on the first
// LegError. Returns the Report and nil on success, or a zero Report
// and the LegError of the first failed leg.
func (p *Pipeline) RunAll(ctx context.Context) (report.Report, error) {
	legs := []func(context.Context) error{
		p.CheckImageVersion,
		p.CopyWorktree,
		p.StartContainer,
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
// the leg-specific subprocess work. If work returns nil, a LegSuccess
// event is emitted and runLeg returns nil. If work returns an error,
// the error is wrapped in *LegError (unless already wrapped), a
// LegFailure event is emitted, and the *LegError is returned.
func (p *Pipeline) runLeg(ctx context.Context, leg Leg, work func() error) error {
	p.emit(Event{Kind: EventLegStart, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	if err := ctx.Err(); err != nil {
		legErr := &LegError{Leg: leg, Cause: err, ExitCode: int(leg) + 1}
		p.emit(Event{Kind: EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return legErr
	}
	if err := work(); err != nil {
		var legErr *LegError
		if !errors.As(err, &legErr) {
			legErr = &LegError{Leg: leg, Cause: err, ExitCode: int(leg) + 1}
			err = legErr
		}
		p.emit(Event{Kind: EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return err
	}
	p.emit(Event{Kind: EventLegSuccess, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	return nil
}

// emit sends an Event non-blockingly. If the channel is full or has
// no consumer, the event is silently dropped. Real consumers should
// buffer generously and drain in a goroutine.
func (p *Pipeline) emit(e Event) {
	if p.events == nil {
		return
	}
	select {
	case p.events <- e:
	default:
	}
}
