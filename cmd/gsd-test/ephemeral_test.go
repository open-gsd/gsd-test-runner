package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// seedDoneStateWithKeep seeds a done State with the given Keep value and returns
// the runID. It mirrors seedDoneState but lets the caller control Keep.
func seedDoneStateWithKeep(t *testing.T, runID string, rep *report.Report, exitCode int, keep bool) {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("seedDoneStateWithKeep: parse spec: %v", err)
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
		Keep:      keep,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("seedDoneStateWithKeep: save: %v", err)
	}
}

// TestWaitRun_EphemeralReleasesRun verifies that waitRun with Keep=false
// (ephemeral) releases the run dir and state after rendering the verdict.
// The stdout must contain a "type":"verdict" line, and after waitRun returns,
// the state file and run dir must be gone.
func TestWaitRun_EphemeralReleasesRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "ephemeral-release-test-001"
	rep := &report.Report{
		Outcome: report.OutcomePassed,
		Total:   1,
		Passed:  1,
		PerTest: []report.TestStat{
			{File: "a.test.js", Name: "passes", DurationMs: 10, Status: "passed", ExitedClean: true},
		},
	}

	// Seed a done state with Keep=false (ephemeral).
	seedDoneStateWithKeep(t, runID, rep, exitAllPass, false)

	// Create the run dir and write a dummy artifact in it.
	runDir, err := runstate.EnsureRunDir(runID)
	if err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	if err := os.WriteFile(runDir+"/dummy.txt", []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write dummy artifact: %v", err)
	}

	// Call waitRun via run() to exercise the real dispatch.
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	// Exit code must reflect the report outcome (pass = 0).
	if code != exitAllPass {
		t.Errorf("exit code: got %d, want %d", code, exitAllPass)
	}

	// stdout must contain a verdict line.
	var foundVerdict bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if strings.Contains(line, `"type":"verdict"`) {
			foundVerdict = true
			break
		}
	}
	if !foundVerdict {
		t.Errorf("stdout must contain a verdict line; got:\n%s", stdout)
	}

	// After waitRun, the state file must be gone (runstate.Load returns ErrNotFound).
	_, loadErr := runstate.Load(runID)
	if !errors.Is(loadErr, runstate.ErrNotFound) {
		t.Errorf("after ephemeral waitRun, Load(%s) should return ErrNotFound; got: %v", runID, loadErr)
	}

	// The run dir must also be gone.
	if _, statErr := os.Stat(runDir); !os.IsNotExist(statErr) {
		t.Errorf("after ephemeral waitRun, run dir %s should be gone; Stat: %v", runDir, statErr)
	}
}

// TestWaitRun_KeepPreservesRun verifies that waitRun with Keep=true (non-ephemeral)
// leaves the state file and run dir intact after rendering the verdict.
func TestWaitRun_KeepPreservesRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "keep-preserve-test-001"
	rep := &report.Report{
		Outcome: report.OutcomePassed,
		Total:   1,
		Passed:  1,
		PerTest: []report.TestStat{
			{File: "b.test.js", Name: "passes", DurationMs: 15, Status: "passed", ExitedClean: true},
		},
	}

	// Seed a done state with Keep=true (non-ephemeral).
	seedDoneStateWithKeep(t, runID, rep, exitAllPass, true)

	// Create the run dir and write a dummy artifact.
	runDir, err := runstate.EnsureRunDir(runID)
	if err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	if err := os.WriteFile(runDir+"/dummy.txt", []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write dummy artifact: %v", err)
	}

	// Call waitRun via run().
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	code := run([]string{"wait", runID}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	// Exit code must reflect the report outcome (pass = 0).
	if code != exitAllPass {
		t.Errorf("exit code: got %d, want %d", code, exitAllPass)
	}

	// stdout must contain a verdict line.
	if !lastLineIsVerdict(stdout) {
		t.Errorf("expected the final stdout line to be the machine verdict; got:\n%s", stdout)
	}

	// After non-ephemeral waitRun, state must still exist.
	st, loadErr := runstate.Load(runID)
	if loadErr != nil {
		t.Errorf("after keep waitRun, Load(%s) should succeed; got: %v", runID, loadErr)
	}
	if st.Status != runstate.StatusDone {
		t.Errorf("state.Status: got %q, want %q", st.Status, runstate.StatusDone)
	}

	// The run dir must still exist.
	if _, statErr := os.Stat(runDir); statErr != nil {
		t.Errorf("after keep waitRun, run dir %s should still exist; Stat: %v", runDir, statErr)
	}
}
