package images

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
)

// Package-level stubbable vars over internal/tartexec (mirrors this file's
// own dockerInspect/dockerPull/dockerBuild pattern above): tests swap these
// for stubs returning canned output, restored via t.Cleanup, so
// EnsurePresentTart never needs real SSH/network in tests.
var (
	tartList = func(ctx context.Context, b bench.Bench) (string, error) {
		return tartexec.RunTart(ctx, b, []string{"list"})
	}
	tartPull = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		return tartexec.RunTart(ctx, b, []string{"pull", image})
	}
)

// EnsurePresentTart guarantees the named Tart Tester Image is present as a
// local VM on the Bench, pulling from the registry if absent. Unlike
// EnsurePresent (Docker), there is no build-fallback: baking a Tart image
// requires the full Packer pipeline (dockerfiles/macos-tart.pkr.hcl), which
// is not something to trigger ad hoc mid-test-run — see ADR-0030 Decision 6.
//
// Presence is checked via `tart list` (no `tart image inspect`-equivalent
// single-image query exists) rather than `tart clone`-and-see-if-it-fails,
// mirroring the shape of imagePresent above (check first, only pull if
// absent) — see tartImagePresent's doc comment for the parsing approach and
// its documented assumption.
//
// Returns:
//   - nil if the image is now present on the Bench
//   - *TartListError if `tart list` itself failed (Bench/Tart infra issue)
//   - *PullAuthError if the registry refused authentication
//   - *PullNotFoundError if the image is not in the registry
//   - *PullDockerError for other pull failures (network, registry 5xx, etc.)
//
// PullAuthError/PullNotFoundError/PullDockerError are the SAME types
// EnsurePresent (Docker) returns — reused here (not reinvented as
// Tart-specific types) per the brief's guidance: internal/runner and other
// callers already know how to handle images.Pull*Error, so reusing them
// avoids adding new error-type surface for callers to learn. Their Stderr
// field carries whatever `tart pull`'s stderr said, which reads sensibly
// even though the underlying tool differs from docker pull.
func EnsurePresentTart(ctx context.Context, b bench.Bench, image ImageID) error {
	out, listErr := tartList(ctx, b)
	if listErr != nil {
		var ee *tartexec.ExecError
		if errors.As(listErr, &ee) {
			return &TartListError{Bench: b.Name, Stderr: ee.Stderr, ExitCode: ee.ExitCode}
		}
		return listErr
	}
	if tartImagePresent(out, string(image)) {
		return nil
	}

	_, pullErr := tartPull(ctx, b, string(image))
	if pullErr == nil {
		return nil
	}

	var ee *tartexec.ExecError
	if !errors.As(pullErr, &ee) {
		return pullErr // unexpected, non-exec error
	}
	stderr := ee.Stderr
	switch {
	case isAuthError(stderr):
		return &PullAuthError{Bench: b.Name, Image: string(image), Stderr: stderr}
	case isNotFoundError(stderr):
		return &PullNotFoundError{Bench: b.Name, Image: string(image), Stderr: stderr}
	default:
		return &PullDockerError{Bench: b.Name, Image: string(image), Stderr: stderr, ExitCode: ee.ExitCode}
	}
}

// tartImagePresent parses `tart list` output looking for a row whose Name
// column exactly matches image.
//
// DOCUMENTED ASSUMPTION: `tart list`'s columns, confirmed hands-on this
// session (ADR-0030 Decision 3/Consequences), are "Source Name Disk Size
// Accessed State" with a header row first. This assumes the Name column for
// an OCI-pulled/cloned image holds the exact reference string passed to
// `tart pull`/`tart clone` (as opposed to some shortened or locally-aliased
// form) — that exact-match behavior was not independently re-verified for
// every possible registry reference shape in this PR; if a future Tart
// version normalizes/truncates the Name column differently, this match
// would need to become a prefix or fuzzy match instead. Documented here
// rather than silently assumed, per the brief's instruction to make
// judgment calls legible.
func tartImagePresent(listOutput, image string) bool {
	lines := strings.Split(listOutput, "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header row
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == image {
			return true
		}
	}
	return false
}

// TartListError is returned when `tart list` itself fails (as opposed to
// the image simply being absent from its output) — a Bench/Tart
// infrastructure failure (SSH unreachable, tart not installed, etc.),
// analogous to BenchDockerError's role for the Docker imagePresent check
// above, but Tart-specific because there's no docker-daemon-down class of
// failure to fold it into.
type TartListError struct {
	Bench    string
	Stderr   string
	ExitCode int
}

func (e *TartListError) Error() string {
	return fmt.Sprintf("bench %s: tart list failed (exit=%d): %s", e.Bench, e.ExitCode, strings.TrimSpace(e.Stderr))
}
