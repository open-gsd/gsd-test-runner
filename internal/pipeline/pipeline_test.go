package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// newTestPipeline builds a Pipeline backed by a buffered event channel
// for tests that need to inspect events. Returns the pipeline and channel.
func newTestPipeline(t *testing.T, bufSize int) (*Pipeline, chan Event) {
	t.Helper()
	ch := make(chan Event, bufSize)
	b := bench.Bench{Name: "test-bench", Host: "localhost", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "v0.0.0-test", "/tmp/worktree", ch)
	return p, ch
}

// stubDocker swaps the package-level dockerInspect for a function
// returning the given output. Restored via t.Cleanup.
func stubDocker(t *testing.T, out string, err error) {
	t.Helper()
	original := dockerInspect
	dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		return out, err
	}
	t.Cleanup(func() { dockerInspect = original })
}

// stubDockerCapture is like stubDocker but captures the bench
// the stub was called with, for tests verifying transport logic.
func stubDockerCapture(t *testing.T, out string, err error) *bench.Bench {
	t.Helper()
	original := dockerInspect
	var captured bench.Bench
	dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		captured = b
		return out, err
	}
	t.Cleanup(func() { dockerInspect = original })
	return &captured
}

// TestNew_NilEventChannelOK verifies that constructing with a nil events
// channel and calling a step method does not panic.
func TestNew_NilEventChannelOK(t *testing.T) {
	stubDocker(t, "v0.0.0-test", nil) // match so leg succeeds without panicking
	b := bench.Bench{Name: "nil-chan-bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "v0.0.0-test", "/tmp/worktree", nil)
	// Must not panic even though emit is called internally.
	err := p.CheckImageVersion(context.Background())
	if err != nil {
		t.Fatalf("expected nil error with matching version, got: %v", err)
	}
}

// TestPipeline_StepMethods_ReturnLegErrorWithNotImplemented table-tests the
// 7 still-stubbed step methods: each must return a *LegError whose Cause is
// ErrNotImplemented and whose Leg field matches the expected leg constant.
// CheckImageVersion is excluded — it has its own dedicated tests below.
func TestPipeline_StepMethods_ReturnLegErrorWithNotImplemented(t *testing.T) {
	type stepCase struct {
		name     string
		leg      Leg
		callStep func(*Pipeline, context.Context) error
	}
	cases := []stepCase{
		{"CopyWorktree", LegCopyWorktree, (*Pipeline).CopyWorktree},
		{"StartContainer", LegStartContainer, (*Pipeline).StartContainer},
		{"NpmCI", LegNpmCI, (*Pipeline).NpmCI},
		{"Build", LegBuild, (*Pipeline).Build},
		{"RunTests", LegRunTests, (*Pipeline).RunTests},
		{"Drain", LegDrain, (*Pipeline).Drain},
		{"Parse", LegParse, (*Pipeline).Parse},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestPipeline(t, 16)
			err := tc.callStep(p, context.Background())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var legErr *LegError
			if !errors.As(err, &legErr) {
				t.Fatalf("expected *LegError, got %T: %v", err, err)
			}
			if legErr.Leg != tc.leg {
				t.Errorf("expected Leg=%v, got %v", tc.leg, legErr.Leg)
			}
			if !errors.Is(legErr.Cause, ErrNotImplemented) {
				t.Errorf("expected Cause=ErrNotImplemented, got %v", legErr.Cause)
			}
		})
	}
}

// TestPipeline_RunAll_StopsAtFirstError verifies RunAll returns a *LegError
// for LegCheckImageVersion (the first leg) whose Cause is *BenchDockerError,
// and a zero Report.
func TestPipeline_RunAll_StopsAtFirstError(t *testing.T) {
	stubDocker(t, "", &dockerexec.ExecError{
		Args:     []string{"image", "inspect"},
		Stderr:   "Cannot connect to the Docker daemon",
		ExitCode: 1,
	})

	p, _ := newTestPipeline(t, 64)
	report, err := p.RunAll(context.Background())
	if err == nil {
		t.Fatal("expected error from RunAll, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if legErr.Leg != LegCheckImageVersion {
		t.Errorf("expected first leg to fail (LegCheckImageVersion=%v), got %v", LegCheckImageVersion, legErr.Leg)
	}
	var benchErr *BenchDockerError
	if !errors.As(legErr.Cause, &benchErr) {
		t.Errorf("expected Cause=*BenchDockerError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	// Report is discarded — on error RunAll returns a zero report.Report.
	_ = report
}

// TestPipeline_EmitsLegStartBeforeReturning verifies that CheckImageVersion
// emits a LegStart event followed by a LegFailure event (on docker error).
func TestPipeline_EmitsLegStartBeforeReturning(t *testing.T) {
	stubDocker(t, "", &dockerexec.ExecError{
		Args:     []string{"image", "inspect"},
		Stderr:   "Cannot connect to the Docker daemon",
		ExitCode: 1,
	})

	p, ch := newTestPipeline(t, 16)
	_ = p.CheckImageVersion(context.Background())

	// Drain all events from the channel.
	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (LegStart + LegFailure), got %d: %v", len(events), events)
	}
	if events[0].Kind != EventLegStart {
		t.Errorf("expected events[0]=EventLegStart, got %v", events[0].Kind)
	}
	if events[0].Leg != LegCheckImageVersion {
		t.Errorf("expected Leg=LegCheckImageVersion, got %v", events[0].Leg)
	}
	if events[0].OS != "linux" {
		t.Errorf("expected OS=linux, got %q", events[0].OS)
	}
	if events[1].Kind != EventLegFailure {
		t.Errorf("expected events[1]=EventLegFailure, got %v", events[1].Kind)
	}
	if events[1].Leg != LegCheckImageVersion {
		t.Errorf("expected events[1].Leg=LegCheckImageVersion, got %v", events[1].Leg)
	}
}

// TestPipeline_PreCanceledContext_ReturnsLegErrorWithContextCause verifies
// that a pre-canceled context surfaces as LegError{Cause: context.Canceled}
// rather than calling the work function.
func TestPipeline_PreCanceledContext_ReturnsLegErrorWithContextCause(t *testing.T) {
	p, _ := newTestPipeline(t, 16)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := p.CheckImageVersion(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if !errors.Is(legErr.Cause, context.Canceled) {
		t.Errorf("expected Cause=context.Canceled, got %v", legErr.Cause)
	}
}

// TestEvent_FullChannel_DoesNotBlock verifies that calling RunAll with a
// capacity-1 channel and no consumer completes without blocking. A short
// context deadline causes the test to fail if Pipeline blocks.
func TestEvent_FullChannel_DoesNotBlock(t *testing.T) {
	stubDocker(t, "", &dockerexec.ExecError{
		Args:     []string{"image", "inspect"},
		Stderr:   "Cannot connect to the Docker daemon",
		ExitCode: 1,
	})

	// Capacity 1: fills after the first event; all subsequent emits must drop.
	ch := make(chan Event, 1)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "v0.0.0-test", "/tmp/worktree", ch)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		p.RunAll(ctx) //nolint:errcheck // result intentionally ignored
		close(done)
	}()

	select {
	case <-done:
		// Good — RunAll completed without blocking.
	case <-ctx.Done():
		t.Fatal("RunAll blocked on full event channel")
	}
}

// TestLeg_String_ContainsLegName verifies that each Leg's String() returns
// the expected stable identifier (used in JSON rendering).
func TestLeg_String_ContainsLegName(t *testing.T) {
	cases := []struct {
		leg  Leg
		want string
	}{
		{LegCheckImageVersion, "check_image_version"},
		{LegCopyWorktree, "copy_worktree"},
		{LegStartContainer, "start_container"},
		{LegNpmCI, "npm_ci"},
		{LegBuild, "build"},
		{LegRunTests, "run_tests"},
		{LegDrain, "drain"},
		{LegParse, "parse"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			got := tc.leg.String()
			if got == "" {
				t.Errorf("Leg(%d).String() returned empty string", int(tc.leg))
			}
			if got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// TestEventKind_String_ContainsKindName verifies that each EventKind's
// String() returns the expected stable identifier.
func TestEventKind_String_ContainsKindName(t *testing.T) {
	cases := []struct {
		kind EventKind
		want string
	}{
		{EventLegStart, "leg_start"},
		{EventLegSuccess, "leg_success"},
		{EventLegFailure, "leg_failure"},
		{EventChildOutput, "child_output"},
		{EventTestPass, "test_pass"},
		{EventTestFail, "test_fail"},
		{EventReport, "report"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			got := tc.kind.String()
			if got == "" {
				t.Errorf("EventKind(%d).String() returned empty string", int(tc.kind))
			}
			if got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// --- CheckImageVersion dedicated tests ---

// TestCheckImageVersion_MatchingLabel_ReturnsNil verifies that when the
// docker label matches expectedVersion, CheckImageVersion returns nil.
func TestCheckImageVersion_MatchingLabel_ReturnsNil(t *testing.T) {
	stubDocker(t, "v1.2.3\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	err := p.CheckImageVersion(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

// TestCheckImageVersion_MismatchedLabel_ReturnsImageVersionMismatch verifies
// that a label mismatch returns a *LegError wrapping *ImageVersionMismatch.
func TestCheckImageVersion_MismatchedLabel_ReturnsImageVersionMismatch(t *testing.T) {
	stubDocker(t, "v1.2.2\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	err := p.CheckImageVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var mismatch *ImageVersionMismatch
	if !errors.As(legErr.Cause, &mismatch) {
		t.Fatalf("expected Cause=*ImageVersionMismatch, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if mismatch.Expected != "v1.2.3" {
		t.Errorf("expected Expected=%q, got %q", "v1.2.3", mismatch.Expected)
	}
	if mismatch.Actual != "v1.2.2" {
		t.Errorf("expected Actual=%q, got %q", "v1.2.2", mismatch.Actual)
	}
}

// TestCheckImageVersion_EmptyLabel_ReturnsImageVersionMismatchWithEmptyActual
// verifies that a missing label (empty output) produces ImageVersionMismatch
// with Actual == "".
func TestCheckImageVersion_EmptyLabel_ReturnsImageVersionMismatchWithEmptyActual(t *testing.T) {
	stubDocker(t, "", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	err := p.CheckImageVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var mismatch *ImageVersionMismatch
	if !errors.As(legErr.Cause, &mismatch) {
		t.Fatalf("expected Cause=*ImageVersionMismatch, got %T", legErr.Cause)
	}
	if mismatch.Actual != "" {
		t.Errorf("expected Actual=%q (empty), got %q", "", mismatch.Actual)
	}
}

// TestCheckImageVersion_NoSuchImage_ReturnsImageNotPresent verifies that
// docker stderr containing "No such image" maps to *ImageNotPresentError.
func TestCheckImageVersion_NoSuchImage_ReturnsImageNotPresent(t *testing.T) {
	stubDocker(t, "", &dockerexec.ExecError{
		Args:     []string{"image", "inspect", "ghcr.io/foo:v1.2.3"},
		Stderr:   "Error response from daemon: No such image: ghcr.io/foo:v1.2.3",
		ExitCode: 1,
	})
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("ghcr.io/foo:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	err := p.CheckImageVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var notPresent *ImageNotPresentError
	if !errors.As(legErr.Cause, &notPresent) {
		t.Fatalf("expected Cause=*ImageNotPresentError, got %T: %v", legErr.Cause, legErr.Cause)
	}
}

// TestCheckImageVersion_GenericDockerFailure_ReturnsBenchDockerError verifies
// that a generic docker error maps to *BenchDockerError.
func TestCheckImageVersion_GenericDockerFailure_ReturnsBenchDockerError(t *testing.T) {
	stubDocker(t, "", &dockerexec.ExecError{
		Args:     []string{"image", "inspect"},
		Stderr:   "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
		ExitCode: 1,
	})
	p, _ := newTestPipeline(t, 16)

	err := p.CheckImageVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var benchErr *BenchDockerError
	if !errors.As(legErr.Cause, &benchErr) {
		t.Fatalf("expected Cause=*BenchDockerError, got %T: %v", legErr.Cause, legErr.Cause)
	}
}

// TestCheckImageVersion_LocalBench_NoDockerHostPassed verifies that a bench
// with Host="local" passes a bench whose DockerHost() returns "" to dockerInspect.
func TestCheckImageVersion_LocalBench_NoDockerHostPassed(t *testing.T) {
	captured := stubDockerCapture(t, "v1.2.3\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	_ = p.CheckImageVersion(context.Background())
	if captured.DockerHost() != "" {
		t.Errorf("expected empty DockerHost for local bench, got %q", captured.DockerHost())
	}
}

// TestCheckImageVersion_EmptyHost_NoDockerHostPassed verifies that a bench
// with Host="" (empty) also results in an empty DockerHost().
func TestCheckImageVersion_EmptyHost_NoDockerHostPassed(t *testing.T) {
	captured := stubDockerCapture(t, "v1.2.3\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	_ = p.CheckImageVersion(context.Background())
	if captured.DockerHost() != "" {
		t.Errorf("expected empty DockerHost for empty-host bench, got %q", captured.DockerHost())
	}
}

// TestCheckImageVersion_RemoteBench_PassesSSHDockerHost verifies that a
// remote bench has a DockerHost() of "ssh://<bench.Host>".
func TestCheckImageVersion_RemoteBench_PassesSSHDockerHost(t *testing.T) {
	captured := stubDockerCapture(t, "v1.2.3\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench-linux-1", Host: "bench-linux-1", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	_ = p.CheckImageVersion(context.Background())
	want := "ssh://bench-linux-1"
	if captured.DockerHost() != want {
		t.Errorf("expected DockerHost()=%q, got %q", want, captured.DockerHost())
	}
}

// TestCheckImageVersion_EmitsLegSuccessOnMatch verifies that a matching
// version emits LegStart followed by LegSuccess (no LegFailure).
func TestCheckImageVersion_EmitsLegSuccessOnMatch(t *testing.T) {
	stubDocker(t, "v1.2.3\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	err := p.CheckImageVersion(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}

	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (LegStart + LegSuccess), got %d", len(events))
	}
	if events[0].Kind != EventLegStart {
		t.Errorf("expected events[0]=EventLegStart, got %v", events[0].Kind)
	}
	if events[1].Kind != EventLegSuccess {
		t.Errorf("expected events[1]=EventLegSuccess, got %v", events[1].Kind)
	}
}

// TestCheckImageVersion_EmitsLegFailureOnMismatch verifies that a mismatched
// version emits LegStart followed by LegFailure with the mismatch error in Detail.
func TestCheckImageVersion_EmitsLegFailureOnMismatch(t *testing.T) {
	stubDocker(t, "v1.2.2\n", nil)
	ch := make(chan Event, 16)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:v1.2.3"), "v1.2.3", "/tmp/worktree", ch)

	_ = p.CheckImageVersion(context.Background())

	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (LegStart + LegFailure), got %d", len(events))
	}
	if events[0].Kind != EventLegStart {
		t.Errorf("expected events[0]=EventLegStart, got %v", events[0].Kind)
	}
	if events[1].Kind != EventLegFailure {
		t.Errorf("expected events[1]=EventLegFailure, got %v", events[1].Kind)
	}
	if events[1].Detail == "" {
		t.Error("expected non-empty Detail on LegFailure event")
	}
}

// TestCheckImageVersion_PreCanceledContext_DoesNotCallDocker verifies that a
// pre-canceled context causes runLeg to short-circuit before calling docker.
func TestCheckImageVersion_PreCanceledContext_DoesNotCallDocker(t *testing.T) {
	called := false
	original := dockerInspect
	dockerInspect = func(ctx context.Context, b bench.Bench, image string) (string, error) {
		called = true
		return "v1.2.3", nil
	}
	t.Cleanup(func() { dockerInspect = original })

	p, _ := newTestPipeline(t, 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.CheckImageVersion(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	if !errors.Is(legErr.Cause, context.Canceled) {
		t.Errorf("expected Cause=context.Canceled, got %v", legErr.Cause)
	}
	if called {
		t.Error("dockerInspect should not have been called for a pre-canceled context")
	}
}
