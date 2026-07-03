package images

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
)

// stubAll swaps inspect/pull/build to the provided stubs (or no-ops
// if nil), restored via t.Cleanup.
func stubAll(t *testing.T,
	inspect func(ctx context.Context, b bench.Bench, image string) (string, error),
	pull func(ctx context.Context, b bench.Bench, image string) (string, error),
	build func(ctx context.Context, b bench.Bench, dockerfile, contextDir, tag string, buildArgs map[string]string) (string, error),
) {
	t.Helper()
	origI, origP, origB := dockerInspect, dockerPull, dockerBuild
	if inspect != nil {
		dockerInspect = inspect
	}
	if pull != nil {
		dockerPull = pull
	}
	if build != nil {
		dockerBuild = build
	}
	t.Cleanup(func() {
		dockerInspect = origI
		dockerPull = origP
		dockerBuild = origB
	})
}

// noSuchImageErr returns a *dockerexec.ExecError that imagePresent recognises
// as "image not present" (not a bench error).
func noSuchImageErr() *dockerexec.ExecError {
	return &dockerexec.ExecError{
		Args:     []string{"image", "inspect", "img"},
		Stderr:   "Error: No such image: img",
		ExitCode: 1,
	}
}

// TestEnsurePresent_AlreadyPresent_PullNotCalled — inspect returns
// success; asserts pull is NOT called and err == nil.
func TestEnsurePresent_AlreadyPresent_PullNotCalled(t *testing.T) {
	pullCalled := false
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "sha256:abc123", nil
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			pullCalled = true
			return "", nil
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if pullCalled {
		t.Fatal("pull should not be called when image already present")
	}
}

// TestEnsurePresent_NotPresent_PullSucceeds — inspect returns
// "No such image"; pull returns nil; build should NOT be called.
func TestEnsurePresent_NotPresent_PullSucceeds(t *testing.T) {
	buildCalled := false
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", nil
		},
		func(_ context.Context, _ bench.Bench, _, _, _ string, _ map[string]string) (string, error) {
			buildCalled = true
			return "", nil
		},
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if buildCalled {
		t.Fatal("build should not be called when pull succeeds")
	}
}

// TestEnsurePresent_PullAuthError — pull stderr "unauthorized: authentication
// required". Expects *PullAuthError with populated fields and login hint.
func TestEnsurePresent_PullAuthError(t *testing.T) {
	stderr := "unauthorized: authentication required"
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: stderr, ExitCode: 1}
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var ae *PullAuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *PullAuthError, got %T: %v", err, err)
	}
	if ae.Bench != b.Name {
		t.Errorf("Bench = %q, want %q", ae.Bench, b.Name)
	}
	if ae.Image != "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0" {
		t.Errorf("Image = %q, want %q", ae.Image, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0")
	}
	if ae.Stderr != stderr {
		t.Errorf("Stderr = %q, want %q", ae.Stderr, stderr)
	}
	if !strings.Contains(ae.Error(), "docker login ghcr.io") {
		t.Errorf("Error() missing login hint, got: %s", ae.Error())
	}
	if !strings.Contains(ae.Error(), "ssh "+b.Name) {
		t.Errorf("Error() missing ssh bench hint, got: %s", ae.Error())
	}
}

// TestEnsurePresent_PullAuthError_DeniedVariant — stderr "denied:
// requested access to the resource is denied". Expects *PullAuthError.
func TestEnsurePresent_PullAuthError_DeniedVariant(t *testing.T) {
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "denied: requested access to the resource is denied", ExitCode: 1}
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var ae *PullAuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *PullAuthError, got %T: %v", err, err)
	}
}

// TestEnsurePresent_PullAuthError_NoBasicAuthVariant — stderr "no basic
// auth credentials". Expects *PullAuthError.
func TestEnsurePresent_PullAuthError_NoBasicAuthVariant(t *testing.T) {
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "no basic auth credentials", ExitCode: 1}
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var ae *PullAuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *PullAuthError, got %T: %v", err, err)
	}
}

// TestEnsurePresent_PullNotFound_NoFallback_ReturnsPullNotFoundError —
// pull stderr "manifest unknown", no FallbackDockerfile. Expects
// *PullNotFoundError and build NOT called.
func TestEnsurePresent_PullNotFound_NoFallback_ReturnsPullNotFoundError(t *testing.T) {
	buildCalled := false
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "manifest unknown", ExitCode: 1}
		},
		func(_ context.Context, _ bench.Bench, _, _, _ string, _ map[string]string) (string, error) {
			buildCalled = true
			return "", nil
		},
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var nfe *PullNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *PullNotFoundError, got %T: %v", err, err)
	}
	if buildCalled {
		t.Fatal("build should not be called when no fallback configured")
	}
}

// TestEnsurePresent_PullNotFound_NotFoundVariant — stderr "manifest for
// ghcr.io/foo:v1 not found". Expects *PullNotFoundError when no fallback.
func TestEnsurePresent_PullNotFound_NotFoundVariant(t *testing.T) {
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "Error response from daemon: manifest for ghcr.io/foo:v1 not found", ExitCode: 1}
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var nfe *PullNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *PullNotFoundError, got %T: %v", err, err)
	}
}

// TestEnsurePresent_PullNotFound_FallbackBuildSucceeds — pull not-found,
// fallback configured, build returns nil. Assert err == nil and build
// was called with correct dockerfile + context.
func TestEnsurePresent_PullNotFound_FallbackBuildSucceeds(t *testing.T) {
	var capturedDockerfile, capturedContextDir string
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "manifest unknown", ExitCode: 1}
		},
		func(_ context.Context, _ bench.Bench, dockerfile, contextDir, _ string, _ map[string]string) (string, error) {
			capturedDockerfile = dockerfile
			capturedContextDir = contextDir
			return "", nil
		},
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	opts := EnsurePresentOptions{
		FallbackDockerfile: "dockerfiles/linux.Dockerfile",
		FallbackContextDir: ".",
	}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", opts)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if capturedDockerfile != "dockerfiles/linux.Dockerfile" {
		t.Errorf("dockerfile = %q, want %q", capturedDockerfile, "dockerfiles/linux.Dockerfile")
	}
	if capturedContextDir != "." {
		t.Errorf("contextDir = %q, want %q", capturedContextDir, ".")
	}
}

// TestEnsurePresent_PullNotFound_FallbackBuildFails — pull not-found,
// fallback configured, build returns dockerexec.ExecError. Expects *BuildError
// with Dockerfile, ContextDir, Stderr fields populated.
func TestEnsurePresent_PullNotFound_FallbackBuildFails(t *testing.T) {
	buildStderr := "failed to solve: error from rpc"
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "manifest unknown", ExitCode: 1}
		},
		func(_ context.Context, _ bench.Bench, _, _, _ string, _ map[string]string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: buildStderr, ExitCode: 1}
		},
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	opts := EnsurePresentOptions{
		FallbackDockerfile: "dockerfiles/linux.Dockerfile",
		FallbackContextDir: ".",
	}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", opts)
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BuildError, got %T: %v", err, err)
	}
	if be.Dockerfile != "dockerfiles/linux.Dockerfile" {
		t.Errorf("Dockerfile = %q, want %q", be.Dockerfile, "dockerfiles/linux.Dockerfile")
	}
	if be.ContextDir != "." {
		t.Errorf("ContextDir = %q, want %q", be.ContextDir, ".")
	}
	if be.Stderr != buildStderr {
		t.Errorf("Stderr = %q, want %q", be.Stderr, buildStderr)
	}
}

// TestEnsurePresent_PullGenericFailure_ReturnsPullDockerError — pull
// stderr is a network error (not auth, not not-found). Expects
// *PullDockerError.
func TestEnsurePresent_PullGenericFailure_ReturnsPullDockerError(t *testing.T) {
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", noSuchImageErr()
		},
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{Stderr: "Get https://ghcr.io/v2/: dial tcp: lookup ghcr.io: no such host", ExitCode: 1}
		},
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var pde *PullDockerError
	if !errors.As(err, &pde) {
		t.Fatalf("expected *PullDockerError, got %T: %v", err, err)
	}
}

// TestEnsurePresent_InitialInspectFails_NonImageError_ReturnsBenchDockerError
// — inspect returns dockerexec.ExecError with "Cannot connect to the Docker daemon"
// (not "No such image"). Expects *BenchDockerError.
func TestEnsurePresent_InitialInspectFails_NonImageError_ReturnsBenchDockerError(t *testing.T) {
	stubAll(t,
		func(_ context.Context, _ bench.Bench, _ string) (string, error) {
			return "", &dockerexec.ExecError{
				Args:     []string{"image", "inspect"},
				Stderr:   "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
				ExitCode: 1,
			}
		},
		nil,
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	var bde *BenchDockerError
	if !errors.As(err, &bde) {
		t.Fatalf("expected *BenchDockerError, got %T: %v", err, err)
	}
	if bde.Bench != b.Name {
		t.Errorf("Bench = %q, want %q", bde.Bench, b.Name)
	}
}

// TestEnsurePresent_LocalBench_NoDockerHostPassed — bench.Host = "local";
// bench passed to inspect stub must have DockerHost() == "".
func TestEnsurePresent_LocalBench_NoDockerHostPassed(t *testing.T) {
	var capturedBench bench.Bench
	stubAll(t,
		func(_ context.Context, b bench.Bench, _ string) (string, error) {
			capturedBench = b
			return "sha256:abc", nil
		},
		nil,
		nil,
	)

	b := bench.Bench{Name: "local-bench", Host: "local"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if capturedBench.DockerHost() != "" {
		t.Errorf("DockerHost() = %q, want %q (empty)", capturedBench.DockerHost(), "")
	}
}

// TestEnsurePresent_EmptyHost_NoDockerHostPassed — bench.Host = "";
// bench passed to inspect stub must have DockerHost() == "" (same as local).
func TestEnsurePresent_EmptyHost_NoDockerHostPassed(t *testing.T) {
	var capturedBench bench.Bench
	stubAll(t,
		func(_ context.Context, b bench.Bench, _ string) (string, error) {
			capturedBench = b
			return "sha256:abc", nil
		},
		nil,
		nil,
	)

	b := bench.Bench{Name: "local-bench", Host: ""}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if capturedBench.DockerHost() != "" {
		t.Errorf("DockerHost() = %q, want %q (empty)", capturedBench.DockerHost(), "")
	}
}

// TestEnsurePresent_RemoteBench_PassesSSHDockerHost — bench.Host =
// "bench-linux-1"; bench passed to inspect stub must have DockerHost()
// == "ssh://bench-linux-1".
func TestEnsurePresent_RemoteBench_PassesSSHDockerHost(t *testing.T) {
	var capturedBench bench.Bench
	stubAll(t,
		func(_ context.Context, b bench.Bench, _ string) (string, error) {
			capturedBench = b
			return "sha256:abc", nil
		},
		nil,
		nil,
	)

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(context.Background(), b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if capturedBench.DockerHost() != "ssh://bench-linux-1" {
		t.Errorf("DockerHost() = %q, want %q", capturedBench.DockerHost(), "ssh://bench-linux-1")
	}
}

// TestEnsurePresent_PreCanceledContext_PropagatesContextError — pre-cancel
// ctx before calling EnsurePresent. Asserts err != nil.
func TestEnsurePresent_PreCanceledContext_PropagatesContextError(t *testing.T) {
	inspectCalled := false
	stubAll(t,
		func(ctx context.Context, _ bench.Bench, _ string) (string, error) {
			inspectCalled = true
			// Honour the cancelled context.
			if err := ctx.Err(); err != nil {
				return "", &dockerexec.ExecError{Stderr: err.Error(), ExitCode: 1}
			}
			return "sha256:abc", nil
		},
		nil,
		nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1"}
	err := EnsurePresent(ctx, b, "ghcr.io/open-gsd/gsd-tester-linux:v1.0.0", EnsurePresentOptions{})
	// The important assertion: an error must be returned.
	if err == nil {
		t.Fatal("expected error for pre-cancelled context, got nil")
	}
	_ = inspectCalled // don't over-specify whether inspect was called
}

// TestIsAuthError — table-driven over various stderr strings.
func TestIsAuthError(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"unauthorized: authentication required", true},
		{"UNAUTHORIZED: authentication required", true}, // case-insensitive
		{"denied: requested access to the resource is denied", true},
		{"no basic auth credentials", true},
		{"authentication required", true},
		{"manifest unknown", false},
		{"not found", false},
		{"Get https://ghcr.io/v2/: dial tcp: no such host", false},
		{"Cannot connect to the Docker daemon", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isAuthError(tc.stderr)
		if got != tc.want {
			t.Errorf("isAuthError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

// TestIsNotFoundError — table-driven over various stderr strings.
func TestIsNotFoundError(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"manifest unknown", true},
		{"MANIFEST UNKNOWN", true}, // case-insensitive
		{"not found", true},
		{"Error response from daemon: manifest for ghcr.io/foo:v1 not found", true},
		{"manifest for ghcr.io/open-gsd/gsd-tester-linux:v9.9.9 not found", true},
		{"unauthorized: authentication required", false},
		{"denied: access denied", false},
		{"Cannot connect to the Docker daemon", false},
		{"Get https://ghcr.io/v2/: dial tcp: no such host", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isNotFoundError(tc.stderr)
		if got != tc.want {
			t.Errorf("isNotFoundError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}
