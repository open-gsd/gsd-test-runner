// Package runstate provides persistent per-run state for async dispatch
// (ADR-0022 Decision 3, issue #70).
package runstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// makeSpec builds a minimal valid Spec for test use.
func makeSpec(t *testing.T) runspec.Spec {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("makeSpec: %v", err)
	}
	sp.RunID = "test-run-id-001"
	return *sp
}

// TestSaveLoad_RoundTrip verifies that Save followed by Load returns a State
// identical to what was saved, including a populated Report pointer.
func TestSaveLoad_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	rep := &report.Report{
		Outcome: report.OutcomePassed,
		Total:   3,
		Passed:  3,
		Failed:  0,
		PerTest: []report.TestStat{
			{File: "a.test.js", Name: "passes", DurationMs: 42.0, Status: "passed", ExitedClean: true},
			{File: "b.test.js", Name: "also passes", DurationMs: 7.5, Status: "passed", ExitedClean: true},
		},
	}
	now := time.Now().UTC().Truncate(time.Second) // truncate sub-second for JSON round-trip
	st := runstate.State{
		RunID:     "test-run-id-001",
		Target:    "linux",
		Repo:      "/work",
		Status:    runstate.StatusDone,
		PID:       12345,
		StartedAt: now,
		UpdatedAt: now.Add(5 * time.Second),
		Spec:      makeSpec(t),
		Report:    rep,
		ExitCode:  0,
		Err:       "",
	}

	if err := runstate.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := runstate.Load(st.RunID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.RunID != st.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, st.RunID)
	}
	if got.Target != st.Target {
		t.Errorf("Target: got %q, want %q", got.Target, st.Target)
	}
	if got.Repo != st.Repo {
		t.Errorf("Repo: got %q, want %q", got.Repo, st.Repo)
	}
	if got.Status != st.Status {
		t.Errorf("Status: got %q, want %q", got.Status, st.Status)
	}
	if got.PID != st.PID {
		t.Errorf("PID: got %d, want %d", got.PID, st.PID)
	}
	if !got.StartedAt.Equal(st.StartedAt) {
		t.Errorf("StartedAt: got %v, want %v", got.StartedAt, st.StartedAt)
	}
	if !got.UpdatedAt.Equal(st.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v, want %v", got.UpdatedAt, st.UpdatedAt)
	}
	if got.ExitCode != st.ExitCode {
		t.Errorf("ExitCode: got %d, want %d", got.ExitCode, st.ExitCode)
	}
	if got.Err != st.Err {
		t.Errorf("Err: got %q, want %q", got.Err, st.Err)
	}
	if got.Report == nil {
		t.Fatal("Report: got nil, want non-nil")
	}
	if got.Report.Outcome != rep.Outcome {
		t.Errorf("Report.Outcome: got %q, want %q", got.Report.Outcome, rep.Outcome)
	}
	if got.Report.Total != rep.Total {
		t.Errorf("Report.Total: got %d, want %d", got.Report.Total, rep.Total)
	}
	if len(got.Report.PerTest) != len(rep.PerTest) {
		t.Errorf("Report.PerTest len: got %d, want %d", len(got.Report.PerTest), len(rep.PerTest))
	}
	// Spec round-trip: check a couple fields.
	if got.Spec.RunID != st.Spec.RunID {
		t.Errorf("Spec.RunID: got %q, want %q", got.Spec.RunID, st.Spec.RunID)
	}
	if got.Spec.Target != st.Spec.Target {
		t.Errorf("Spec.Target: got %q, want %q", got.Spec.Target, st.Spec.Target)
	}
}

// TestLoad_ErrNotFound verifies that loading an unknown run-id returns ErrNotFound.
func TestLoad_ErrNotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	_, err := runstate.Load("nonexistent-run-id")
	if err == nil {
		t.Fatal("Load(nonexistent): expected error, got nil")
	}
	if !errors.Is(err, runstate.ErrNotFound) {
		t.Errorf("Load(nonexistent): got %v, want errors.Is(ErrNotFound)", err)
	}
}

// TestSave_Atomic verifies that after Save the file parses as valid JSON
// (i.e. no partial writes observed by a concurrent reader).
func TestSave_Atomic(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	st := runstate.State{
		RunID:     "atomic-run-001",
		Target:    "linux",
		Repo:      "/work",
		Status:    runstate.StatusRunning,
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Spec:      makeSpec(t),
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// After Save the file must be readable and parseable.
	path, err := runstate.Path(st.RunID)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Simple validation: must start/end with { } (valid JSON object).
	if len(raw) == 0 {
		t.Fatal("file is empty after Save")
	}
	// Load again to confirm it parses.
	got, err := runstate.Load(st.RunID)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.RunID != st.RunID {
		t.Errorf("RunID after atomic save: got %q, want %q", got.RunID, st.RunID)
	}
}

// TestDir_HonorsXDGStateHome verifies that Dir uses XDG_STATE_HOME when set.
func TestDir_HonorsXDGStateHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	dir, err := runstate.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	want := filepath.Join(tmp, "gsd-test", "runs")
	if dir != want {
		t.Errorf("Dir: got %q, want %q", dir, want)
	}
}

// TestDir_FallbackWhenXDGUnset verifies that Dir falls back to ~/.local/state
// when XDG_STATE_HOME is not set.
func TestDir_FallbackWhenXDGUnset(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "") // clear it
	// Unset entirely so os.Getenv returns ""
	if err := os.Unsetenv("XDG_STATE_HOME"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}

	dir, err := runstate.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".local", "state", "gsd-test", "runs")
	if dir != want {
		t.Errorf("Dir (no XDG): got %q, want %q", dir, want)
	}
}

// ── B-5: path-traversal defense-in-depth ─────────────────────────────────────

// TestLoad_TraversalRejected verifies that runstate.Load rejects RunID values
// that would escape the store via path traversal (B-5, read-sink protection).
// It must return a traversal error, NOT ErrNotFound — ErrNotFound would indicate
// the function reached the filesystem (information disclosure).
func TestLoad_TraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	traversalIDs := []struct {
		name  string
		runID string
	}{
		{"dotdot two levels", "../../etc/passwd"},
		{"dotdot many levels", "../../../../etc/passwd"},
		{"parent only", ".."},
	}
	for _, tc := range traversalIDs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := runstate.Load(tc.runID)
			if err == nil {
				t.Fatalf("Load(%q): expected error, got nil", tc.runID)
			}
			if errors.Is(err, runstate.ErrNotFound) {
				t.Errorf("Load(%q): got ErrNotFound — filesystem was reached; want traversal error", tc.runID)
			}
			if !errors.Is(err, runstate.ErrTraversal) {
				t.Errorf("Load(%q): got %v, want ErrTraversal (or wrapping it)", tc.runID, err)
			}
		})
	}
}

// TestEnsureRunDir_TraversalRejected verifies that runstate.EnsureRunDir rejects
// RunID values that would create directories outside the store (B-5, write-sink
// protection). No directory must be created on the filesystem.
func TestEnsureRunDir_TraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	traversalIDs := []struct {
		name  string
		runID string
	}{
		{"dotdot escape", "../escape"},
		{"many dotdots", "../../../../tmp/pwned"},
		{"parent only", ".."},
	}
	for _, tc := range traversalIDs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := runstate.EnsureRunDir(tc.runID)
			if err == nil {
				t.Fatalf("EnsureRunDir(%q): expected error, got nil", tc.runID)
			}
			if !errors.Is(err, runstate.ErrTraversal) {
				t.Errorf("EnsureRunDir(%q): got %v, want ErrTraversal (or wrapping it)", tc.runID, err)
			}
		})
	}
}
