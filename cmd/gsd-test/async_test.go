package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runrender"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// helpers for seeding runstate in tests.

func seedRunningState(t *testing.T, runID string) {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("seedRunningState: parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    runstate.StatusRunning,
		PID:       999999, // no real process — safe for unit tests
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Spec:      *sp,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("seedRunningState: save: %v", err)
	}
}

func seedDoneState(t *testing.T, runID string, rep *report.Report, exitCode int, errStr string) {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("seedDoneState: parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    runstate.StatusDone,
		PID:       0,
		StartedAt: time.Now().Add(-5 * time.Second).UTC(),
		UpdatedAt: time.Now().UTC(),
		Spec:      *sp,
		Report:    rep,
		ExitCode:  exitCode,
		Err:       errStr,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("seedDoneState: save: %v", err)
	}
}

// fakeSpawn is a spawnFunc that records calls and returns immediately without
// launching any real process. It is used in async tests so no Docker or worker
// is needed.
func fakeSpawn(runID, configPath string) (int, error) {
	return 42, nil // fake pid 42
}

// TestRunAsync_DispatchReturnsZeroFastAndPrintsRunID verifies that
// `gsd-test run --async` returns exit 0 immediately, prints a line containing
// run-id=<something>, and leaves a state file with Status=running.
func TestRunAsync_DispatchReturnsZeroFastAndPrintsRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	// Inject fake spawn so no worker is launched.
	orig := defaultSpawn
	defaultSpawn = fakeSpawn
	defer func() { defaultSpawn = orig }()

	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"run", "--async", "--config", cfgPath, "--target", "linux"}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitAllPass {
		t.Errorf("exit code: got %d, want 0 (async dispatch)", code)
	}

	// Must print a line containing run-id=<something>.
	if !strings.Contains(stdout, "run-id=") {
		t.Errorf("stdout must contain run-id=...; got:\n%s", stdout)
	}

	// Extract run-id from the output.
	var runID string
	for _, part := range strings.Fields(stdout) {
		if strings.HasPrefix(part, "run-id=") {
			runID = strings.TrimPrefix(part, "run-id=")
		}
	}
	if runID == "" {
		t.Fatalf("could not extract run-id from stdout: %q", stdout)
	}

	// State file must exist with Status=running.
	st, err := runstate.Load(runID)
	if err != nil {
		t.Fatalf("Load(%s): %v", runID, err)
	}
	if st.Status != runstate.StatusRunning {
		t.Errorf("Status: got %q, want %q", st.Status, runstate.StatusRunning)
	}
	if st.RunID != runID {
		t.Errorf("RunID in state: got %q, want %q", st.RunID, runID)
	}
}

// TestWait_DoneStateRendersVerdict verifies that `gsd-test wait <id>` against
// a pre-seeded DONE state renders the same node:test output as runrender.Render
// and returns the same exit code.
// lastLineIsVerdict reports whether the final non-empty stdout line is the
// Option C machine verdict ({"type":"verdict",...}, appended after the
// runrender text by `gsd-test run`/`wait`, epic #84).
func lastLineIsVerdict(stdout string) bool {
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	return strings.HasPrefix(lines[len(lines)-1], `{"type":"verdict"`)
}

func TestWait_DoneStateRendersVerdict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "wait-done-test-001"
	rep := &report.Report{
		Outcome: report.OutcomePassed,
		Total:   2,
		Passed:  2,
		PerTest: []report.TestStat{
			{File: "a.test.js", Name: "passes", DurationMs: 10, Status: "passed", ExitedClean: true},
			{File: "b.test.js", Name: "also passes", DurationMs: 20, Status: "passed", ExitedClean: true},
		},
	}
	wantText, wantCode := runrender.Render(*rep)
	seedDoneState(t, runID, rep, wantCode, "")

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != wantCode {
		t.Errorf("exit code: got %d, want %d", code, wantCode)
	}
	if !strings.Contains(stdout, "ℹ tests") {
		t.Errorf("stdout missing 'ℹ tests' marker; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "ℹ pass") {
		t.Errorf("stdout missing 'ℹ pass' marker; got:\n%s", stdout)
	}
	if !strings.HasPrefix(stdout, wantText) {
		t.Errorf("stdout should begin with the runrender verdict text:\ngot:\n%s\nwant prefix:\n%s", stdout, wantText)
	}
	if !lastLineIsVerdict(stdout) {
		t.Errorf("expected the final stdout line to be the machine verdict (#84); got:\n%s", stdout)
	}
}

// TestWait_DoneStateWithErr verifies that `gsd-test wait <id>` against a done
// state with Err set returns exit 2 (inconclusive).
func TestWait_DoneStateWithErr(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "wait-err-test-001"
	seedDoneState(t, runID, nil, exitInconclusive, "dispatch failed")

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (err state)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "dispatch failed") {
		t.Errorf("stderr must mention the error; got:\n%s", stderr)
	}
}

// TestWait_UnknownRunID verifies that `gsd-test wait <unknown>` returns exit 2
// with an "unknown run-id" style message on stderr.
func TestWait_UnknownRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"wait", "nonexistent-run-id-9999"}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (unknown id)", code, exitInconclusive)
	}
	if !strings.Contains(strings.ToLower(stderr), "unknown run-id") {
		t.Errorf("stderr must contain 'unknown run-id'; got:\n%s", stderr)
	}
}

// TestStatus_RunningRunID verifies that `gsd-test status <id>` against a running
// state prints "state=running" and returns exit 0.
func TestStatus_RunningRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "status-running-test-001"
	seedRunningState(t, runID)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"status", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitAllPass {
		t.Errorf("exit code: got %d, want 0 (status is pure reporter)", code)
	}
	if !strings.Contains(stdout, "state=running") {
		t.Errorf("stdout must contain 'state=running'; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "run-id="+runID) {
		t.Errorf("stdout must contain run-id=...; got:\n%s", stdout)
	}
}

// TestStatus_DoneRunID verifies that `gsd-test status <id>` against a done
// state with exit=1 prints "state=done" and "exit=1" and returns exit 0.
func TestStatus_DoneRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "status-done-test-001"
	rep := &report.Report{
		Outcome: report.OutcomeFailed,
		Total:   2,
		Passed:  1,
		Failed:  1,
		PerTest: []report.TestStat{
			{File: "a.test.js", Name: "passes", DurationMs: 10, Status: "passed", ExitedClean: true},
			{File: "b.test.js", Name: "fails", DurationMs: 5, Status: "failed", ExitedClean: true},
		},
	}
	seedDoneState(t, runID, rep, exitSomeFailed, "")

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"status", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitAllPass {
		t.Errorf("exit code: got %d, want 0 (status is pure reporter)", code)
	}
	if !strings.Contains(stdout, "state=done") {
		t.Errorf("stdout must contain 'state=done'; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "exit=1") {
		t.Errorf("stdout must contain 'exit=1'; got:\n%s", stdout)
	}
}

// TestStatus_UnknownRunID verifies that `gsd-test status <unknown>` returns
// exit 2 with an "unknown run-id" style message on stderr.
func TestStatus_UnknownRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"status", "nonexistent-9999"}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (unknown id)", code, exitInconclusive)
	}
	if !strings.Contains(strings.ToLower(stderr), "unknown run-id") {
		t.Errorf("stderr must contain 'unknown run-id'; got:\n%s", stderr)
	}
}

// seedRunningStateWithOpts creates a running runstate with caller-controlled PID
// and StartedAt so regression tests can inject dead PIDs and stale timestamps.
func seedRunningStateWithOpts(t *testing.T, runID string, pid int, startedAt time.Time) {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("seedRunningStateWithOpts: parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    runstate.StatusRunning,
		PID:       pid,
		StartedAt: startedAt,
		UpdatedAt: time.Now().UTC(),
		Spec:      *sp,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("seedRunningStateWithOpts: save: %v", err)
	}
}

// deadPID returns a PID that is guaranteed not to be alive. It spawns a trivial
// process, waits for it to exit, then returns its (now-dead) PID.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("deadPID: start process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("deadPID: wait for process: %v", err)
	}
	return pid
}

// ── Regression tests for fixes 1-3 ──────────────────────────────────────────

// TestWait_DoneStateWithDeadPIDWinsOverLivenessGuard is a regression test for
// Fix 1. The bug: waitRun checked workerPIDAlive on the STALE pre-sleep st
// BEFORE reloading. If the worker wrote done and exited during the sleep, the
// next iteration saw a dead PID on the old running struct and returned
// exitInconclusive — discarding the real result.
//
// Scenario: seed running (dead PID). A goroutine overwrites the state with done
// at ~50ms (before the 200ms sleep expires). waitRun wakes, checks PID on the
// STALE running st (bug path), returns inconclusive. After the fix: reload
// first, observe done, break out of the loop, render the real verdict.
func TestWait_DoneStateWithDeadPIDWinsOverLivenessGuard(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "fix1-done-dead-pid-001"

	// Use a real dead PID: spawn + wait a trivial process.
	dpid := deadPID(t)

	// Build the spec shared by both the running and done states.
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	sp.RunID = runID

	// Seed a RUNNING state with the dead PID — this is what waitRun loads first.
	runSt := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    runstate.StatusRunning,
		PID:       dpid, // dead already — simulates worker-exited-after-writing-done
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Spec:      *sp,
	}
	if err := runstate.Save(runSt); err != nil {
		t.Fatalf("save running state: %v", err)
	}

	rep := &report.Report{
		Outcome: report.OutcomePassed,
		Total:   3,
		Passed:  3,
		PerTest: []report.TestStat{
			{File: "x.test.js", Name: "ok", DurationMs: 5, Status: "passed", ExitedClean: true},
			{File: "y.test.js", Name: "ok2", DurationMs: 6, Status: "passed", ExitedClean: true},
			{File: "z.test.js", Name: "ok3", DurationMs: 7, Status: "passed", ExitedClean: true},
		},
	}
	wantText, wantCode := runrender.Render(*rep)

	// Overwrite the state with done at ~50ms — well before waitRun's 200ms sleep
	// expires. This simulates the worker writing its final state mid-sleep.
	go func() {
		time.Sleep(50 * time.Millisecond)
		doneSt := runstate.State{
			RunID:     runID,
			Target:    sp.Target,
			Repo:      sp.Repo,
			Status:    runstate.StatusDone,
			PID:       dpid,
			StartedAt: runSt.StartedAt,
			UpdatedAt: time.Now().UTC(),
			Spec:      *sp,
			Report:    rep,
			ExitCode:  wantCode,
		}
		_ = runstate.Save(doneSt)
	}()

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	stderr := drain(errR)

	// The done state must win: real exit code + rendered verdict — NOT inconclusive.
	if code != wantCode {
		t.Errorf("exit code: got %d, want %d (done state with dead pid must win over liveness guard); stderr:\n%s",
			code, wantCode, stderr)
	}
	if !strings.HasPrefix(stdout, wantText) {
		t.Errorf("stdout should begin with the runrender verdict text:\ngot:\n%s\nwant prefix:\n%s", stdout, wantText)
	}
	if !lastLineIsVerdict(stdout) {
		t.Errorf("expected the final stdout line to be the machine verdict (#84); got:\n%s", stdout)
	}
	if strings.Contains(stderr, "worker") && strings.Contains(stderr, "gone") {
		t.Errorf("stderr must NOT say 'worker gone' for a completed run; got:\n%s", stderr)
	}
}

// TestWait_RunningWithDeadPIDReturnsInconclusive verifies that the liveness
// guard still fires correctly when the state is genuinely still running but
// the worker PID is dead (worker crashed before writing a final state).
func TestWait_RunningWithDeadPIDReturnsInconclusive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "fix1-running-dead-pid-002"
	dpid := deadPID(t)

	// Seed a RUNNING state with a dead PID. The worker never wrote a done state.
	seedRunningStateWithOpts(t, runID, dpid, time.Now().UTC())

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (running + dead pid → liveness guard)", code, exitInconclusive)
	}
	if !strings.Contains(strings.ToLower(stderr), "gone") {
		t.Errorf("stderr must mention worker gone; got:\n%s", stderr)
	}
}

// TestRunWorker_DispatchFailSavesAndWaitReturnsInconclusive is a portable
// integration test for Fix 2 and the worker path (Fix 4.3). It exercises
// runWorker → dispatchRun(fail) → Save(done, Err) → wait(exitInconclusive)
// without Docker by pointing at a config with no Bench.
func TestRunWorker_DispatchFailSavesAndWaitReturnsInconclusive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "fix2-worker-dispatch-fail-001"

	// Config with no Bench — dispatchRun will return ok=false.
	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a running state with the spec the worker will load.
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    runstate.StatusRunning,
		PID:       0,
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Spec:      *sp,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("save running state: %v", err)
	}

	// Drive the worker directly via run(["__run-worker", ...]). This exercises
	// the real worker code path without any Docker.
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	workerCode := run([]string{"__run-worker", "--run-id", runID, "--config", cfgPath}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	_ = drain(errR)

	// Worker should return inconclusive (dispatch failed).
	if workerCode != exitInconclusive {
		t.Errorf("worker exit code: got %d, want %d (no bench → dispatch fail)", workerCode, exitInconclusive)
	}

	// The persisted state must now be done with a non-empty Err.
	saved, err := runstate.Load(runID)
	if err != nil {
		t.Fatalf("load state after worker: %v", err)
	}
	if saved.Status != runstate.StatusDone {
		t.Errorf("state after worker: Status=%q, want %q", saved.Status, runstate.StatusDone)
	}
	if saved.Err == "" {
		t.Errorf("state after worker: Err is empty, want non-empty dispatch-fail error")
	}
	// Worker must have claimed the PID (its own pid > 0) in the state.
	if saved.PID <= 0 {
		t.Errorf("state after worker: PID=%d, want >0 (worker must write its own pid)", saved.PID)
	}

	// Now call wait — it must return inconclusive (not hang).
	outR2, outW2, _ := os.Pipe()
	errR2, errW2, _ := os.Pipe()
	waitCode := run([]string{"wait", runID}, outW2, errW2)
	outW2.Close()
	errW2.Close()
	_ = drain(outR2)
	stderr2 := drain(errR2)

	if waitCode != exitInconclusive {
		t.Errorf("wait exit code: got %d, want %d (dispatch-failed run → inconclusive)", waitCode, exitInconclusive)
	}
	_ = fmt.Sprintf("stderr from wait: %s", stderr2) // consume without asserting content
}

// TestWait_BackstopFiresWhenStartedAtIsStale is a regression test for Fix 3.
// A running state with StartedAt set far in the past (2h ago) and pid 0 must
// cause waitRun to return exitInconclusive promptly with a "did not complete
// within" message — never spinning forever.
func TestWait_BackstopFiresWhenStartedAtIsStale(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "fix3-backstop-stale-001"

	// StartedAt 2h ago — well beyond asyncWaitCeiling (90 min).
	staleStart := time.Now().Add(-2 * time.Hour).UTC()
	// PID 0 means workerPIDAlive returns false immediately (pid<=0 guard in
	// workerPIDAlive). We set it 0 so the liveness check would also fire —
	// but the backstop should trip first on the very first loop iteration.
	seedRunningStateWithOpts(t, runID, 0, staleStart)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (backstop must fire)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "did not complete within") {
		t.Errorf("stderr must contain 'did not complete within'; got:\n%s", stderr)
	}
}

// ── TestRunAsync_DefaultRunIsUnchanged verifies the existing blocking behaviour
// is preserved when --async is not passed (mirrors the existing
// TestRun_RunCommand_NotifiesAndExitsTwoWhenNoBench test but is explicit about
// the no-async contract).
func TestRunAsync_DefaultRunIsUnchanged(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"run", "--config", cfgPath, "--target", "linux"}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	// Must still behave like blocking run: exit 2 + handoff banner.
	if code != exitInconclusive {
		t.Errorf("exit = %d, want %d (blocking run, no Bench)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "handed off to Docker") {
		t.Errorf("blocking handoff banner missing; stderr:\n%s", stderr)
	}
}
