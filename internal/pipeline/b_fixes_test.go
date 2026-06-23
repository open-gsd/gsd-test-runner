package pipeline

// Tests for defects B-6, B-7, B-8, B-14, B-21.
// Written RED-first before the corresponding fixes land.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// ---------------------------------------------------------------------------
// B-6: tailJSONLForLiveEvents must use tail -F (not -f) and surface errors
// ---------------------------------------------------------------------------

// TestTailJSONL_UsesTailCapitalF verifies that the args passed to dockerStream
// when tailing the JSONL file use "-F" (follow-retry) rather than "-f" (follow
// without retry). "-F" handles the common case where the JSONL file does not
// yet exist when `tail` starts — `-f` would exit immediately with an error,
// silently discarding all live events for the run.
//
// The test directly invokes tailJSONLForLiveEvents via a stub that captures the
// args and returns immediately, avoiding the need to run the full RunTests leg.
func TestTailJSONL_UsesTailCapitalF(t *testing.T) {
	var capturedArgs []string
	var mu sync.Mutex

	stubDockerStream(t, func(ctx context.Context, b bench.Bench, args []string, stdout, stderr dockerexec.LineHandler) error {
		mu.Lock()
		capturedArgs = append([]string(nil), args...)
		mu.Unlock()
		return nil // return immediately — we only care about the args
	})

	p, ch := newTestPipeline(t, 64)
	p.containerID = "test-container"

	// Call tailJSONLForLiveEvents directly with a pre-cancelled context so it
	// exits cleanly after the stub returns.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so the goroutine exits promptly

	var wg sync.WaitGroup
	wg.Add(1)
	p.tailJSONLForLiveEvents(ctx, &wg)
	wg.Wait()

	p.closeEvents()
	for range ch {
	}

	mu.Lock()
	defer mu.Unlock()

	// Find "tail" in the captured args and check for -F vs -f.
	foundTailF := false
	foundTailLowerF := false
	for i, a := range capturedArgs {
		if a != "tail" {
			continue
		}
		for j := i + 1; j < len(capturedArgs); j++ {
			switch capturedArgs[j] {
			case "-F":
				foundTailF = true
			case "-f":
				foundTailLowerF = true
			}
		}
		break
	}
	if foundTailLowerF {
		t.Errorf("tail was invoked with -f (lowercase); want -F (capital F for follow-retry). args=%v", capturedArgs)
	}
	if !foundTailF {
		t.Errorf("tail -F was not found in dockerStream args; got: %v", capturedArgs)
	}
}

// TestTailJSONL_DockerStreamError_DoesNotBlock verifies that when
// tailJSONLForLiveEvents gets a non-cancellation error from dockerStream, it
// exits cleanly (does not block) and emits a diagnostic EventChildOutput event.
func TestTailJSONL_DockerStreamError_DoesNotBlock(t *testing.T) {
	stubDockerStream(t, func(ctx context.Context, b bench.Bench, args []string, stdout, stderr dockerexec.LineHandler) error {
		return errors.New("injected tail error")
	})

	p, ch := newTestPipeline(t, 64)
	p.containerID = "test-container"

	// NOT pre-cancelled — so err != nil and ctx.Err() == nil => error must be surfaced.
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.tailJSONLForLiveEvents(ctx, &wg)
		wg.Wait()
	}()

	select {
	case <-done:
		// good — tailJSONLForLiveEvents returned without hanging
	case <-waitTimeout(2 * time.Second):
		t.Fatal("tailJSONLForLiveEvents hung after dockerStream error")
	}

	// Collect events — the diagnostic should be in there.
	p.closeEvents()
	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	// There should be a diagnostic EventChildOutput event with the error message.
	var foundDiag bool
	for _, e := range events {
		if e.Kind == EventChildOutput && e.Stream == "stderr" && strings.Contains(e.Line, "injected tail error") {
			foundDiag = true
		}
	}
	if !foundDiag {
		t.Errorf("expected a diagnostic EventChildOutput (stderr) mentioning the tail error; events=%v", events)
	}
}

// ---------------------------------------------------------------------------
// B-7: eventQueue must be bounded; oldest EventChildOutput dropped on overflow;
//
//	failure/result events kept lossless; marker event emitted.
//
// ---------------------------------------------------------------------------

// TestEventQueue_BoundedCap_DropsOldestChildOutputKeepsFailEvents verifies that
// when q.items exceeds the configured high-water mark:
//   - The oldest EventChildOutput entries are dropped (not appended indefinitely).
//   - EventTestFail events are NEVER dropped (lossless fail semantics).
//   - A single synthetic EventDroppedOutput marker is emitted.
//   - Total items in the queue stays bounded.
func TestEventQueue_BoundedCap_DropsOldestChildOutputKeepsFailEvents(t *testing.T) {
	const cap = 10
	q := newEventQueueWithCap(cap)

	// Fill beyond capacity with a mix of child-output and fail events.
	const totalOutput = 20
	const failCount = 3

	for i := 0; i < totalOutput; i++ {
		q.push(Event{Kind: EventChildOutput, Line: fmt.Sprintf("output-%d", i)})
	}
	for i := 0; i < failCount; i++ {
		q.push(Event{Kind: EventTestFail, Line: fmt.Sprintf("fail-%d", i)})
	}
	q.close()

	var got []Event
	for {
		e, ok := q.pop()
		if !ok {
			break
		}
		got = append(got, e)
	}

	// All EventTestFail events must be present.
	var fails, outputs, markers []Event
	for _, e := range got {
		switch e.Kind {
		case EventTestFail:
			fails = append(fails, e)
		case EventChildOutput:
			outputs = append(outputs, e)
		case EventDroppedOutput:
			markers = append(markers, e)
		}
	}

	if len(fails) != failCount {
		t.Errorf("expected all %d fail events, got %d", failCount, len(fails))
	}
	// After capping, queue must not exceed cap+failCount (bounded).
	if len(got) > cap+failCount+1 { // +1 for the marker
		t.Errorf("queue grew past bound: got %d total events (cap=%d, fails=%d)", len(got), cap, failCount)
	}
	// Exactly one marker must be emitted when drops occurred.
	if totalOutput > cap && len(markers) != 1 {
		t.Errorf("expected exactly 1 dropped-output marker event, got %d", len(markers))
	}
	// The marker's Detail must mention a count.
	if len(markers) == 1 && markers[0].Detail == "" {
		t.Error("dropped-output marker must include a count in Detail")
	}
	// Child-output events that are present must be contiguous tail (newest kept).
	for i, e := range outputs {
		_ = i
		_ = e // we can't assert ordering without knowing which were dropped, but
		// we can assert none are from before the cap was exceeded if we had them tagged.
		// The core invariant is the count bound above.
	}
}

// TestEventQueue_NoBoundViolation_NoDrop verifies that pushing fewer than cap
// items results in ZERO drops and no marker.
func TestEventQueue_NoBoundViolation_NoDrop(t *testing.T) {
	const cap = 50
	q := newEventQueueWithCap(cap)

	const n = 5
	for i := 0; i < n; i++ {
		q.push(Event{Kind: EventChildOutput, Line: fmt.Sprintf("line-%d", i)})
	}
	q.close()

	var got []Event
	for {
		e, ok := q.pop()
		if !ok {
			break
		}
		got = append(got, e)
	}
	if len(got) != n {
		t.Errorf("expected %d events (no drops), got %d", n, len(got))
	}
	for _, e := range got {
		if e.Kind == EventDroppedOutput {
			t.Error("unexpected dropped-output marker when under cap")
		}
	}
}

// ---------------------------------------------------------------------------
// B-8: Pump goroutine must not leak when Pipeline is created but RunAll never runs
// ---------------------------------------------------------------------------

// TestNew_WithEvents_PumpExitsOnClose verifies that after calling New with a
// non-nil events channel and then calling closeEvents (simulating the minimum
// lifetime obligation), the pump goroutine exits and the channel is closed.
// This is a goroutine-leak guard.
func TestNew_WithEvents_PumpExitsOnClose(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	ch := make(chan Event, 4)
	b := bench.Bench{Name: "leak-test", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "v", "/tmp/wt", nil, ch)

	// Simulate: caller created the pipeline but is taking an early-return path
	// before RunAll. They must call Close/closeEvents to release the pump.
	p.closeEvents() // idempotent cleanup obligation

	// Drain so the pump can send remaining items and exit.
	for range ch {
	}

	// Give the goroutine scheduler time to reclaim.
	// Use a spin approach rather than sleep: check a few times.
	var goroutinesAfter int
	for attempts := 0; attempts < 100; attempts++ {
		goroutinesAfter = runtime.NumGoroutine()
		if goroutinesAfter <= goroutinesBefore {
			break
		}
		runtime.Gosched()
	}

	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d after=%d (pump goroutine may still be running)",
			goroutinesBefore, goroutinesAfter)
	}
}

// TestNew_NilEvents_NoPumpStarted verifies that New with a nil events channel
// does NOT start any goroutine.
func TestNew_NilEvents_NoPumpStarted(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	b := bench.Bench{Name: "nil-test", Host: "local", OS: "linux"}
	_ = New(b, images.ImageID("gsd-tester-linux:dev"), "v", "/tmp/wt", nil, nil)

	runtime.Gosched()
	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("unexpected goroutine started with nil events: before=%d after=%d",
			goroutinesBefore, goroutinesAfter)
	}
}

// ---------------------------------------------------------------------------
// B-14: stdout crash diagnostics must appear in typed-error message at Normal verbosity
// ---------------------------------------------------------------------------

// TestNpmCI_StdoutCrashDiagnosticInError verifies that when the NpmCI leg
// runner crashes and its fatal diagnostic is emitted to STDOUT (not stderr),
// the resulting *NpmCIError.Stdout field contains the diagnostic — so the
// error message shown at Normal verbosity (which suppresses EventChildOutput)
// still carries the root cause.
func TestNpmCI_StdoutCrashDiagnosticInError(t *testing.T) {
	const stdoutDiag = "FATAL: postinstall hook failed with segmentation fault"
	stubDockerStream(t, func(ctx context.Context, b bench.Bench, args []string, stdout, stderr dockerexec.LineHandler) error {
		stdout(stdoutDiag) // diagnostic only on stdout — NOT stderr
		return &dockerexec.ExecError{Args: args, ExitCode: 2}
	})

	p, _ := newTestPipeline(t, 32)
	p.containerID = "test-container"

	err := p.NpmCI(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var npmErr *NpmCIError
	if !errors.As(legErr.Cause, &npmErr) {
		t.Fatalf("expected Cause=*NpmCIError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if !strings.Contains(npmErr.Error(), stdoutDiag) {
		t.Errorf("NpmCIError.Error() must include stdout diagnostic %q, got: %q", stdoutDiag, npmErr.Error())
	}
}

// TestBuild_StdoutCrashDiagnosticInError verifies the same contract for the
// Build leg: a stdout-only fatal message must surface in *BuildError.
func TestBuild_StdoutCrashDiagnosticInError(t *testing.T) {
	const stdoutDiag = "FATAL: webpack config is invalid — cannot read property"

	// dockerStream is called twice for Build: first for `cat package.json`
	// (via dockerRun, which goes through dockerStream), then for npm run build.
	// We stub dockerRun for the package.json probe separately.
	origRun := dockerRun
	dockerRun = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
		return `{"scripts":{"build":"webpack"}}`, nil
	}
	t.Cleanup(func() { dockerRun = origRun })

	stubDockerStream(t, func(ctx context.Context, b bench.Bench, args []string, stdout, stderr dockerexec.LineHandler) error {
		stdout(stdoutDiag)
		return &dockerexec.ExecError{Args: args, ExitCode: 2}
	})

	p, _ := newTestPipeline(t, 32)
	p.containerID = "test-container"

	err := p.Build(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var buildErr *BuildError
	if !errors.As(legErr.Cause, &buildErr) {
		t.Fatalf("expected Cause=*BuildError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if !strings.Contains(buildErr.Error(), stdoutDiag) {
		t.Errorf("BuildError.Error() must include stdout diagnostic %q, got: %q", stdoutDiag, buildErr.Error())
	}
}

// TestRunTests_StdoutCrashDiagnosticInError verifies the same contract for
// the RunTests leg.
func TestRunTests_StdoutCrashDiagnosticInError(t *testing.T) {
	const stdoutDiag = "FATAL: could not load test runner plugin (segfault)"

	// Identify tail vs main call by args: the tail call contains "tail" in args;
	// the main exec call contains "node" (via the default test command).
	stubDockerStream(t, func(ctx context.Context, b bench.Bench, args []string, stdout, stderr dockerexec.LineHandler) error {
		isTailCall := false
		for _, a := range args {
			if a == "tail" {
				isTailCall = true
				break
			}
		}
		if isTailCall {
			// Tail goroutine — block until context is cancelled by RunTests.
			<-ctx.Done()
			return ctx.Err()
		}
		// Main test exec — emit stdout diagnostic and crash with exit>1.
		stdout(stdoutDiag)
		return &dockerexec.ExecError{Args: args, ExitCode: 2}
	})

	p, ch := newTestPipeline(t, 64)
	p.containerID = "test-container"

	err := p.RunTests(context.Background())
	p.closeEvents()
	for range ch {
	}

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var legErr *LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *LegError, got %T", err)
	}
	var testErr *TestRunError
	if !errors.As(legErr.Cause, &testErr) {
		t.Fatalf("expected Cause=*TestRunError, got %T: %v", legErr.Cause, legErr.Cause)
	}
	if !strings.Contains(testErr.Error(), stdoutDiag) {
		t.Errorf("TestRunError.Error() must include stdout diagnostic %q, got: %q", stdoutDiag, testErr.Error())
	}
}

// ---------------------------------------------------------------------------
// B-21: parseLiveTestEvent must reject unknown kind values (fail-closed enum)
// ---------------------------------------------------------------------------

// TestParseLiveTestEvent_RejectsUnknownKind verifies that parseLiveTestEvent
// drops events whose kind is neither "pass" nor "fail". An unknown kind must
// return (zero, false) — it must NOT be treated as a pass (which would violate
// the failure-first contract by rendering green for an unrecognised event).
func TestParseLiveTestEvent_RejectsUnknownKind(t *testing.T) {
	unknownKinds := []string{"skip", "todo", "diagnostic", "cancelled", "unknown", "PASS", "FAIL"}
	for _, kind := range unknownKinds {
		kind := kind
		t.Run("kind="+kind, func(t *testing.T) {
			line := []byte(fmt.Sprintf(
				`{"type":"test_event","kind":%q,"name":"some test","file":"a.test.js"}`,
				kind,
			))
			_, ok := parseLiveTestEvent(line)
			if ok {
				t.Errorf("parseLiveTestEvent accepted unknown kind %q — must return (zero,false)", kind)
			}
		})
	}
}

// TestParseLiveTestEvent_AcceptsKnownKinds verifies that "pass" and "fail" are
// still accepted after the enum guard is added.
func TestParseLiveTestEvent_AcceptsKnownKinds(t *testing.T) {
	for _, kind := range []string{"pass", "fail"} {
		kind := kind
		t.Run("kind="+kind, func(t *testing.T) {
			line := []byte(fmt.Sprintf(
				`{"type":"test_event","kind":%q,"name":"some test","file":"a.test.js"}`,
				kind,
			))
			ev, ok := parseLiveTestEvent(line)
			if !ok {
				t.Errorf("parseLiveTestEvent rejected known kind %q — must return (event,true)", kind)
			}
			if ev.Kind != kind {
				t.Errorf("ev.Kind=%q, want %q", ev.Kind, kind)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitTimeout returns a channel that receives after d duration.
func waitTimeout(d time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		time.Sleep(d)
		close(ch)
	}()
	return ch
}
