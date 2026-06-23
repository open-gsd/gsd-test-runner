// Package runstate_test — tests for Release and Prune (#102 Option C).
package runstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// makeRunID returns a well-formed run ID usable in tests.
func makeRunID(suffix string) string {
	return "prune-test-run-" + suffix
}

// saveState is a helper that saves a State with the given runID, status, and updatedAt.
func saveState(t *testing.T, runID, status string, updatedAt time.Time) {
	t.Helper()
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("saveState: parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:     runID,
		Target:    sp.Target,
		Repo:      sp.Repo,
		Status:    status,
		StartedAt: updatedAt,
		UpdatedAt: updatedAt,
		Spec:      *sp,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("saveState: %v", err)
	}
}

// TestRelease_RemovesAllArtifacts verifies that Release deletes the json state,
// the artifact directory, and the worker log for a run.
func TestRelease_RemovesAllArtifacts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	runID := makeRunID("release-a")
	now := time.Now().UTC()

	// Save a state file.
	saveState(t, runID, runstate.StatusDone, now)

	// Create the artifact directory with a file inside.
	artifactDir, err := runstate.EnsureRunDir(runID)
	if err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	artifactFile := filepath.Join(artifactDir, "FAILURES.md")
	if err := os.WriteFile(artifactFile, []byte("# Failures\n"), 0o644); err != nil {
		t.Fatalf("write artifact file: %v", err)
	}

	// Create a worker log file.
	storeDir, err := runstate.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	workerLogPath := filepath.Join(storeDir, runID+".worker.log")
	if err := os.WriteFile(workerLogPath, []byte("log output\n"), 0o644); err != nil {
		t.Fatalf("write worker log: %v", err)
	}

	// Release.
	if err := runstate.Release(runID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// State file must be gone.
	jsonPath, err := runstate.Path(runID)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Errorf("state json still exists after Release")
	}

	// Artifact dir must be gone.
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Errorf("artifact dir still exists after Release")
	}

	// Worker log must be gone.
	if _, err := os.Stat(workerLogPath); !os.IsNotExist(err) {
		t.Errorf("worker log still exists after Release")
	}

	// Calling Release again on a non-existent run must return nil (idempotent).
	if err := runstate.Release(runID); err != nil {
		t.Errorf("Release on non-existent run returned error: %v", err)
	}
}

// TestPrune_TTL verifies that Prune removes runs older than TTL and keeps newer ones.
func TestPrune_TTL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	now := time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	cutoff := now.Add(-ttl) // 2025-01-09 12:00:00

	oldID := makeRunID("ttl-old")
	newID := makeRunID("ttl-new")

	// Old run: updated 48h before now (should be pruned).
	saveState(t, oldID, runstate.StatusDone, now.Add(-48*time.Hour))
	// New run: updated 1h before now (should be kept).
	saveState(t, newID, runstate.StatusDone, now.Add(-1*time.Hour))

	_ = cutoff // used implicitly by TTL logic

	n, err := runstate.Prune(runstate.PruneOptions{
		TTL:          ttl,
		KeepLastRuns: 0,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune removed %d runs, want 1", n)
	}

	// Old run must be gone.
	if _, loadErr := runstate.Load(oldID); !isNotFound(loadErr) {
		t.Errorf("old run still exists after Prune (err=%v)", loadErr)
	}

	// New run must still exist.
	if _, loadErr := runstate.Load(newID); loadErr != nil {
		t.Errorf("new run was unexpectedly pruned: %v", loadErr)
	}
}

// TestPrune_KeepLastRuns verifies that only the N newest runs survive.
func TestPrune_KeepLastRuns(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	now := time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC)
	base := now.Add(-10 * time.Hour)

	ids := []string{
		makeRunID("klr-a"),
		makeRunID("klr-b"),
		makeRunID("klr-c"),
		makeRunID("klr-d"),
		makeRunID("klr-e"),
	}

	// Save 5 runs with distinct timestamps (a=oldest, e=newest).
	for i, id := range ids {
		saveState(t, id, runstate.StatusDone, base.Add(time.Duration(i)*time.Hour))
	}

	n, err := runstate.Prune(runstate.PruneOptions{
		TTL:          0,
		KeepLastRuns: 2,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("Prune removed %d runs, want 3", n)
	}

	// The 3 oldest (a, b, c) should be gone.
	for _, id := range ids[:3] {
		if _, loadErr := runstate.Load(id); !isNotFound(loadErr) {
			t.Errorf("run %s still exists after Prune (err=%v)", id, loadErr)
		}
	}

	// The 2 newest (d, e) should remain.
	for _, id := range ids[3:] {
		if _, loadErr := runstate.Load(id); loadErr != nil {
			t.Errorf("run %s was unexpectedly pruned: %v", id, loadErr)
		}
	}
}

// TestPrune_NeverPrunesRunning verifies that a StatusRunning run is never removed.
func TestPrune_NeverPrunesRunning(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	now := time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC)

	runningID := makeRunID("running-keep")
	doneID := makeRunID("done-prune")

	// Running run: very old — should NOT be pruned.
	saveState(t, runningID, runstate.StatusRunning, now.Add(-7*24*time.Hour))
	// Done run: also old — should be pruned.
	saveState(t, doneID, runstate.StatusDone, now.Add(-7*24*time.Hour))

	n, err := runstate.Prune(runstate.PruneOptions{
		TTL:          1 * time.Hour, // very aggressive: everything older than 1h is eligible
		KeepLastRuns: 0,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune removed %d runs, want 1 (only the done run)", n)
	}

	// Running run must still exist.
	if _, loadErr := runstate.Load(runningID); loadErr != nil {
		t.Errorf("running run was unexpectedly pruned: %v", loadErr)
	}

	// Done run must be gone.
	if _, loadErr := runstate.Load(doneID); !isNotFound(loadErr) {
		t.Errorf("done run still exists after Prune (err=%v)", loadErr)
	}
}

// TestPrune_SkipsForeignAndTempFiles verifies that Prune ignores temp files
// and non-conforming names without erroring.
func TestPrune_SkipsForeignAndTempFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	storeDir, err := runstate.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	// Create the store dir so we can write into it.
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Temp file (atomic-write artifact) — must be ignored.
	tmpFile := filepath.Join(storeDir, ".runstate-tmp-xyz")
	if err := os.WriteFile(tmpFile, []byte("temp"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Foreign file with an invalid run ID (contains '/') — actually, path component
	// with spaces — not matching [A-Za-z0-9_-].
	foreignFile := filepath.Join(storeDir, "foreign file.txt")
	if err := os.WriteFile(foreignFile, []byte("foreign"), 0o644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	// Also add a valid run that should survive KeepLastRuns=1.
	keepID := makeRunID("skip-keep")
	now := time.Now().UTC()
	saveState(t, keepID, runstate.StatusDone, now)

	n, err := runstate.Prune(runstate.PruneOptions{
		TTL:          0,
		KeepLastRuns: 1,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune removed %d runs, want 0", n)
	}

	// Temp file must still be present.
	if _, statErr := os.Stat(tmpFile); statErr != nil {
		t.Errorf("temp file was unexpectedly removed: %v", statErr)
	}

	// Foreign file must still be present.
	if _, statErr := os.Stat(foreignFile); statErr != nil {
		t.Errorf("foreign file was unexpectedly removed: %v", statErr)
	}
}

// isNotFound returns true if err wraps runstate.ErrNotFound.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// runstate.Load wraps ErrNotFound via fmt.Errorf("... %w", ErrNotFound).
	return errors.Is(err, runstate.ErrNotFound)
}
