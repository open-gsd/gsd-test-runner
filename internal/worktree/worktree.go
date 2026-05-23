package worktree

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Options configures a PR-merged worktree construction. All fields except
// ScratchDir are required.
type Options struct {
	// SourceRepo is the path to the developer's local git repo (the one
	// the Local Engine is being invoked from). Must contain BaseSHA and
	// PRSHA as objects reachable in the repo.
	SourceRepo string

	// BaseSHA is the full 40-character commit SHA of the target branch the
	// PR merges into. Resolved from a symbolic ref via internal/refs before
	// calling Construct — see docs/adr/0010-prref-resolution-contract.md.
	BaseSHA string

	// PRSHA is the full 40-character commit SHA to merge into BaseSHA.
	// Resolved from a symbolic ref via internal/refs before calling
	// Construct — see docs/adr/0010-prref-resolution-contract.md.
	PRSHA string

	// ScratchDir is the parent directory under which the scratch clone
	// is created. If empty, os.TempDir() is used. The actual clone goes
	// into a uniquely-named subdirectory under ScratchDir so multiple
	// concurrent Local Engine invocations do not collide.
	ScratchDir string
}

// Worktree is a handle to a constructed PR-merged worktree. Callers must
// Close it when done to remove the scratch directory.
type Worktree struct {
	path   string
	mu     sync.Mutex
	closed bool
}

// Path returns the absolute path to the worktree on disk.
func (w *Worktree) Path() string { return w.path }

// Close removes the scratch directory. Idempotent.
func (w *Worktree) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.path == "" {
		return nil
	}
	return os.RemoveAll(w.path)
}

// Compile-time check.
var _ io.Closer = (*Worktree)(nil)

// Stage names the leg of construction that failed.
type Stage int

const (
	StageValidate Stage = iota // pre-flight: options non-empty, SourceRepo is a git repo
	StageClone                 // git clone --local <SourceRepo> <scratch>
	StageCheckout              // git -C <scratch> checkout <BaseRef>
	StageMerge                 // git -C <scratch> merge --no-edit <PRRef>
)

func (s Stage) String() string {
	switch s {
	case StageValidate:
		return "validate"
	case StageClone:
		return "clone"
	case StageCheckout:
		return "checkout"
	case StageMerge:
		return "merge"
	}
	return fmt.Sprintf("stage(%d)", int(s))
}

// ConstructError envelopes a worktree-construction failure. Callers use
// errors.As to learn which Stage failed and the unwrapped Cause for
// stage-specific context.
type ConstructError struct {
	Stage Stage
	Cause error
	// CleanupErr is set if Construct attempted to remove a partial
	// scratch directory and that removal also failed. Non-fatal — the
	// primary Cause still drives the failure — but exposed for
	// diagnostics.
	CleanupErr error
}

func (e *ConstructError) Error() string {
	if e.CleanupErr != nil {
		return fmt.Sprintf("worktree construction failed at %s: %v (cleanup also failed: %v)", e.Stage, e.Cause, e.CleanupErr)
	}
	return fmt.Sprintf("worktree construction failed at %s: %v", e.Stage, e.Cause)
}

func (e *ConstructError) Unwrap() error { return e.Cause }

// Typed Causes — one per real failure mode. Callers use errors.As against
// these for rich, stage-specific context.

// InvalidOptionsError reports a pre-flight failure: required Options
// field missing or SourceRepo is not a git repo.
type InvalidOptionsError struct {
	Field  string // "SourceRepo", "BaseSHA", "PRSHA", or "SourceRepo (not a git repo)"
	Detail string
}

func (e *InvalidOptionsError) Error() string {
	return fmt.Sprintf("invalid option %s: %s", e.Field, e.Detail)
}

// CloneError reports that `git clone --local` failed.
type CloneError struct {
	SourceRepo string
	ScratchDir string
	ExitCode   int
	Stderr     string
}

func (e *CloneError) Error() string {
	return fmt.Sprintf("git clone --local %s %s failed (exit %d): %s", e.SourceRepo, e.ScratchDir, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// CheckoutError reports that `git checkout <BaseSHA>` failed in the
// scratch clone. Typically means BaseSHA is not a valid object in the
// source repo.
type CheckoutError struct {
	SHA      string
	ExitCode int
	Stderr   string
}

func (e *CheckoutError) Error() string {
	return fmt.Sprintf("git checkout %s failed (exit %d): %s", e.SHA, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// MergeConflictError reports that `git merge <PRSHA>` produced conflicts.
// Files lists the conflicting paths (relative to the worktree root).
type MergeConflictError struct {
	BaseSHA string
	PRSHA   string
	Files   []string
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merge of %s into %s produced %d conflicting file(s): %s", e.PRSHA, e.BaseSHA, len(e.Files), strings.Join(e.Files, ", "))
}

// MergeError reports a non-conflict merge failure (e.g., PRSHA does not
// exist as a reachable object, merge aborted for an unexpected reason).
type MergeError struct {
	BaseSHA  string
	PRSHA    string
	ExitCode int
	Stderr   string
}

func (e *MergeError) Error() string {
	return fmt.Sprintf("git merge %s into %s failed (exit %d): %s", e.PRSHA, e.BaseSHA, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// runGit runs git with the given args. Captures both stdout and stderr.
// Returns exit code, captured streams, and any non-exit error (e.g., binary
// not found). For non-zero git exit, exitCode is set but err is nil — the
// caller interprets the exit code. Honors ctx cancellation: if ctx is
// cancelled (pre or mid-exec), returns ctx.Err() directly per the
// ADR-0014 dec 4 subprocess-cancellation convention.
func runGit(ctx context.Context, dir string, args ...string) (exitCode int, stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if ctx.Err() != nil {
		return -1, stdout, stderr, ctx.Err()
	}

	if runErr == nil {
		return 0, stdout, stderr, nil
	}

	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout, stderr, nil
	}

	// Non-exit error: binary not found, etc.
	return -1, stdout, stderr, runErr
}

// Construct builds a PR-merged worktree. On success the returned
// *Worktree must be Closed by the caller. On failure the returned error
// is always a *ConstructError; any partial scratch directory is removed
// before the error is returned (and any cleanup failure is recorded in
// ConstructError.CleanupErr).
func Construct(ctx context.Context, opts Options) (*Worktree, error) {
	// 1. Validate options.
	if opts.SourceRepo == "" {
		return nil, &ConstructError{
			Stage: StageValidate,
			Cause: &InvalidOptionsError{Field: "SourceRepo", Detail: "must not be empty"},
		}
	}
	if opts.BaseSHA == "" {
		return nil, &ConstructError{
			Stage: StageValidate,
			Cause: &InvalidOptionsError{Field: "BaseSHA", Detail: "must not be empty"},
		}
	}
	if opts.PRSHA == "" {
		return nil, &ConstructError{
			Stage: StageValidate,
			Cause: &InvalidOptionsError{Field: "PRSHA", Detail: "must not be empty"},
		}
	}

	// Check SourceRepo exists.
	if _, statErr := os.Stat(opts.SourceRepo); statErr != nil {
		return nil, &ConstructError{
			Stage: StageValidate,
			Cause: &InvalidOptionsError{
				Field:  "SourceRepo",
				Detail: fmt.Sprintf("path does not exist: %v", statErr),
			},
		}
	}

	// Check SourceRepo is a git repo: .git must exist (file or dir).
	gitPath := filepath.Join(opts.SourceRepo, ".git")
	if _, statErr := os.Stat(gitPath); statErr != nil {
		return nil, &ConstructError{
			Stage: StageValidate,
			Cause: &InvalidOptionsError{
				Field:  "SourceRepo (not a git repo)",
				Detail: fmt.Sprintf("no .git entry at %s", gitPath),
			},
		}
	}

	// 2. Resolve ScratchDir and create unique sub-dir.
	scratchParent := opts.ScratchDir
	if scratchParent == "" {
		scratchParent = os.TempDir()
	}

	scratchPath, mkErr := os.MkdirTemp(scratchParent, "gsd-worktree-*")
	if mkErr != nil {
		return nil, &ConstructError{
			Stage: StageClone,
			Cause: mkErr,
		}
	}

	// cleanup is a helper that removes the scratch dir on failure and
	// populates CleanupErr if the removal itself fails.
	cleanup := func(ce *ConstructError) *ConstructError {
		if rmErr := os.RemoveAll(scratchPath); rmErr != nil {
			ce.CleanupErr = rmErr
		}
		return ce
	}

	// 3. git clone --local <SourceRepo> <scratchPath>
	// MkdirTemp already created the directory; git clone requires the
	// target to not exist (or be empty). Remove the dir so git can
	// create it itself — or use a subdirectory naming trick. The
	// simplest approach: clone into a known sub-path of scratchPath.
	cloneTarget := filepath.Join(scratchPath, "repo")

	exitCode, _, stderr, runErr := runGit(ctx, "", "clone", "--local", opts.SourceRepo, cloneTarget)
	if runErr != nil {
		return nil, cleanup(&ConstructError{
			Stage: StageClone,
			Cause: runErr,
		})
	}
	if exitCode != 0 {
		return nil, cleanup(&ConstructError{
			Stage: StageClone,
			Cause: &CloneError{
				SourceRepo: opts.SourceRepo,
				ScratchDir: cloneTarget,
				ExitCode:   exitCode,
				Stderr:     stderr,
			},
		})
	}

	// 4. git -C <cloneTarget> checkout <BaseSHA>
	// The --local scratch clone has every object the source repo has, so
	// BaseSHA is directly reachable without any fetch step.
	exitCode, _, stderr, runErr = runGit(ctx, cloneTarget, "checkout", opts.BaseSHA)
	if runErr != nil {
		return nil, cleanup(&ConstructError{
			Stage: StageCheckout,
			Cause: runErr,
		})
	}
	if exitCode != 0 {
		return nil, cleanup(&ConstructError{
			Stage: StageCheckout,
			Cause: &CheckoutError{
				SHA:      opts.BaseSHA,
				ExitCode: exitCode,
				Stderr:   stderr,
			},
		})
	}

	// 5. Merge PRSHA directly into the checked-out BaseSHA. No fetch step
	// needed: the --local clone already has every object from the source
	// repo — see docs/adr/0010-prref-resolution-contract.md.
	mergeMsg := fmt.Sprintf("PR merge of %s into %s", opts.PRSHA, opts.BaseSHA)
	exitCode, _, stderr, runErr = runGit(ctx, cloneTarget, "merge", "--no-edit", "-m", mergeMsg, opts.PRSHA)
	if runErr != nil {
		return nil, cleanup(&ConstructError{
			Stage: StageMerge,
			Cause: runErr,
		})
	}
	if exitCode != 0 {
		// Try to list conflicting files.
		_, conflictOut, _, _ := runGit(ctx, cloneTarget, "diff", "--name-only", "--diff-filter=U")
		conflictFiles := parseLines(conflictOut)
		if len(conflictFiles) > 0 {
			return nil, cleanup(&ConstructError{
				Stage: StageMerge,
				Cause: &MergeConflictError{
					BaseSHA: opts.BaseSHA,
					PRSHA:   opts.PRSHA,
					Files:   conflictFiles,
				},
			})
		}
		return nil, cleanup(&ConstructError{
			Stage: StageMerge,
			Cause: &MergeError{
				BaseSHA:  opts.BaseSHA,
				PRSHA:    opts.PRSHA,
				ExitCode: exitCode,
				Stderr:   stderr,
			},
		})
	}

	// 6. Success.
	return &Worktree{path: cloneTarget}, nil
}

// parseLines splits s on newlines and returns non-empty trimmed lines.
func parseLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
