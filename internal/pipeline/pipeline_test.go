package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
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
// 3 still-stubbed step methods: each must return a *LegError whose Cause is
// ErrNotImplemented and whose Leg field matches the expected leg constant.
// CheckImageVersion is excluded — it has its own dedicated tests below.
// Drain and Parse are excluded — they are implemented in an earlier slice.
// StartContainer and CopyWorktree are excluded — they are implemented in G1.
func TestPipeline_StepMethods_ReturnLegErrorWithNotImplemented(t *testing.T) {
	type stepCase struct {
		name     string
		leg      Leg
		callStep func(*Pipeline, context.Context) error
	}
	cases := []stepCase{
		{"NpmCI", LegNpmCI, (*Pipeline).NpmCI},
		{"Build", LegBuild, (*Pipeline).Build},
		{"RunTests", LegRunTests, (*Pipeline).RunTests},
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

// TestLegExitCode_UniqueAndStable verifies that all 8 legs produce distinct
// exit codes and that the constants have not drifted from their documented
// values per ADR-0004.
func TestLegExitCode_UniqueAndStable(t *testing.T) {
	// Assert exact constant values — these are stable per ADR-0004 and must
	// not change without a corresponding ADR update. Wrapper scripts depend
	// on these values.
	if ExitCodeCheckImageVersion != 10 {
		t.Fatalf("ExitCodeCheckImageVersion = %d, want 10", ExitCodeCheckImageVersion)
	}
	if ExitCodeCopyWorktree != 11 {
		t.Fatalf("ExitCodeCopyWorktree = %d, want 11", ExitCodeCopyWorktree)
	}
	if ExitCodeStartContainer != 12 {
		t.Fatalf("ExitCodeStartContainer = %d, want 12", ExitCodeStartContainer)
	}
	if ExitCodeNpmCI != 13 {
		t.Fatalf("ExitCodeNpmCI = %d, want 13", ExitCodeNpmCI)
	}
	if ExitCodeBuild != 14 {
		t.Fatalf("ExitCodeBuild = %d, want 14", ExitCodeBuild)
	}
	if ExitCodeRunTests != 15 {
		t.Fatalf("ExitCodeRunTests = %d, want 15", ExitCodeRunTests)
	}
	if ExitCodeDrain != 16 {
		t.Fatalf("ExitCodeDrain = %d, want 16", ExitCodeDrain)
	}
	if ExitCodeParse != 17 {
		t.Fatalf("ExitCodeParse = %d, want 17", ExitCodeParse)
	}

	// Assert uniqueness: no two legs may share an exit code.
	legs := []Leg{
		LegCheckImageVersion,
		LegCopyWorktree,
		LegStartContainer,
		LegNpmCI,
		LegBuild,
		LegRunTests,
		LegDrain,
		LegParse,
	}
	seen := make(map[int]Leg)
	for _, leg := range legs {
		code := legExitCode(leg)
		if prev, dup := seen[code]; dup {
			t.Errorf("exit code %d is shared by legs %v and %v", code, prev, leg)
		}
		seen[code] = leg
	}

	// Assert legExitCode matches the constants for each leg.
	cases := []struct {
		leg  Leg
		want int
	}{
		{LegCheckImageVersion, ExitCodeCheckImageVersion},
		{LegCopyWorktree, ExitCodeCopyWorktree},
		{LegStartContainer, ExitCodeStartContainer},
		{LegNpmCI, ExitCodeNpmCI},
		{LegBuild, ExitCodeBuild},
		{LegRunTests, ExitCodeRunTests},
		{LegDrain, ExitCodeDrain},
		{LegParse, ExitCodeParse},
	}
	for _, tc := range cases {
		got := legExitCode(tc.leg)
		if got != tc.want {
			t.Errorf("legExitCode(%v) = %d, want %d", tc.leg, got, tc.want)
		}
	}
}

// TestDrain_PopulatesDiagPath_OnDockerCpFailure verifies that when docker cp
// fails after the temp file is created, LegError.DiagPath is set to the
// local temp file path (partial data may exist on disk for diagnosis).
func TestDrain_PopulatesDiagPath_OnDockerCpFailure(t *testing.T) {
	execErr := &dockerexec.ExecError{
		Args:     []string{"cp", "ctr:/work/test-events.jsonl", "/tmp/x"},
		Stderr:   "Error: No such container: ctr",
		ExitCode: 1,
	}
	stubDockerCp(t, "", execErr)

	p, _ := newTestPipeline(t, 16)
	p.containerID = "ctr"

	err := p.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}

	// DiagPath must be set to the local temp file path.
	if legErr.DiagPath == "" {
		t.Error("expected LegError.DiagPath to be non-empty after docker cp failure")
	}

	// Clean up the temp file that Drain left on disk (kept for diagnosis).
	if legErr.DiagPath != "" {
		os.Remove(legErr.DiagPath)
	}
}

// stubDockerRun swaps the package-level dockerRun for a function returning
// the given output/error. Restored via t.Cleanup.
func stubDockerRun(t *testing.T, out string, err error) {
	t.Helper()
	original := dockerRun
	dockerRun = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
		return out, err
	}
	t.Cleanup(func() { dockerRun = original })
}

// stubDockerRmCapture swaps dockerRm for a stub that records the args it was
// called with. Restored via t.Cleanup.
func stubDockerRmCapture(t *testing.T) *[][]string {
	t.Helper()
	original := dockerRm
	var calls [][]string
	dockerRm = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	t.Cleanup(func() { dockerRm = original })
	return &calls
}

// --- StartContainer dedicated tests ---

// TestStartContainer_Success verifies that when dockerRun succeeds,
// p.containerID is set to the trimmed stdout and the leg returns nil.
func TestStartContainer_Success(t *testing.T) {
	stubDockerRun(t, "abc123def456\n", nil)
	stubDockerRmCapture(t) // suppress real docker rm in deferred cleanup

	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if p.containerID != "abc123def456" {
		t.Errorf("expected containerID=%q, got %q", "abc123def456", p.containerID)
	}
}

// TestStartContainer_NoSuchImage verifies that when dockerRun returns an
// ExecError with an image-not-found stderr, the leg returns a *LegError
// wrapping *ContainerStartError.
func TestStartContainer_NoSuchImage(t *testing.T) {
	execErr := &dockerexec.ExecError{
		Args:     []string{"run", "--rm", "-d"},
		Stderr:   "Unable to find image 'foo:bar' locally",
		ExitCode: 125,
	}
	stubDockerRun(t, "", execErr)
	stubDockerRmCapture(t)

	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if legErr.Leg != LegStartContainer {
		t.Errorf("expected Leg=LegStartContainer, got %v", legErr.Leg)
	}
	var startErr *ContainerStartError
	if !errors.As(legErr.Cause, &startErr) {
		t.Fatalf("expected Cause=*ContainerStartError, got %T: %v", legErr.Cause, legErr.Cause)
	}
}

// TestStartContainer_DaemonDown verifies that when dockerRun returns an
// ExecError containing "Cannot connect to the Docker daemon", the leg
// returns a *LegError wrapping *BenchDockerError.
func TestStartContainer_DaemonDown(t *testing.T) {
	execErr := &dockerexec.ExecError{
		Args:     []string{"run", "--rm", "-d"},
		Stderr:   "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
		ExitCode: 1,
	}
	stubDockerRun(t, "", execErr)
	stubDockerRmCapture(t)

	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	var benchErr *BenchDockerError
	if !errors.As(legErr.Cause, &benchErr) {
		t.Fatalf("expected Cause=*BenchDockerError, got %T: %v", legErr.Cause, legErr.Cause)
	}
}

// --- CopyWorktree dedicated tests ---

// TestCopyWorktree_Success seeds containerID and work, stubs dockerCp to
// succeed, and asserts no error is returned.
func TestCopyWorktree_Success(t *testing.T) {
	p, _ := newTestPipeline(t, 16)
	p.containerID = "test-container-xyz"
	// p.work is already set to "/tmp/worktree" by newTestPipeline via New().

	stubDockerCp(t, "", nil)

	err := p.CopyWorktree(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// TestCopyWorktree_NoContainerID verifies that calling CopyWorktree without a
// containerID returns a *LegError wrapping *CopyInError whose Cause mentions
// "containerID is empty".
func TestCopyWorktree_NoContainerID(t *testing.T) {
	p, _ := newTestPipeline(t, 16)
	// containerID is empty — StartContainer was never called.

	err := p.CopyWorktree(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if legErr.Leg != LegCopyWorktree {
		t.Errorf("expected Leg=LegCopyWorktree, got %v", legErr.Leg)
	}
	var copyErr *CopyInError
	if !errors.As(legErr.Cause, &copyErr) {
		t.Fatalf("expected Cause=*CopyInError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if !strings.Contains(copyErr.Error(), "containerID is empty") {
		t.Errorf("expected error message to mention 'containerID is empty', got: %v", copyErr)
	}
}

// TestCopyWorktree_DockerCpFails verifies that when dockerCp fails, the leg
// returns a *LegError wrapping *CopyInError that wraps the docker error.
func TestCopyWorktree_DockerCpFails(t *testing.T) {
	execErr := &dockerexec.ExecError{
		Args:     []string{"cp", "/tmp/worktree/.", "test-container:/work"},
		Stderr:   "Error: No such container: test-container",
		ExitCode: 1,
	}
	stubDockerCp(t, "", execErr)

	p, _ := newTestPipeline(t, 16)
	p.containerID = "test-container"

	err := p.CopyWorktree(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	var copyErr *CopyInError
	if !errors.As(legErr.Cause, &copyErr) {
		t.Fatalf("expected Cause=*CopyInError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	var gotExecErr *dockerexec.ExecError
	if !errors.As(copyErr.Cause, &gotExecErr) {
		t.Fatalf("expected CopyInError.Cause=*dockerexec.ExecError, got %T: %v", copyErr.Cause, copyErr.Cause)
	}
}

// --- RunAll cleanup tests ---

// TestRunAll_CleanupRunsAfterSuccess verifies that the Cleanup method invokes
// dockerRm with the container ID when containerID is non-empty.
func TestRunAll_CleanupRunsAfterSuccess(t *testing.T) {
	rmCalls := stubDockerRmCapture(t)

	p, _ := newTestPipeline(t, 16)
	p.containerID = "container-to-clean"

	p.Cleanup(context.Background())

	if len(*rmCalls) != 1 {
		t.Fatalf("expected dockerRm called once, got %d times", len(*rmCalls))
	}
	found := false
	for _, arg := range (*rmCalls)[0] {
		if arg == "container-to-clean" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dockerRm to be called with container ID 'container-to-clean', got args: %v", (*rmCalls)[0])
	}
}

// TestRunAll_CleanupRunsAfterFailure verifies that RunAll defers cleanup even
// when StartContainer succeeds but a later leg fails. We stub dockerRun to
// return a container ID, stub dockerCp to fail so CopyWorktree fails, and
// assert dockerRm is still called with the container ID.
func TestRunAll_CleanupRunsAfterFailure(t *testing.T) {
	stubDockerRun(t, "cleanup-test-container\n", nil)
	rmCalls := stubDockerRmCapture(t)
	// stub dockerInspect so CheckImageVersion passes
	stubDocker(t, "v0.0.0-test\n", nil)
	// stub dockerCp to fail so CopyWorktree fails
	stubDockerCp(t, "", &dockerexec.ExecError{
		Args:     []string{"cp"},
		Stderr:   "no such container",
		ExitCode: 1,
	})

	p, ch := newTestPipeline(t, 64)
	// Drain the event channel so Pipeline doesn't block.
	go func() {
		for range ch {
		}
	}()

	_, err := p.RunAll(context.Background())
	close(ch)

	if err == nil {
		t.Fatal("expected error from RunAll (CopyWorktree should fail), got nil")
	}

	// dockerRm must have been called (by the deferred cleanup in RunAll).
	if len(*rmCalls) == 0 {
		t.Fatal("expected dockerRm to be called by RunAll deferred cleanup, got 0 calls")
	}
	found := false
	for _, arg := range (*rmCalls)[0] {
		if arg == "cleanup-test-container" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dockerRm to be called with 'cleanup-test-container', got args: %v", (*rmCalls)[0])
	}
}

// --- Drain leg dedicated tests ---

// stubDockerCp swaps the package-level dockerCp for a function returning the
// given output/error. Restored via t.Cleanup.
func stubDockerCp(t *testing.T, out string, err error) {
	t.Helper()
	original := dockerCp
	dockerCp = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
		return out, err
	}
	t.Cleanup(func() { dockerCp = original })
}

// TestDrain_Success verifies that when dockerCp succeeds, p.drainedPath is
// set to a non-empty path and LegStart + LegSuccess events are emitted.
func TestDrain_Success(t *testing.T) {
	// The stub must actually write a file so the path is valid for later
	// use by Parse. For Drain's own test, we just need dockerCp to not
	// error; the temp file was already created before dockerCp is called,
	// and docker cp overwrites it. We simulate by doing nothing (the temp
	// file created inside Drain will remain as-is).
	stubDockerCp(t, "", nil)

	p, ch := newTestPipeline(t, 16)
	p.containerID = "test-container-xyz"

	err := p.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if p.drainedPath == "" {
		t.Error("expected p.drainedPath to be set after successful Drain")
	}

	// Clean up the temp file created by Drain.
	if p.drainedPath != "" {
		os.Remove(p.drainedPath)
	}

	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (LegStart + LegSuccess), got %d", len(events))
	}
	if events[0].Kind != EventLegStart || events[0].Leg != LegDrain {
		t.Errorf("events[0]: expected LegStart/LegDrain, got Kind=%v Leg=%v", events[0].Kind, events[0].Leg)
	}
	if events[1].Kind != EventLegSuccess || events[1].Leg != LegDrain {
		t.Errorf("events[1]: expected LegSuccess/LegDrain, got Kind=%v Leg=%v", events[1].Kind, events[1].Leg)
	}
}

// TestDrain_DockerCpFails verifies that a dockerCp failure returns a *LegError
// wrapping *DrainError wrapping the original exec error, and p.drainedPath
// remains empty (temp file is cleaned up).
func TestDrain_DockerCpFails(t *testing.T) {
	execErr := &dockerexec.ExecError{
		Args:     []string{"cp", "test-container:/work/test-events.jsonl", "/tmp/x"},
		Stderr:   "Error: No such container: test-container",
		ExitCode: 1,
	}
	stubDockerCp(t, "", execErr)

	p, _ := newTestPipeline(t, 16)
	p.containerID = "test-container"

	err := p.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if legErr.Leg != LegDrain {
		t.Errorf("expected Leg=LegDrain, got %v", legErr.Leg)
	}

	var drainErr *DrainError
	if !errors.As(legErr.Cause, &drainErr) {
		t.Fatalf("expected Cause=*DrainError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if drainErr.Stage != "docker_cp" {
		t.Errorf("expected Stage=%q, got %q", "docker_cp", drainErr.Stage)
	}

	var gotExecErr *dockerexec.ExecError
	if !errors.As(drainErr.Cause, &gotExecErr) {
		t.Fatalf("expected DrainError.Cause=*dockerexec.ExecError, got %T", drainErr.Cause)
	}

	// Temp file should be cleaned up on failure.
	if p.drainedPath != "" {
		t.Errorf("expected p.drainedPath to be empty after failed Drain, got %q", p.drainedPath)
	}
}

// --- Parse leg dedicated tests ---

// TestParse_ReadsFromDrainedPath verifies that Parse reads the file at
// p.drainedPath and populates p.result.Total/Passed/Failures correctly.
func TestParse_ReadsFromDrainedPath(t *testing.T) {
	// Write sample JSONL to a temp file.
	content := `{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}
{"type":"test_event","kind":"fail","name":"test B","file":"b.test.js","error":"oops","error_class":"throw"}
`
	f, err := os.CreateTemp("", "gsd-parse-test-*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	f.Close()

	p, _ := newTestPipeline(t, 16)
	p.drainedPath = f.Name()

	if err := p.Parse(context.Background()); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if p.result.Total != 2 {
		t.Errorf("expected result.Total=2, got %d", p.result.Total)
	}
	if p.result.Passed != 1 {
		t.Errorf("expected result.Passed=1, got %d", p.result.Passed)
	}
	if p.result.Failed != 1 {
		t.Errorf("expected result.Failed=1, got %d", p.result.Failed)
	}
	if len(p.result.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(p.result.Failures))
	}
	if p.result.Failures[0].Name != "test B" {
		t.Errorf("expected failure name %q, got %q", "test B", p.result.Failures[0].Name)
	}
}

// TestParse_NoDrainedPath verifies that calling Parse without Drain having run
// returns a *LegError wrapping *ParseError.
func TestParse_NoDrainedPath(t *testing.T) {
	p, _ := newTestPipeline(t, 16)
	// p.drainedPath is empty — Drain was not called.

	err := p.Parse(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}
	if legErr.Leg != LegParse {
		t.Errorf("expected Leg=LegParse, got %v", legErr.Leg)
	}

	var parseErr *ParseError
	if !errors.As(legErr.Cause, &parseErr) {
		t.Fatalf("expected Cause=*ParseError, got %T: %v", legErr.Cause, legErr.Cause)
	}
}

// TestRunAll_DrainPlusParse_EventOrdering verifies the Drain + Parse leg event
// ordering using stubs for all preceding legs. Because RunAll must run all 8
// legs in order and the first 6 still return ErrNotImplemented, we verify that
// when CopyWorktree fails (leg 2), only legs 1+2 events are emitted. To test
// Drain+Parse events specifically we exercise them individually.
func TestRunAll_DrainPlusParse_EventOrdering(t *testing.T) {
	// Write sample JSONL for Parse to consume.
	content := `{"type":"test_event","kind":"pass","name":"test A","file":"a.test.js"}
`
	f, err := os.CreateTemp("", "gsd-runall-test-*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("failed to write temp file: %v", err)
	}
	f.Close()

	stubDockerCp(t, "", nil)
	p, ch := newTestPipeline(t, 32)
	p.containerID = "test-container-xyz"
	// Seed drainedPath so we can call Parse independently.
	p.drainedPath = f.Name()

	// Call Drain and Parse directly to verify their event ordering without
	// needing the preceding 6 stubs to succeed.
	if err := p.Drain(context.Background()); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
	// Override drainedPath back to our test file (Drain overwrites it with
	// the stub's temp file path which is empty content).
	if p.drainedPath != "" {
		os.Remove(p.drainedPath)
	}
	p.drainedPath = f.Name()

	if err := p.Parse(context.Background()); err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	// Expect: LegStart(Drain), LegSuccess(Drain), LegStart(Parse), LegSuccess(Parse).
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(events), events)
	}
	if events[0].Kind != EventLegStart || events[0].Leg != LegDrain {
		t.Errorf("events[0]: expected LegStart/Drain, got Kind=%v Leg=%v", events[0].Kind, events[0].Leg)
	}
	if events[1].Kind != EventLegSuccess || events[1].Leg != LegDrain {
		t.Errorf("events[1]: expected LegSuccess/Drain, got Kind=%v Leg=%v", events[1].Kind, events[1].Leg)
	}
	if events[2].Kind != EventLegStart || events[2].Leg != LegParse {
		t.Errorf("events[2]: expected LegStart/Parse, got Kind=%v Leg=%v", events[2].Kind, events[2].Leg)
	}
	if events[3].Kind != EventLegSuccess || events[3].Leg != LegParse {
		t.Errorf("events[3]: expected LegSuccess/Parse, got Kind=%v Leg=%v", events[3].Kind, events[3].Leg)
	}

	// Verify Parse populated the result.
	if p.result.Total != 1 {
		t.Errorf("expected result.Total=1, got %d", p.result.Total)
	}
	if p.result.Passed != 1 {
		t.Errorf("expected result.Passed=1, got %d", p.result.Passed)
	}
}

// TestParse_ZeroEventsInDrainedFile verifies that a drained file with no
// test_event records causes Parse to return a *LegError wrapping *ParseError
// wrapping *ZeroEventsError.
func TestParse_ZeroEventsInDrainedFile(t *testing.T) {
	content := `{"type":"suite_start","suite":"my-suite"}
`
	f, err := os.CreateTemp("", "gsd-parse-zero-test-*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("failed to write temp file: %v", err)
	}
	f.Close()

	p, _ := newTestPipeline(t, 16)
	p.drainedPath = f.Name()

	err = p.Parse(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}

	var parseErr *ParseError
	if !errors.As(legErr.Cause, &parseErr) {
		t.Fatalf("expected Cause=*ParseError, got %T: %v", legErr.Cause, legErr.Cause)
	}

	var zeroErr *ZeroEventsError
	if !errors.As(parseErr.Cause, &zeroErr) {
		t.Fatalf("expected ParseError.Cause=*ZeroEventsError, got %T: %v", parseErr.Cause, parseErr.Cause)
	}
}

// TestDrain_ContainerIDUsedInDockerCpArgs verifies the container ID is
// incorporated into the docker cp source argument as "<containerID>:<path>".
func TestDrain_ContainerIDUsedInDockerCpArgs(t *testing.T) {
	var capturedArgs []string
	original := dockerCp
	dockerCp = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
		capturedArgs = append(capturedArgs, args...)
		return "", nil
	}
	t.Cleanup(func() { dockerCp = original })

	p, _ := newTestPipeline(t, 16)
	p.containerID = "my-container-abc"

	err := p.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if p.drainedPath != "" {
		os.Remove(p.drainedPath)
	}

	if len(capturedArgs) < 1 {
		t.Fatal("expected dockerCp to be called with args")
	}
	wantSrc := "my-container-abc:" + containerJSONLPath
	if capturedArgs[0] != wantSrc {
		t.Errorf("expected first arg %q, got %q", wantSrc, capturedArgs[0])
	}
}

// TestParse_MalformedJSONLInDrainedFile verifies that malformed JSON in the
// drained file causes a *LegError wrapping *ParseError wrapping *MalformedJSONLError.
func TestParse_MalformedJSONLInDrainedFile(t *testing.T) {
	content := strings.Join([]string{
		`{"type":"test_event","kind":"pass","name":"ok","file":"a.js"}`,
		`not valid json`,
	}, "\n") + "\n"

	f, err := os.CreateTemp("", "gsd-parse-malformed-*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("failed to write temp file: %v", err)
	}
	f.Close()

	p, _ := newTestPipeline(t, 16)
	p.drainedPath = f.Name()

	err = p.Parse(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T: %v", err, err)
	}

	var parseErr *ParseError
	if !errors.As(legErr.Cause, &parseErr) {
		t.Fatalf("expected Cause=*ParseError, got %T: %v", legErr.Cause, legErr.Cause)
	}

	var malformedErr *MalformedJSONLError
	if !errors.As(parseErr.Cause, &malformedErr) {
		t.Fatalf("expected ParseError.Cause=*MalformedJSONLError, got %T: %v", parseErr.Cause, parseErr.Cause)
	}
	if malformedErr.Line != 2 {
		t.Errorf("expected MalformedJSONLError.Line=2, got %d", malformedErr.Line)
	}
}
