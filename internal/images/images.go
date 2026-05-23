package images

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
)

// ImageID is a fully-qualified Tester Image reference, typically
// "ghcr.io/open-gsd/gsd-tester-<os>:<tag>" (ADR-0005) or a local
// tag like "gsd-tester-linux:dev" when built from the in-repo
// Dockerfile fallback.
type ImageID string

// EnsurePresentOptions configures EnsurePresent's fallback behavior.
type EnsurePresentOptions struct {
	// FallbackDockerfile, if non-empty, is the path to a Dockerfile
	// EnsurePresent builds when the pull fails with "not found". Empty
	// disables fallback (pull-only mode).
	FallbackDockerfile string
	// FallbackContextDir is the build context directory passed to
	// docker build. Required when FallbackDockerfile is set.
	FallbackContextDir string
}

// EnsurePresent guarantees that the named Tester Image is present on
// the Bench's docker daemon. If already present, returns nil
// immediately. If absent, pulls from the registry (typically GHCR per
// ADR-0005). If the pull fails with a not-found error and opts.FallbackDockerfile
// is configured, falls back to building from that Dockerfile.
//
// Canonical Dockerfile paths (per ADR-0012):
//   - Linux:   dockerfiles/linux.Dockerfile
//   - Windows: dockerfiles/windows.Dockerfile
//
// Both Dockerfiles expect the build context to be the repo root (so
// COPY reporter/reporter.mjs resolves correctly). Wiring these paths
// into EnsurePresentOptions.FallbackDockerfile is Slice G8's job.
//
// Version label verification is NOT performed here — that is
// Pipeline.CheckImageVersion's responsibility per ADR-0012.
//
// Returns:
//   - nil if the image is now present on the Bench
//   - *PullAuthError if the registry refused authentication
//   - *PullNotFoundError if the image is not in the registry and no fallback was configured
//   - *PullDockerError for other pull failures (network, registry 5xx, etc.)
//   - *BuildError if the fallback build failed
//   - *BenchDockerError if the initial presence check failed for a non-image-related reason
func EnsurePresent(ctx context.Context, b bench.Bench, image ImageID, opts EnsurePresentOptions) error {
	// 1. Check presence first.
	present, presenceErr := imagePresent(ctx, b, string(image))
	if presenceErr != nil {
		var de *dockerexec.ExecError
		if errors.As(presenceErr, &de) {
			return &bench.BenchDockerError{
				Bench:    b.Name,
				Args:     de.Args,
				Stderr:   de.Stderr,
				ExitCode: de.ExitCode,
			}
		}
		return presenceErr
	}
	if present {
		return nil
	}

	// 2. Try pulling.
	_, pullErr := dockerPull(ctx, b, string(image))
	if pullErr == nil {
		return nil
	}

	// 3. Classify and possibly fall back.
	var de *dockerexec.ExecError
	if !errors.As(pullErr, &de) {
		return pullErr // unexpected, non-exec error
	}
	stderr := de.Stderr
	switch {
	case isAuthError(stderr):
		return &PullAuthError{Bench: b.Name, Image: string(image), Stderr: stderr}
	case isNotFoundError(stderr):
		if opts.FallbackDockerfile == "" {
			return &PullNotFoundError{Bench: b.Name, Image: string(image), Stderr: stderr}
		}
		return tryFallbackBuild(ctx, b, image, opts)
	default:
		return &PullDockerError{
			Bench:    b.Name,
			Image:    string(image),
			Stderr:   stderr,
			ExitCode: de.ExitCode,
		}
	}
}

// imagePresent returns true if the named image is present on the
// docker daemon at the bench. Returns (false, nil) for "no such
// image", (false, error) for any other failure.
func imagePresent(ctx context.Context, b bench.Bench, image string) (bool, error) {
	_, err := dockerInspect(ctx, b, image)
	if err == nil {
		return true, nil
	}
	var de *dockerexec.ExecError
	if errors.As(err, &de) && strings.Contains(de.Stderr, "No such image") {
		return false, nil
	}
	return false, err
}

func tryFallbackBuild(ctx context.Context, b bench.Bench, image ImageID, opts EnsurePresentOptions) error {
	_, buildErr := dockerBuild(ctx, b, opts.FallbackDockerfile, opts.FallbackContextDir, string(image))
	if buildErr == nil {
		return nil
	}
	var de *dockerexec.ExecError
	if errors.As(buildErr, &de) {
		return &BuildError{
			Bench:      b.Name,
			Image:      string(image),
			Dockerfile: opts.FallbackDockerfile,
			ContextDir: opts.FallbackContextDir,
			Stderr:     de.Stderr,
			ExitCode:   de.ExitCode,
		}
	}
	return buildErr
}

// isAuthError reports whether the docker stderr indicates an
// authentication / authorization failure.
func isAuthError(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "no basic auth credentials")
}

// isNotFoundError reports whether the docker stderr indicates the
// image (or its manifest) is not in the registry.
func isNotFoundError(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "manifest unknown") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "manifest for")
}

// Function variables per ADR-0011 (decision 4): tests swap these for
// stubs returning canned output, restored via t.Cleanup.
var (
	dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		return dockerexec.Run(ctx, b, []string{"image", "inspect", image})
	}
	dockerPull = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		return dockerexec.Run(ctx, b, []string{"pull", image})
	}
	dockerBuild = func(ctx context.Context, b bench.Bench, dockerfile, contextDir, tag string) (string, error) {
		return dockerexec.Run(ctx, b, []string{"build", "-t", tag, "-f", dockerfile, contextDir})
	}
)

// Typed errors. Each carries Bench name + Image ID for diagnostics.

type PullAuthError struct {
	Bench  string
	Image  string
	Stderr string
}

func (e *PullAuthError) Error() string {
	return fmt.Sprintf("pull of %s on bench %s denied: %s (run `ssh %s docker login ghcr.io` to authenticate)", e.Image, e.Bench, strings.TrimSpace(e.Stderr), e.Bench)
}

type PullNotFoundError struct {
	Bench  string
	Image  string
	Stderr string
}

func (e *PullNotFoundError) Error() string {
	return fmt.Sprintf("image %s not found in registry from bench %s: %s", e.Image, e.Bench, strings.TrimSpace(e.Stderr))
}

type PullDockerError struct {
	Bench    string
	Image    string
	Stderr   string
	ExitCode int
}

func (e *PullDockerError) Error() string {
	return fmt.Sprintf("pull of %s on bench %s failed (exit %d): %s", e.Image, e.Bench, e.ExitCode, strings.TrimSpace(e.Stderr))
}

type BuildError struct {
	Bench      string
	Image      string
	Dockerfile string
	ContextDir string
	Stderr     string
	ExitCode   int
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("fallback build of %s on bench %s (using %s) failed (exit %d): %s", e.Image, e.Bench, e.Dockerfile, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// BenchDockerError is an alias kept for package-local convenience; the
// canonical definition lives in internal/bench.
type BenchDockerError = bench.BenchDockerError
