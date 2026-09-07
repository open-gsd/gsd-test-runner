package images

import (
	"context"
	"errors"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
)

// stubTart swaps tartList/tartPull to the provided stubs (or no-ops if
// nil), restored via t.Cleanup, mirroring this package's own stubAll
// pattern for the Docker seams.
func stubTart(t *testing.T,
	list func(ctx context.Context, b bench.Bench) (string, error),
	pull func(ctx context.Context, b bench.Bench, image string) (string, error),
) {
	t.Helper()
	origL, origP := tartList, tartPull
	if list != nil {
		tartList = list
	}
	if pull != nil {
		tartPull = pull
	}
	t.Cleanup(func() {
		tartList = origL
		tartPull = origP
	})
}

const sampleTartListOutput = `Source     Name                                              Disk    Size    Accessed  State
OCI        ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0    50.1GB  27.3GB  1h ago    stopped
local      some-other-vm                                     10.0GB  5.0GB   2d ago    stopped
`

func TestEnsurePresentTart_AlreadyPresent_PullNotCalled(t *testing.T) {
	pullCalled := false
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) { return sampleTartListOutput, nil },
		func(context.Context, bench.Bench, string) (string, error) {
			pullCalled = true
			return "", nil
		},
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	err := EnsurePresentTart(context.Background(), b, ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0"))
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if pullCalled {
		t.Fatal("pull should not be called when image already present in tart list output")
	}
}

func TestEnsurePresentTart_NotPresent_PullSucceeds(t *testing.T) {
	var pulledImage string
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) { return sampleTartListOutput, nil },
		func(_ context.Context, _ bench.Bench, image string) (string, error) {
			pulledImage = image
			return "", nil
		},
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	image := ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:v2.0.0")
	err := EnsurePresentTart(context.Background(), b, image)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if pulledImage != string(image) {
		t.Errorf("expected pull called with %q, got %q", image, pulledImage)
	}
}

func TestEnsurePresentTart_ListFails_ReturnsTartListError(t *testing.T) {
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) {
			return "", &tartexec.ExecError{Stderr: "ssh: connection refused", ExitCode: 255}
		},
		nil,
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	err := EnsurePresentTart(context.Background(), b, ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0"))
	var tle *TartListError
	if !errors.As(err, &tle) {
		t.Fatalf("expected *TartListError, got %T: %v", err, err)
	}
	if tle.Bench != "bench-macos-1" {
		t.Errorf("expected Bench=bench-macos-1, got %q", tle.Bench)
	}
}

func TestEnsurePresentTart_PullAuthError(t *testing.T) {
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) { return "Source Name Disk Size Accessed State\n", nil },
		func(context.Context, bench.Bench, string) (string, error) {
			return "", &tartexec.ExecError{Stderr: "unauthorized: authentication required", ExitCode: 1}
		},
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	err := EnsurePresentTart(context.Background(), b, ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0"))
	var pae *PullAuthError
	if !errors.As(err, &pae) {
		t.Fatalf("expected *PullAuthError, got %T: %v", err, err)
	}
}

func TestEnsurePresentTart_PullNotFoundError(t *testing.T) {
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) { return "Source Name Disk Size Accessed State\n", nil },
		func(context.Context, bench.Bench, string) (string, error) {
			return "", &tartexec.ExecError{Stderr: "manifest unknown", ExitCode: 1}
		},
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	err := EnsurePresentTart(context.Background(), b, ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:vNOPE"))
	var pnf *PullNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("expected *PullNotFoundError, got %T: %v", err, err)
	}
}

func TestEnsurePresentTart_PullDockerError_Fallthrough(t *testing.T) {
	stubTart(t,
		func(context.Context, bench.Bench) (string, error) { return "Source Name Disk Size Accessed State\n", nil },
		func(context.Context, bench.Bench, string) (string, error) {
			return "", &tartexec.ExecError{Stderr: "network unreachable", ExitCode: 1}
		},
	)
	b := bench.Bench{Name: "bench-macos-1", Host: "bench-macos-1", Runtime: bench.RuntimeTart}
	err := EnsurePresentTart(context.Background(), b, ImageID("ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0"))
	var pde *PullDockerError
	if !errors.As(err, &pde) {
		t.Fatalf("expected *PullDockerError, got %T: %v", err, err)
	}
}

func TestTartImagePresent_ExactNameMatch(t *testing.T) {
	if !tartImagePresent(sampleTartListOutput, "ghcr.io/open-gsd/gsd-tester-macos-tart:v1.0.0") {
		t.Error("expected match for the exact Name column value")
	}
	if tartImagePresent(sampleTartListOutput, "ghcr.io/open-gsd/gsd-tester-macos-tart:v9.9.9") {
		t.Error("expected no match for an absent version tag")
	}
}

func TestTartImagePresent_EmptyOutput(t *testing.T) {
	if tartImagePresent("", "anything") {
		t.Error("expected false on empty tart list output")
	}
}

func TestTartImagePresent_HeaderOnlyOutput(t *testing.T) {
	if tartImagePresent("Source Name Disk Size Accessed State\n", "anything") {
		t.Error("expected false when only the header row is present")
	}
}
