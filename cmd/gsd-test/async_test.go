package main

import (
	"os"
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
	if stdout != wantText {
		t.Errorf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, wantText)
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

// TestRunAsync_DefaultRunIsUnchanged verifies the existing blocking behaviour
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
