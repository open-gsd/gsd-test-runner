package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/images"
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

// Report is the per-OS final result of a Pipeline run.
//
// TODO(ADR-0011?): deepening candidate #4 — the Report's shape (fields,
// JSON serialization, renderer-vs-machine consumer split) is an open
// design question. This placeholder allows the Pipeline interface to
// compile and the orchestrator (ADR-0009) to be wired up. Real fields
// land when candidate #4 resolves.
type Report struct{}

// Pipeline executes the 8 per-OS legs against one Bench using one
// Tester Image and one PR-merged worktree. One Pipeline per (Bench,
// OS) per Local Engine run. See ADR-0008 for the shape rationale.
type Pipeline struct {
	bench  bench.Bench
	image  images.ImageID
	work   string // path to the PR-merged worktree (from worktree.Worktree.Path())
	events chan<- Event
	report Report
}

// New constructs a Pipeline. The events channel must be readable by
// some consumer; Pipeline sends are non-blocking (select-with-default),
// so a full or unread channel silently drops events. Real callers
// should buffer generously and drain in a goroutine.
func New(b bench.Bench, img images.ImageID, worktreePath string, events chan<- Event) *Pipeline {
	return &Pipeline{
		bench:  b,
		image:  img,
		work:   worktreePath,
		events: events,
	}
}

// CheckImageVersion verifies the Image-version sentinel on the Bench
// matches what this Local Engine expects (ADR-0001).
func (p *Pipeline) CheckImageVersion(ctx context.Context) error {
	return p.runLegStub(ctx, LegCheckImageVersion)
}

// CopyWorktree copies the PR-merged worktree into a fresh container
// on the Bench (ADR-0002: no bind-mounts).
func (p *Pipeline) CopyWorktree(ctx context.Context) error {
	return p.runLegStub(ctx, LegCopyWorktree)
}

// StartContainer launches the fresh container with the copied
// worktree mounted at the expected in-image path.
func (p *Pipeline) StartContainer(ctx context.Context) error {
	return p.runLegStub(ctx, LegStartContainer)
}

// NpmCI runs `npm ci` inside the container.
func (p *Pipeline) NpmCI(ctx context.Context) error {
	return p.runLegStub(ctx, LegNpmCI)
}

// Build runs the project's build step inside the container.
func (p *Pipeline) Build(ctx context.Context) error {
	return p.runLegStub(ctx, LegBuild)
}

// RunTests executes the test suite inside the container; the Reporter
// (ADR-0001) emits JSON Lines to a capture file.
func (p *Pipeline) RunTests(ctx context.Context) error {
	return p.runLegStub(ctx, LegRunTests)
}

// Drain pulls the JSON Lines capture file from the container to the
// Dev Workstation.
func (p *Pipeline) Drain(ctx context.Context) error {
	return p.runLegStub(ctx, LegDrain)
}

// Parse converts the JSON Lines stream into structured test events
// and aggregates them into the Pipeline's Report. A non-empty JSONL
// file that yields zero parsed events is a Parse failure (ADR-0004:
// not a silent zero-test success).
func (p *Pipeline) Parse(ctx context.Context) error {
	return p.runLegStub(ctx, LegParse)
}

// Report returns the per-OS final result. Only meaningful after
// successful Parse (or after RunAll returns nil).
func (p *Pipeline) Report() Report { return p.report }

// RunAll executes all 8 legs in order, short-circuiting on the first
// LegError. Returns the Report and nil on success, or a zero Report
// and the LegError of the first failed leg.
func (p *Pipeline) RunAll(ctx context.Context) (Report, error) {
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
			return Report{}, err
		}
	}
	return p.report, nil
}

// runLegStub emits a LegStart event then returns a LegError wrapping
// ErrNotImplemented. Real implementations will replace this with the
// actual leg work, emitting LegSuccess on completion or LegFailure
// before returning.
func (p *Pipeline) runLegStub(ctx context.Context, leg Leg) error {
	p.emit(Event{Kind: EventLegStart, OS: p.bench.OS, Time: time.Now(), Leg: leg})

	// Honor context cancellation even in the stub — establishes the
	// pattern future real implementations must follow.
	if err := ctx.Err(); err != nil {
		return &LegError{Leg: leg, Cause: err, ExitCode: int(leg) + 1}
	}

	return &LegError{Leg: leg, Cause: ErrNotImplemented, ExitCode: int(leg) + 1}
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
