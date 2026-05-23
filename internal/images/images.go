package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
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
	dockerHost := ""
	if b.Host != "" && b.Host != "local" {
		dockerHost = "ssh://" + b.Host
	}

	// 1. Check presence first.
	present, presenceErr := imagePresent(ctx, dockerHost, string(image))
	if presenceErr != nil {
		var de *dockerExecError
		if errors.As(presenceErr, &de) {
			return &BenchDockerError{
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
	pullErr := dockerPull(ctx, dockerHost, string(image))
	if pullErr == nil {
		return nil
	}

	// 3. Classify and possibly fall back.
	var de *dockerExecError
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
		return tryFallbackBuild(ctx, dockerHost, b, image, opts)
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
// docker daemon at dockerHost. Returns (false, nil) for "no such
// image", (false, error) for any other failure.
func imagePresent(ctx context.Context, dockerHost, image string) (bool, error) {
	_, err := dockerInspect(ctx, dockerHost, image, "{{.Id}}")
	if err == nil {
		return true, nil
	}
	var de *dockerExecError
	if errors.As(err, &de) && strings.Contains(de.Stderr, "No such image") {
		return false, nil
	}
	return false, err
}

func tryFallbackBuild(ctx context.Context, dockerHost string, b bench.Bench, image ImageID, opts EnsurePresentOptions) error {
	buildErr := dockerBuild(ctx, dockerHost, string(image), opts.FallbackDockerfile, opts.FallbackContextDir)
	if buildErr == nil {
		return nil
	}
	var de *dockerExecError
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

// dockerExecError captures the output of a failed docker command for
// classification by callers. Deliberately duplicated from
// internal/pipeline — extraction to a shared internal/dockerexec
// package will follow once a third leg implementation cements the
// patterns (per NOTES.md "Known duplication").
type dockerExecError struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

func (e *dockerExecError) Error() string {
	return fmt.Sprintf("docker %s failed (exit %d): %s", strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// Function variables per ADR-0011 (decision 4): tests swap these for
// stubs returning canned output, restored via t.Cleanup.
var (
	dockerInspect = realDockerInspect
	dockerPull    = realDockerPull
	dockerBuild   = realDockerBuild
)

func realDockerInspect(ctx context.Context, dockerHost, image, format string) (string, error) {
	args := []string{"image", "inspect", image, "--format", format}
	return runDocker(ctx, dockerHost, args, "")
}

func realDockerPull(ctx context.Context, dockerHost, image string) error {
	args := []string{"pull", image}
	_, err := runDocker(ctx, dockerHost, args, "")
	return err
}

func realDockerBuild(ctx context.Context, dockerHost, image, dockerfile, contextDir string) error {
	args := []string{"build", "-t", image, "-f", dockerfile, contextDir}
	_, err := runDocker(ctx, dockerHost, args, "")
	return err
}

func runDocker(ctx context.Context, dockerHost string, args []string, _stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), &dockerExecError{
				Args:     append([]string{"docker"}, args...),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: exitErr.ExitCode(),
			}
		}
		return "", fmt.Errorf("docker exec failed: %w", err)
	}
	return stdout.String(), nil
}

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

type BenchDockerError struct {
	Bench    string
	Args     []string
	Stderr   string
	ExitCode int
}

func (e *BenchDockerError) Error() string {
	return fmt.Sprintf("docker on bench %s failed (exit %d): %s", e.Bench, e.ExitCode, strings.TrimSpace(e.Stderr))
}
