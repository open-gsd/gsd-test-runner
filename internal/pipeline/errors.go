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

// NpmCIError is the typed Cause for LegError when the NpmCI leg fails.
type NpmCIError struct {
	Stderr   string
	Stdout   string // B-14 fix: captured stdout so crash diagnostics are visible at Normal verbosity
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *NpmCIError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("npm ci failed: %v", e.Cause)
	}
	// B-14 fix: include stdout in the error message when it carries diagnostic
	// content that wouldn't otherwise be visible at Normal/Quiet verbosity.
	if e.Stdout != "" && e.Stderr != "" {
		return fmt.Sprintf("npm ci failed (exit=%d): %s\n%s", e.ExitCode, strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	}
	if e.Stdout != "" {
		return fmt.Sprintf("npm ci failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stdout))
	}
	return fmt.Sprintf("npm ci failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *NpmCIError) Unwrap() error { return e.Cause }

// BuildError is the typed Cause for LegError when the Build leg fails.
type BuildError struct {
	Stderr   string
	Stdout   string // B-14 fix: captured stdout so crash diagnostics are visible at Normal verbosity
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *BuildError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("npm run build failed: %v", e.Cause)
	}
	// B-14 fix: include stdout when it carries diagnostic content.
	if e.Stdout != "" && e.Stderr != "" {
		return fmt.Sprintf("npm run build failed (exit=%d): %s\n%s", e.ExitCode, strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	}
	if e.Stdout != "" {
		return fmt.Sprintf("npm run build failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stdout))
	}
	return fmt.Sprintf("npm run build failed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *BuildError) Unwrap() error { return e.Cause }

// TestRunError is the typed Cause for LegError when the RunTests leg fails
// due to a runner crash (not merely test failures — exit 1 is intentionally
// not a leg error per ADR-0017).
type TestRunError struct {
	Stderr   string
	Stdout   string // B-14 fix: captured stdout so crash diagnostics are visible at Normal verbosity
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, etc.)
}

func (e *TestRunError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("test runner failed: %v", e.Cause)
	}
	// B-14 fix: include stdout when it carries diagnostic content.
	if e.Stdout != "" && e.Stderr != "" {
		return fmt.Sprintf("test runner crashed (exit=%d): %s\n%s", e.ExitCode, strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	}
	if e.Stdout != "" {
		return fmt.Sprintf("test runner crashed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stdout))
	}
	return fmt.Sprintf("test runner crashed (exit=%d): %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *TestRunError) Unwrap() error { return e.Cause }
