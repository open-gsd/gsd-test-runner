package tartpipeline

import (
	"fmt"
	"strings"
)

// VMStartError is the typed Cause for a pipeline.LegError from the
// StartContainer leg: it can fail at clone, write_state (the required
// tartreaper state-file write — see StartContainer's doc comment),
// set-memory, boot (issuing the detached `tart run`), or wait_ip (the
// `tart ip --wait` readiness probe). Stage discriminates which sub-step
// failed.
type VMStartError struct {
	Stage  string // "clone" | "write_state" | "set_memory" | "boot" | "wait_ip"
	VMName string
	Cause  error
}

func (e *VMStartError) Error() string {
	return fmt.Sprintf("tart VM %s start failed at stage %q: %v", e.VMName, e.Stage, e.Cause)
}

func (e *VMStartError) Unwrap() error { return e.Cause }

// SentinelReadError is the typed Cause for a pipeline.LegError from
// CheckImageVersion when reading the in-guest image-version sentinel file
// itself fails (guest unreachable, sentinel missing, etc.) — as opposed to a
// successful read that simply doesn't match (images.ImageVersionMismatch is
// reused for that case; see CheckImageVersion's doc comment for why).
type SentinelReadError struct {
	VMName string
	Cause  error
}

func (e *SentinelReadError) Error() string {
	return fmt.Sprintf("read image-version sentinel on VM %s failed: %v", e.VMName, e.Cause)
}

func (e *SentinelReadError) Unwrap() error { return e.Cause }

// CopyInError is the typed Cause for a pipeline.LegError from the
// CopyWorktree leg (the Workstation->Bench scp, per ADR-0030 Decision 6 —
// distinct from Docker's CopyInError, which copies into a running
// container; this one copies onto the Bench's filesystem before boot).
type CopyInError struct {
	Cause error
}

func (e *CopyInError) Error() string {
	return fmt.Sprintf("copy worktree to bench failed: %v", e.Cause)
}

func (e *CopyInError) Unwrap() error { return e.Cause }

// DrainError is the typed Cause for a pipeline.LegError from the Drain leg.
// Stage is "create_temp" when the local temp file can't be created, "scp"
// when the Bench->Workstation CopyFromBench transfer fails.
type DrainError struct {
	Stage string
	Cause error
}

func (e *DrainError) Error() string {
	return fmt.Sprintf("drain failed at stage %q: %v", e.Stage, e.Cause)
}

func (e *DrainError) Unwrap() error { return e.Cause }

// ParseError is the typed Cause for a pipeline.LegError from the Parse leg.
type ParseError struct {
	Cause error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse failed: %v", e.Cause)
}

func (e *ParseError) Unwrap() error { return e.Cause }

// GuestExecError is the typed Cause for a pipeline.LegError from a streaming
// in-guest leg (NpmCI, Build, RunTests). Mirrors internal/pipeline's
// unexported streamError shape (duplicated here rather than exported from
// internal/pipeline, per the brief's guidance to avoid widening that
// package's public surface for one reuse). Verb identifies the step ("npm
// ci" / "npm run build" / "test runner"); Stdout/Stderr are the full
// captured output (this PR has no live JSONL tail, so RunTests reports its
// output as a single post-hoc EventChildOutput burst — see RunTests's doc
// comment).
type GuestExecError struct {
	Verb     string
	Stdout   string
	Stderr   string
	ExitCode int
	Cause    error // non-nil for non-exec errors (ctx canceled, ssh transport failure, etc.)
}

func (e *GuestExecError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s failed: %v", e.Verb, e.Cause)
	}
	if e.Stdout != "" && e.Stderr != "" {
		return fmt.Sprintf("%s failed (exit=%d): %s\n%s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	}
	if e.Stdout != "" {
		return fmt.Sprintf("%s failed (exit=%d): %s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stdout))
	}
	return fmt.Sprintf("%s failed (exit=%d): %s", e.Verb, e.ExitCode, strings.TrimSpace(e.Stderr))
}

func (e *GuestExecError) Unwrap() error { return e.Cause }
