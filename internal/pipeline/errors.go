package pipeline

import (
	"errors"
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// ErrNotImplemented is the Cause of every LegError returned by the
// skeleton. Real implementations will replace it with typed Cause
// errors (ImageVersionMismatch, CopyInError, NpmCIError, ...).
var ErrNotImplemented = errors.New("not implemented")

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

// streamError is the typed Cause for LegError when a streaming leg (NpmCI,
// Build, RunTests) fails. It carries the captured stdout/stderr (B-14: so a
// crash that writes its fatal diagnostic to stdout is visible at Normal
// verbosity), the subprocess exit code, and a Cause for non-exec errors
// (ctx canceled, etc.). Verb identifies the step ("npm ci" / "npm run build" /
// "test runner"); which step failed is also recoverable from the wrapping
// LegError.Leg.
//
// This unified type replaces the former per-leg NpmCIError/BuildError/
// TestRunError, which were structurally identical except for the verb string.
// The 3-way stdout/stderr Error() logic now lives in one place, so the next
// B-14-style capture fix applies once instead of three times.
type streamError struct {
	Verb     string // "npm ci" | "npm run build" | "test runner"
	Stderr   string
	Stdout   string
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *streamError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s failed: %v", e.Verb, e.Cause)
	}
	// B-14 fix: include stdout when it carries diagnostic content.
	if e.Stdout != "" && e.Stderr != "" {
		return fmt.Sprintf("%s failed (exit=%d): %s\n%s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	}
	if e.Stdout != "" {
		return fmt.Sprintf("%s failed (exit=%d): %s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stdout))
	}
	return fmt.Sprintf("%s failed (exit=%d): %s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *streamError) Unwrap() error { return e.Cause }
