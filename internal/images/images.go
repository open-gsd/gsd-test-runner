package images

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
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
	// FallbackBuildArgs are passed as `--build-arg K=V` to the fallback
	// build. The Node matrix (enhancement #108) uses this to pass
	// NODE_VERSION so a locally-built fallback image bakes the SAME Node
	// major the pulled image would have — otherwise the fallback silently
	// builds the Dockerfile's default major. Empty adds no build args.
	FallbackBuildArgs map[string]string
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
	_, buildErr := dockerBuild(ctx, b, opts.FallbackDockerfile, opts.FallbackContextDir, string(image), opts.FallbackBuildArgs)
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
	dockerBuild = func(ctx context.Context, b bench.Bench, dockerfile, contextDir, tag string, buildArgs map[string]string) (string, error) {
		args := []string{"build", "-t", tag, "-f", dockerfile}
		// Deterministic --build-arg ordering (sorted keys) so the command is
		// stable across runs and reproducible in logs/tests.
		keys := make([]string, 0, len(buildArgs))
		for k := range buildArgs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--build-arg", k+"="+buildArgs[k])
		}
		args = append(args, contextDir)
		return dockerexec.Run(ctx, b, args)
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

// ImageVersionLabel is the OCI sentinel label carrying the Tester Image's
// release version (ADR-0011). Reverse-DNS, matching the run-container labels.
// The Node major is a separate label (ADR-0024); this one stays un-suffixed.
const ImageVersionLabel = "sh.gsd-test.image-version"

// ImageVersionMismatch is returned when the image's sentinel label does not
// match the version expected for this repo checkout — the "stale image silently
// produces wrong results" failure class, surfaced loud (ADR-0011/ADR-0004).
// Bench is optional, carried for diagnostics when a Bench context is known.
type ImageVersionMismatch struct {
	Bench string // optional, for diagnostics
	Image string
	Want  string
	Got   string // "" when the label is absent
}

func (e *ImageVersionMismatch) Error() string {
	loc := ""
	if e.Bench != "" {
		loc = " on bench " + e.Bench
	}
	if e.Got == "" {
		return fmt.Sprintf("image %s%s: expected version %q but image has no %s label", e.Image, loc, e.Want, ImageVersionLabel)
	}
	return fmt.Sprintf("image %s%s: expected version %q, got %q", e.Image, loc, e.Want, e.Got)
}

// VerifyImageVersion reads the image-version sentinel label via
// `docker image inspect` and checks it equals want. An empty want skips the
// check (no expected version configured). runner is the docker-execution seam
// (reaper.Runner — dockerexec over SSH in production, faked in tests).
//
// This is the single version-sentinel check used by both execution engines:
// the Pipeline engine's CheckImageVersion leg and the Watchdog engine's
// pre-run check (ADR-0027). It closes the open question noted in this
// package's doc.go.
func VerifyImageVersion(ctx context.Context, runner reaper.Runner, imageID, want string) error {
	if want == "" {
		return nil
	}
	out, err := runner(ctx, "image", "inspect", imageID,
		"--format", fmt.Sprintf(`{{ index .Config.Labels %q }}`, ImageVersionLabel))
	if err != nil {
		return fmt.Errorf("images: inspect image version: %w", err)
	}
	got := strings.TrimSpace(string(out))
	if got != want {
		return &ImageVersionMismatch{Image: imageID, Want: want, Got: got}
	}
	return nil
}

// Ref constructs a Tester Image reference for osName at version, encoding the
// ADR-0024 tag convention in one place. When nodeMajor is empty the plain
// :<version> tag is used (Active-LTS back-compat — the single-Node run-and-die
// path); otherwise the :<version>-node<major> suffix selects a specific Node
// matrix image. When version is also empty the reference is untagged (resolves
// to :latest; version is then verified via the OCI sentinel label rather than
// the tag). This is the single source of truth for the Tester Image tag policy.
func Ref(osName, version, nodeMajor string) ImageID {
	base := fmt.Sprintf("ghcr.io/open-gsd/gsd-tester-%s", osName)
	if version == "" {
		return ImageID(base)
	}
	tag := version
	if nodeMajor != "" {
		tag = version + "-node" + nodeMajor
	}
	return ImageID(base + ":" + tag)
}
