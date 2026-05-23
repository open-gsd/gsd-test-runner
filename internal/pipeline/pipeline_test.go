package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// newTestPipeline builds a Pipeline backed by a buffered event channel
// for tests that need to inspect events. Returns the pipeline and channel.
func newTestPipeline(t *testing.T, bufSize int) (*Pipeline, chan Event) {
	t.Helper()
	ch := make(chan Event, bufSize)
	b := bench.Bench{Name: "test-bench", Host: "localhost", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "/tmp/worktree", ch)
	return p, ch
}

// TestNew_NilEventChannelOK verifies that constructing with a nil events
// channel and calling a step method does not panic.
func TestNew_NilEventChannelOK(t *testing.T) {
	b := bench.Bench{Name: "nil-chan-bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "/tmp/worktree", nil)
	// Must not panic even though emit is called internally.
	err := p.CheckImageVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestPipeline_StepMethods_ReturnLegErrorWithNotImplemented table-tests all
// 8 step methods: each must return a *LegError whose Cause is ErrNotImplemented
// and whose Leg field matches the expected leg constant.
func TestPipeline_StepMethods_ReturnLegErrorWithNotImplemented(t *testing.T) {
	type stepCase struct {
		name     string
		leg      Leg
		callStep func(*Pipeline, context.Context) error
	}
	cases := []stepCase{
		{"CheckImageVersion", LegCheckImageVersion, (*Pipeline).CheckImageVersion},
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
// for LegCheckImageVersion (the first leg) and a zero Report.
func TestPipeline_RunAll_StopsAtFirstError(t *testing.T) {
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
	// Report must be zero.
	_ = report // Report is a zero-field struct; any value equals Report{}
}

// TestPipeline_EmitsLegStartBeforeReturning verifies that CheckImageVersion
// emits exactly one EventLegStart event with the correct Leg and OS before
// returning.
func TestPipeline_EmitsLegStartBeforeReturning(t *testing.T) {
	p, ch := newTestPipeline(t, 16)
	_ = p.CheckImageVersion(context.Background())

	// Drain all events from the channel.
	close(ch)
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Kind != EventLegStart {
		t.Errorf("expected EventLegStart, got %v", e.Kind)
	}
	if e.Leg != LegCheckImageVersion {
		t.Errorf("expected Leg=LegCheckImageVersion, got %v", e.Leg)
	}
	if e.OS != "linux" {
		t.Errorf("expected OS=linux, got %q", e.OS)
	}
}

// TestPipeline_PreCanceledContext_ReturnsLegErrorWithContextCause verifies
// that a pre-canceled context surfaces as LegError{Cause: context.Canceled}
// rather than ErrNotImplemented.
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
	// Capacity 1: fills after the first event; all subsequent emits must drop.
	ch := make(chan Event, 1)
	b := bench.Bench{Name: "bench", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "/tmp/worktree", ch)

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
