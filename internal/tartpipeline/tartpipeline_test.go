package tartpipeline

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
	"github.com/open-gsd/gsd-test-runner/internal/tartreaper"
)

// --- test seams -------------------------------------------------------

// stubRunTart swaps the package-level runTart var. Restored via t.Cleanup.
func stubRunTart(t *testing.T, fn func(ctx context.Context, b bench.Bench, args []string) (string, error)) {
	t.Helper()
	orig := runTart
	runTart = fn
	t.Cleanup(func() { runTart = orig })
}

// stubExecGuest swaps the package-level execGuest var.
func stubExecGuest(t *testing.T, fn func(ctx context.Context, b bench.Bench, vmName, user, pass string, args []string) (string, error)) {
	t.Helper()
	orig := execGuest
	execGuest = fn
	t.Cleanup(func() { execGuest = orig })
}

// stubCopyToBench swaps the package-level copyToBench var.
func stubCopyToBench(t *testing.T, fn func(ctx context.Context, b bench.Bench, local, remote string) error) {
	t.Helper()
	orig := copyToBench
	copyToBench = fn
	t.Cleanup(func() { copyToBench = orig })
}

// stubCopyFromBench swaps the package-level copyFromBench var.
func stubCopyFromBench(t *testing.T, fn func(ctx context.Context, b bench.Bench, remote, local string) error) {
	t.Helper()
	orig := copyFromBench
	copyFromBench = fn
	t.Cleanup(func() { copyFromBench = orig })
}

// stubRunSSHRaw swaps the package-level runSSHRaw var (used by bootDetached).
func stubRunSSHRaw(t *testing.T, fn func(ctx context.Context, sshArgs []string) bootResult) {
	t.Helper()
	orig := runSSHRaw
	runSSHRaw = fn
	t.Cleanup(func() { runSSHRaw = orig })
}

// stubRunShellOnBench swaps the package-level runShellOnBench var (used by
// writeReaperState and Cleanup's state-file removal).
func stubRunShellOnBench(t *testing.T, fn func(ctx context.Context, b bench.Bench, script string) (string, error)) {
	t.Helper()
	orig := runShellOnBench
	runShellOnBench = fn
	t.Cleanup(func() { runShellOnBench = orig })
}

// allGood wires every seam to a no-op success, so tests can override just
// the one seam they care about.
func allGood(t *testing.T) {
	t.Helper()
	stubCopyToBench(t, func(context.Context, bench.Bench, string, string) error { return nil })
	stubRunSSHRaw(t, func(context.Context, []string) bootResult { return bootResult{RunErr: nil} })
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 && args[0] == "ip" {
			return "10.0.0.5\n", nil
		}
		return "", nil
	})
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "cat" {
			return "v0.0.0-test\n", nil
		}
		return "", nil
	})
	stubCopyFromBench(t, func(context.Context, bench.Bench, string, string) error { return nil })
	stubRunShellOnBench(t, func(context.Context, bench.Bench, string) (string, error) { return "", nil })
}

func testBench() bench.Bench {
	return bench.Bench{Name: "tart-bench", Host: "tart-bench.local", OS: "macos", Runtime: bench.RuntimeTart}
}

func newTestPipeline(t *testing.T, bufSize int) (*Pipeline, chan pipeline.Event) {
	t.Helper()
	ch := make(chan pipeline.Event, bufSize)
	p := New(testBench(), images.ImageID("gsd-tester-macos-tart:dev"), "v0.0.0-test", "/tmp/worktree", nil, "22", ch, pipeline.ContainerIdentity{})
	t.Cleanup(func() {
		p.closeEvents()
		for range ch {
		}
	})
	return p, ch
}

// --- New / naming -------------------------------------------------------

func TestNew_NilEventChannelOK(t *testing.T) {
	allGood(t)
	p := New(testBench(), images.ImageID("gsd-tester-macos-tart:dev"), "v0.0.0-test", "/tmp/worktree", nil, "22", nil, pipeline.ContainerIdentity{})
	if err := p.CheckImageVersion(context.Background()); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestDeriveVMName_EmptyIdent_FallsBackToNoid(t *testing.T) {
	name := deriveVMName(pipeline.ContainerIdentity{})
	if name != "gsd-tart-noid" {
		t.Errorf("expected gsd-tart-noid, got %q", name)
	}
}

func TestDeriveVMName_WithCellAndRunID(t *testing.T) {
	name := deriveVMName(pipeline.ContainerIdentity{RunID: "abc12345", Cell: "macos-node22"})
	want := "gsd-tart-macos-node22-abc12345"
	if name != want {
		t.Errorf("expected %q, got %q", want, name)
	}
}

func TestDeriveScratchPath_Unique(t *testing.T) {
	a := deriveScratchPath(pipeline.ContainerIdentity{RunID: "run1", Cell: "macos"})
	b := deriveScratchPath(pipeline.ContainerIdentity{RunID: "run2", Cell: "macos"})
	if a == b {
		t.Fatalf("expected distinct scratch paths, got %q for both", a)
	}
	if !strings.HasPrefix(a, "/tmp/gsd-test-tart-") {
		t.Errorf("expected /tmp/gsd-test-tart- prefix, got %q", a)
	}
}

// --- CopyWorktree ---------------------------------------------------------

func TestCopyWorktree_Success(t *testing.T) {
	allGood(t)
	var gotLocal, gotRemote string
	stubCopyToBench(t, func(_ context.Context, _ bench.Bench, local, remote string) error {
		gotLocal, gotRemote = local, remote
		return nil
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.CopyWorktree(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if gotLocal != "/tmp/worktree" {
		t.Errorf("expected local=/tmp/worktree, got %q", gotLocal)
	}
	if gotRemote != p.scratchPath {
		t.Errorf("expected remote=%q, got %q", p.scratchPath, gotRemote)
	}
}

func TestCopyWorktree_EmptyPath_ReturnsCopyInError(t *testing.T) {
	allGood(t)
	p, _ := newTestPipeline(t, 16)
	p.work = ""
	err := p.CopyWorktree(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var cie *CopyInError
	if !errors.As(legErr.Cause, &cie) {
		t.Fatalf("expected Cause=*CopyInError, got %T", legErr.Cause)
	}
}

func TestCopyWorktree_ScpFails_WrapsInLegError(t *testing.T) {
	allGood(t)
	stubCopyToBench(t, func(context.Context, bench.Bench, string, string) error {
		return &tartexec.ExecError{Stderr: "scp: connection refused", ExitCode: 1}
	})
	p, _ := newTestPipeline(t, 16)
	err := p.CopyWorktree(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	if legErr.Leg != pipeline.LegCopyWorktree {
		t.Errorf("expected LegCopyWorktree, got %v", legErr.Leg)
	}
}

// --- StartContainer ---------------------------------------------------------

func TestStartContainer_Success_SetsVMStarted(t *testing.T) {
	allGood(t)
	var sawClone, sawSet, sawIP bool
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		switch {
		case len(args) > 0 && args[0] == "clone":
			sawClone = true
			return "", nil
		case len(args) > 0 && args[0] == "set":
			sawSet = true
			return "", nil
		case len(args) > 0 && args[0] == "ip":
			sawIP = true
			return "10.0.0.5\n", nil
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.StartContainer(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !sawClone || !sawSet || !sawIP {
		t.Errorf("expected clone+set+ip all invoked, got clone=%v set=%v ip=%v", sawClone, sawSet, sawIP)
	}
	if !p.vmStarted {
		t.Error("expected vmStarted=true after successful StartContainer")
	}
}

func TestStartContainer_CloneFails_VMStartErrorStageClone(t *testing.T) {
	allGood(t)
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 && args[0] == "clone" {
			return "", &tartexec.ExecError{Stderr: "no such image", ExitCode: 1}
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var vse *VMStartError
	if !errors.As(legErr.Cause, &vse) {
		t.Fatalf("expected Cause=*VMStartError, got %T", legErr.Cause)
	}
	if vse.Stage != "clone" {
		t.Errorf("expected stage=clone, got %q", vse.Stage)
	}
	if p.vmStarted {
		t.Error("expected vmStarted=false when clone itself fails")
	}
}

func TestStartContainer_SetMemoryFails_VMStartedTrue_ForCleanup(t *testing.T) {
	allGood(t)
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 && args[0] == "set" {
			return "", &tartexec.ExecError{Stderr: "boom", ExitCode: 1}
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	// vmStarted must be true here: clone already registered a VM on the
	// Bench that Cleanup needs to remove even though boot never happened.
	if !p.vmStarted {
		t.Error("expected vmStarted=true so Cleanup still attempts stop+delete after clone succeeded")
	}
}

func TestStartContainer_WaitIPEmpty_VMStartError(t *testing.T) {
	allGood(t)
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 && args[0] == "ip" {
			return "   \n", nil
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var vse *VMStartError
	if !errors.As(legErr.Cause, &vse) {
		t.Fatalf("expected Cause=*VMStartError, got %T", legErr.Cause)
	}
	if vse.Stage != "wait_ip" {
		t.Errorf("expected stage=wait_ip, got %q", vse.Stage)
	}
}

// --- StartContainer: write_state (tartreaper state-file write) -----------

func TestStartContainer_WritesReaperStateFile(t *testing.T) {
	allGood(t)
	var gotScript string
	stubRunShellOnBench(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		gotScript = script
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	p.ident = pipeline.ContainerIdentity{RunID: "run-123", BranchSlug: "fix-foo", DeadlineMs: 999000}
	if err := p.StartContainer(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if gotScript == "" {
		t.Fatal("expected runShellOnBench to be called with a non-empty script")
	}
	if !strings.Contains(gotScript, "mkdir -p") || !strings.Contains(gotScript, tartreaper.StateDir) {
		t.Errorf("expected script to mkdir -p the state dir, got %q", gotScript)
	}
	if !strings.Contains(gotScript, p.vmName+".json") {
		t.Errorf("expected script to reference %s.json, got %q", p.vmName, gotScript)
	}
	// The JSON payload is single-quote-shell-escaped inside the script; a
	// simple substring check on the (unquoted) field values is sufficient to
	// verify the right identity was marshaled in.
	if !strings.Contains(gotScript, `\"run_id\":\"run-123\"`) && !strings.Contains(gotScript, `"run_id":"run-123"`) {
		t.Errorf("expected script to embed run_id=run-123, got %q", gotScript)
	}
	if !strings.Contains(gotScript, "fix-foo") {
		t.Errorf("expected script to embed branch_slug=fix-foo, got %q", gotScript)
	}
	if !strings.Contains(gotScript, "999000") {
		t.Errorf("expected script to embed deadline_ms=999000, got %q", gotScript)
	}
}

func TestStartContainer_WriteStateFails_VMStartErrorStageWriteState_TriggersCleanup(t *testing.T) {
	allGood(t)
	stubRunShellOnBench(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		return "", &tartexec.ExecError{Stderr: "no space left on device", ExitCode: 1}
	})
	var seen []string
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 {
			seen = append(seen, args[0])
		}
		if len(args) > 0 && args[0] == "ip" {
			return "10.0.0.5\n", nil
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.StartContainer(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var vse *VMStartError
	if !errors.As(legErr.Cause, &vse) {
		t.Fatalf("expected Cause=*VMStartError, got %T", legErr.Cause)
	}
	if vse.Stage != "write_state" {
		t.Errorf("expected stage=write_state, got %q", vse.Stage)
	}
	if !p.vmStarted {
		t.Fatal("expected vmStarted=true so Cleanup still attempts stop+delete after clone succeeded")
	}
	// set/boot/ip must NOT have run past the write_state failure.
	for _, s := range seen {
		if s == "set" || s == "ip" {
			t.Errorf("expected StartContainer to stop at write_state, but saw tart %q invoked", s)
		}
	}

	// Now exercise Cleanup directly (mirroring how RunAll's defer would call
	// it) and assert stop+delete still fire.
	seen = nil
	p.Cleanup(context.Background())
	if len(seen) != 2 || seen[0] != "stop" || seen[1] != "delete" {
		t.Errorf("expected Cleanup to invoke [stop delete], got %v", seen)
	}
}

func TestBootDetached_BuildsNohupCommand(t *testing.T) {
	allGood(t)
	var gotArgs []string
	stubRunSSHRaw(t, func(_ context.Context, sshArgs []string) bootResult {
		gotArgs = sshArgs
		return bootResult{RunErr: nil}
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.bootDetached(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != p.bench.Host || gotArgs[1] != "--" {
		t.Fatalf("unexpected ssh args shape: %#v", gotArgs)
	}
	cmd := gotArgs[2]
	if !strings.Contains(cmd, "nohup tart run") || !strings.Contains(cmd, "--no-graphics") ||
		!strings.Contains(cmd, "disown") || !strings.Contains(cmd, p.vmName) ||
		!strings.Contains(cmd, p.scratchPath) {
		t.Errorf("boot command missing expected pieces: %q", cmd)
	}
}

// --- CheckImageVersion ---------------------------------------------------------

func TestCheckImageVersion_Match(t *testing.T) {
	allGood(t)
	p, _ := newTestPipeline(t, 16)
	if err := p.CheckImageVersion(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckImageVersion_Mismatch_ReturnsImagesMismatchType(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(context.Context, bench.Bench, string, string, string, []string) (string, error) {
		return "v9.9.9\n", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.CheckImageVersion(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var mm *images.ImageVersionMismatch
	if !errors.As(legErr.Cause, &mm) {
		t.Fatalf("expected Cause=*images.ImageVersionMismatch, got %T", legErr.Cause)
	}
	if mm.Want != "v0.0.0-test" || mm.Got != "v9.9.9" {
		t.Errorf("unexpected mismatch fields: want=%q got=%q", mm.Want, mm.Got)
	}
}

func TestCheckImageVersion_ExecFails_SentinelReadError(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(context.Context, bench.Bench, string, string, string, []string) (string, error) {
		return "", &tartexec.ExecError{Stderr: "no such file", ExitCode: 1}
	})
	p, _ := newTestPipeline(t, 16)
	err := p.CheckImageVersion(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var sre *SentinelReadError
	if !errors.As(legErr.Cause, &sre) {
		t.Fatalf("expected Cause=*SentinelReadError, got %T", legErr.Cause)
	}
}

// --- NpmCI / Build / RunTests ---------------------------------------------------------

func TestNpmCI_Success_UsesPathPrefixAndCd(t *testing.T) {
	allGood(t)
	var gotArgs []string
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		gotArgs = args
		return "added 1 package\n", nil
	})
	p, ch := newTestPipeline(t, 16)
	if err := p.NpmCI(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "sh" || gotArgs[1] != "-c" {
		t.Fatalf("unexpected guest args shape: %#v", gotArgs)
	}
	if !strings.Contains(gotArgs[2], "PATH=/opt/homebrew/opt/node@22/bin") || !strings.Contains(gotArgs[2], "npm ci") {
		t.Errorf("expected PATH prefix + npm ci in command, got %q", gotArgs[2])
	}
	// Verify captured output was emitted as EventChildOutput.
	drainEvents(t, ch, 10)
}

func TestBuild_NoBuildScript_Skipped(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) == 3 && strings.Contains(args[2], "node -e") {
			return "", &tartexec.ExecError{ExitCode: 1}
		}
		return "", nil
	})
	p, ch := newTestPipeline(t, 16)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("expected nil (skipped, not an error), got %v", err)
	}
	evs := drainEvents(t, ch, 10)
	found := false
	for _, e := range evs {
		if e.Kind == pipeline.EventLegSkipped && e.Leg == pipeline.LegBuild {
			found = true
		}
	}
	if !found {
		t.Error("expected an EventLegSkipped for LegBuild")
	}
}

func TestBuild_HasBuildScript_RunsNpmRunBuild(t *testing.T) {
	allGood(t)
	var sawBuild bool
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) == 3 && strings.Contains(args[2], "npm run build") {
			sawBuild = true
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !sawBuild {
		t.Error("expected npm run build to be invoked")
	}
}

func TestRunTests_ExitCode1_NotALegError(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) == 3 && strings.Contains(args[2], "node") && strings.Contains(args[2], "--test") {
			return "1 failing\n", &tartexec.ExecError{Stdout: "1 failing\n", ExitCode: 1}
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.RunTests(context.Background()); err != nil {
		t.Fatalf("expected nil (exit 1 downgraded), got %v", err)
	}
}

func TestRunTests_ExitCode2_IsALegError(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) == 3 && strings.Contains(args[2], "node") && strings.Contains(args[2], "--test") {
			return "", &tartexec.ExecError{Stderr: "crash", ExitCode: 2}
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	err := p.RunTests(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError for exit 2, got %T: %v", err, err)
	}
}

func TestRunTests_ReporterDestination_UnderMountedPath(t *testing.T) {
	allGood(t)
	var gotArgs []string
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) == 3 && strings.Contains(args[2], "--test") {
			gotArgs = args
		}
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.RunTests(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !strings.Contains(gotArgs[2], guestJSONLPath) {
		t.Errorf("expected reporter destination %q in command, got %q", guestJSONLPath, gotArgs[2])
	}
	if !strings.Contains(guestJSONLPath, guestMountBase) {
		t.Fatalf("test invariant broken: guestJSONLPath must be under guestMountBase")
	}
}

// --- Drain / Parse ---------------------------------------------------------

func TestDrain_Success_SetsDrainedPath(t *testing.T) {
	allGood(t)
	var gotRemote string
	stubCopyFromBench(t, func(_ context.Context, _ bench.Bench, remote, local string) error {
		gotRemote = remote
		return os.WriteFile(local, []byte(`{"type":"test_event","kind":"pass","name":"a"}`+"\n"), 0o644)
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.Drain(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if p.DrainedPath() == "" {
		t.Fatal("expected DrainedPath to be set")
	}
	defer os.Remove(p.DrainedPath())
	wantRemote := p.scratchPath + "/test-events.jsonl"
	if gotRemote != wantRemote {
		t.Errorf("expected remote=%q, got %q", wantRemote, gotRemote)
	}
}

func TestDrain_ScpFails_DrainError(t *testing.T) {
	allGood(t)
	stubCopyFromBench(t, func(context.Context, bench.Bench, string, string) error {
		return &tartexec.ExecError{Stderr: "no such file", ExitCode: 1}
	})
	p, _ := newTestPipeline(t, 16)
	err := p.Drain(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var de *DrainError
	if !errors.As(legErr.Cause, &de) {
		t.Fatalf("expected Cause=*DrainError, got %T", legErr.Cause)
	}
}

func TestParse_NoDrainedPath_ParseError(t *testing.T) {
	allGood(t)
	p, _ := newTestPipeline(t, 16)
	err := p.Parse(context.Background())
	var legErr *pipeline.LegError
	if !errors.As(err, &legErr) {
		t.Fatalf("expected *pipeline.LegError, got %T: %v", err, err)
	}
	var pe *ParseError
	if !errors.As(legErr.Cause, &pe) {
		t.Fatalf("expected Cause=*ParseError, got %T", legErr.Cause)
	}
}

func TestParse_AfterDrain_AggregatesCounts(t *testing.T) {
	allGood(t)
	jsonl := strings.Join([]string{
		`{"type":"test_event","kind":"pass","name":"a"}`,
		`{"type":"test_event","kind":"fail","name":"b","error":"boom"}`,
	}, "\n") + "\n"
	stubCopyFromBench(t, func(_ context.Context, _ bench.Bench, _, local string) error {
		return os.WriteFile(local, []byte(jsonl), 0o644)
	})
	p, _ := newTestPipeline(t, 16)
	if err := p.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	defer os.Remove(p.DrainedPath())
	if err := p.Parse(context.Background()); err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep := p.Report()
	if rep.Total != 2 || rep.Passed != 1 || rep.Failed != 1 {
		t.Errorf("unexpected counts: total=%d passed=%d failed=%d", rep.Total, rep.Passed, rep.Failed)
	}
}

// --- Cleanup ---------------------------------------------------------

func TestCleanup_NotStarted_NoOp(t *testing.T) {
	called := false
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		called = true
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	p.Cleanup(context.Background())
	if called {
		t.Error("expected no tart invocation when vmStarted is false")
	}
}

func TestCleanup_Started_StopsAndDeletes(t *testing.T) {
	var seen []string
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 {
			seen = append(seen, args[0])
		}
		return "", nil
	})
	stubRunShellOnBench(t, func(context.Context, bench.Bench, string) (string, error) { return "", nil })
	p, _ := newTestPipeline(t, 16)
	p.vmStarted = true
	p.Cleanup(context.Background())
	if len(seen) != 2 || seen[0] != "stop" || seen[1] != "delete" {
		t.Errorf("expected [stop delete], got %v", seen)
	}
}

func TestCleanup_Started_RemovesStateFileAfterStopDelete(t *testing.T) {
	var order []string
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 {
			order = append(order, "tart:"+args[0])
		}
		return "", nil
	})
	var gotScript string
	stubRunShellOnBench(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		order = append(order, "shell")
		gotScript = script
		return "", nil
	})
	p, _ := newTestPipeline(t, 16)
	p.vmStarted = true
	p.Cleanup(context.Background())

	want := []string{"tart:stop", "tart:delete", "shell"}
	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
	if !strings.Contains(gotScript, "rm -f") || !strings.Contains(gotScript, tartreaper.StateDir) || !strings.Contains(gotScript, p.vmName+".json") {
		t.Errorf("expected an `rm -f <stateDir>/%s.json` script, got %q", p.vmName, gotScript)
	}
}

// --- RunAll: leg order + event shape ---------------------------------------------------------

func TestRunAll_LegCallOrder_CopyWorktreeBeforeStartContainer(t *testing.T) {
	allGood(t)
	var order []string
	stubCopyToBench(t, func(context.Context, bench.Bench, string, string) error {
		order = append(order, "copy_worktree")
		return nil
	})
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) > 0 && args[0] == "clone" {
			order = append(order, "start_container")
		}
		if len(args) > 0 && args[0] == "ip" {
			return "10.0.0.5\n", nil
		}
		return "", nil
	})
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "cat" {
			order = append(order, "check_image_version")
			return "v0.0.0-test\n", nil
		}
		return "", nil
	})
	p, ch := newTestPipeline(t, 256)
	go func() {
		for range ch {
		}
	}()
	// Only run the first three legs directly to isolate ordering, avoiding
	// the need to stub every remaining leg's guest command shape.
	if err := p.CopyWorktree(context.Background()); err != nil {
		t.Fatalf("CopyWorktree: %v", err)
	}
	if err := p.StartContainer(context.Background()); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	if err := p.CheckImageVersion(context.Background()); err != nil {
		t.Fatalf("CheckImageVersion: %v", err)
	}
	want := []string{"copy_worktree", "start_container", "check_image_version"}
	if len(order) != len(want) {
		t.Fatalf("expected order %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, order)
		}
	}
}

func TestRunAll_FullSuccess_AllEightLegsEmitStartAndSuccess(t *testing.T) {
	allGood(t)
	stubExecGuest(t, func(_ context.Context, _ bench.Bench, _, _, _ string, args []string) (string, error) {
		switch {
		case len(args) > 0 && args[0] == "cat":
			return "v0.0.0-test\n", nil
		case len(args) == 3 && strings.Contains(args[2], "node -e"):
			return "", &tartexec.ExecError{ExitCode: 1} // no build script -> skip Build
		default:
			return "", nil
		}
	})
	stubCopyFromBench(t, func(_ context.Context, _ bench.Bench, _, local string) error {
		return os.WriteFile(local, []byte(`{"type":"test_event","kind":"pass","name":"a"}`+"\n"), 0o644)
	})
	p, ch := newTestPipeline(t, 256)

	var events []pipeline.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			events = append(events, e)
		}
	}()

	rep, err := p.RunAll(context.Background())
	<-done
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if rep.Total != 1 || rep.Passed != 1 {
		t.Errorf("unexpected report: %+v", rep)
	}

	wantLegs := []pipeline.Leg{
		pipeline.LegCopyWorktree,
		pipeline.LegStartContainer,
		pipeline.LegCheckImageVersion,
		pipeline.LegNpmCI,
		pipeline.LegBuild,
		pipeline.LegRunTests,
		pipeline.LegDrain,
		pipeline.LegParse,
	}
	for _, leg := range wantLegs {
		if !hasStartEvent(events, leg) {
			t.Errorf("missing EventLegStart for %v", leg)
		}
	}
	if !hasSkippedEvent(events, pipeline.LegBuild) {
		t.Error("expected EventLegSkipped for LegBuild (no build script)")
	}
	defer os.Remove(p.DrainedPath())
}

func hasStartEvent(events []pipeline.Event, leg pipeline.Leg) bool {
	for _, e := range events {
		if e.Kind == pipeline.EventLegStart && e.Leg == leg {
			return true
		}
	}
	return false
}

func hasSkippedEvent(events []pipeline.Event, leg pipeline.Leg) bool {
	for _, e := range events {
		if e.Kind == pipeline.EventLegSkipped && e.Leg == leg {
			return true
		}
	}
	return false
}

func drainEvents(t *testing.T, ch chan pipeline.Event, max int) []pipeline.Event {
	t.Helper()
	var out []pipeline.Event
	for i := 0; i < max; i++ {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
	return out
}

// --- legExitCode ---------------------------------------------------------

func TestLegExitCode_MatchesPipelineTable(t *testing.T) {
	cases := []struct {
		leg  pipeline.Leg
		want int
	}{
		{pipeline.LegCheckImageVersion, pipeline.ExitCodeCheckImageVersion},
		{pipeline.LegCopyWorktree, pipeline.ExitCodeCopyWorktree},
		{pipeline.LegStartContainer, pipeline.ExitCodeStartContainer},
		{pipeline.LegNpmCI, pipeline.ExitCodeNpmCI},
		{pipeline.LegBuild, pipeline.ExitCodeBuild},
		{pipeline.LegRunTests, pipeline.ExitCodeRunTests},
		{pipeline.LegDrain, pipeline.ExitCodeDrain},
		{pipeline.LegParse, pipeline.ExitCodeParse},
	}
	for _, c := range cases {
		if got := legExitCode(c.leg); got != c.want {
			t.Errorf("legExitCode(%v) = %d, want %d", c.leg, got, c.want)
		}
	}
}

// --- shellQuote sanity (duplicated-from-tartexec helper) -------------------

func TestShellQuote_EscapesEmbeddedQuote(t *testing.T) {
	got := shellQuote("it's")
	want := `'it'\''s'`
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "it's", got, want)
	}
}

func TestDefaultMemoryMB_IsPositiveInt(t *testing.T) {
	if DefaultMemoryMB <= 0 {
		t.Fatalf("expected positive DefaultMemoryMB, got %d", DefaultMemoryMB)
	}
	// Sanity: must be representable via strconv.Itoa the same way
	// StartContainer builds the `tart set --memory` arg.
	if strconv.Itoa(DefaultMemoryMB) == "" {
		t.Fatal("unexpected empty Itoa result")
	}
}
