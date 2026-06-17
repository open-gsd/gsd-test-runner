package telemetry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- RepoLogPath ---

func TestRepoLogPath_PersistentWorkstationPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	got := RepoLogPath("/tmp/scratch/myrepo")
	want := "/state/gsd-test/myrepo/telemetry.jsonl"
	if got != want {
		t.Errorf("RepoLogPath = %q, want %q", got, want)
	}
	// Must not live inside the (ephemeral) repo/worktree.
	if filepath.Dir(filepath.Dir(got)) == "/tmp/scratch/myrepo" {
		t.Error("telemetry must not be stored inside the worktree")
	}
}

// --- Append + Load round-trip ---

func TestAppend_Load_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gsd-test", "telemetry.jsonl")

	recs := []RunRecord{
		{
			RunID:      "run-001",
			Target:     "linux",
			Outcome:    "passed",
			DurationMs: 1234,
			Reaped:     false,
			PerTest: []TestStat{
				{File: "foo_test.go", Name: "TestFoo", DurationMs: 100, Status: "passed", ExitedClean: true},
				{File: "bar_test.go", Name: "TestBar", DurationMs: 200, Status: "passed", ExitedClean: true},
			},
		},
		{
			RunID:      "run-002",
			Target:     "windows",
			Outcome:    "reaped",
			DurationMs: 5000,
			Reaped:     true,
			ReapReason: "timeout",
			PerTest: []TestStat{
				{File: "slow_test.go", Name: "TestSlow", DurationMs: 4900, Status: "killed", PeakRssBytes: 1024, ExitedClean: false},
			},
		},
	}

	for _, rec := range recs {
		if err := Append(path, rec); err != nil {
			t.Fatalf("Append(%q, %v) error: %v", path, rec.RunID, err)
		}
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !reflect.DeepEqual(got, recs) {
		t.Errorf("Load returned:\n%+v\nwant:\n%+v", got, recs)
	}
}

func TestAppend_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	// nested dirs that don't exist yet
	path := filepath.Join(dir, "a", "b", "c", "telemetry.jsonl")
	rec := RunRecord{RunID: "x", Target: "linux", Outcome: "passed", DurationMs: 1}
	if err := Append(path, rec); err != nil {
		t.Fatalf("Append failed when parent dirs don't exist: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestAppend_MultipleCallsAccumulate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")

	for i := 0; i < 5; i++ {
		rec := RunRecord{RunID: "id", Target: "linux", Outcome: "passed", DurationMs: int64(i)}
		if err := Append(path, rec); err != nil {
			t.Fatalf("Append call %d: %v", i, err)
		}
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("Load returned %d records, want 5", len(got))
	}
}

// --- Load edge cases ---

func TestLoad_MissingFile_ReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.jsonl")

	recs, err := Load(path)
	if err != nil {
		t.Errorf("Load of missing file returned error %v, want nil", err)
	}
	if recs != nil {
		t.Errorf("Load of missing file returned %v, want nil", recs)
	}
}

func TestLoad_MalformedLine_ErrorContainsLineNumber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")

	// Write one valid line then one malformed line
	content := `{"run_id":"r1","target":"linux","outcome":"passed","duration_ms":10}` + "\n" +
		`{not valid json` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load of malformed line returned nil error, want error")
	}

	mle, ok := err.(*MalformedLineError)
	if !ok {
		t.Fatalf("error type = %T, want *MalformedLineError", err)
	}
	if mle.Line != 2 {
		t.Errorf("MalformedLineError.Line = %d, want 2", mle.Line)
	}
}

func TestLoad_MalformedLine_FirstLineErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")

	if err := os.WriteFile(path, []byte("not-json\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	mle, ok := err.(*MalformedLineError)
	if !ok {
		t.Fatalf("error type = %T, want *MalformedLineError", err)
	}
	if mle.Line != 1 {
		t.Errorf("MalformedLineError.Line = %d, want 1", mle.Line)
	}
}

// --- Leaderboard ---

func TestLeaderboard_RanksKilledTestsCorrectly(t *testing.T) {
	// Build records: slowTest killed in 3 of 5 runs, fastTest killed once.
	records := []RunRecord{
		// Run 1: slowTest killed → reaper trip for slowTest
		{RunID: "r1", Outcome: "reaped", PerTest: []TestStat{
			{File: "slow_test.go", Name: "TestSlow", Status: "killed"},
			{File: "fast_test.go", Name: "TestFast", Status: "passed"},
		}},
		// Run 2: slowTest killed
		{RunID: "r2", Outcome: "reaped", PerTest: []TestStat{
			{File: "slow_test.go", Name: "TestSlow", Status: "killed"},
		}},
		// Run 3: slowTest killed
		{RunID: "r3", Outcome: "reaped", PerTest: []TestStat{
			{File: "slow_test.go", Name: "TestSlow", Status: "killed"},
		}},
		// Run 4: fastTest killed
		{RunID: "r4", Outcome: "reaped", PerTest: []TestStat{
			{File: "fast_test.go", Name: "TestFast", Status: "killed"},
		}},
		// Run 5: all passed
		{RunID: "r5", Outcome: "passed", PerTest: []TestStat{
			{File: "slow_test.go", Name: "TestSlow", Status: "passed"},
			{File: "fast_test.go", Name: "TestFast", Status: "passed"},
		}},
	}

	got := Leaderboard(records)

	want := []SuspectTest{
		{File: "slow_test.go", Name: "TestSlow", ReaperTrips: 3, Runs: 4},
		{File: "fast_test.go", Name: "TestFast", ReaperTrips: 1, Runs: 3},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Leaderboard =\n%+v\nwant:\n%+v", got, want)
	}
}

func TestLeaderboard_ExcludesZeroTrips(t *testing.T) {
	records := []RunRecord{
		{RunID: "r1", Outcome: "passed", PerTest: []TestStat{
			{File: "a_test.go", Name: "TestA", Status: "passed"},
		}},
	}
	got := Leaderboard(records)
	if len(got) != 0 {
		t.Errorf("Leaderboard = %+v, want empty slice", got)
	}
}

func TestLeaderboard_SortsByReaperTripsDescThenFileAsc(t *testing.T) {
	// Two tests with same trip count — sort by file asc.
	records := []RunRecord{
		{RunID: "r1", Outcome: "reaped", PerTest: []TestStat{
			{File: "z_test.go", Name: "TestZ", Status: "killed"},
			{File: "a_test.go", Name: "TestA", Status: "killed"},
		}},
	}

	got := Leaderboard(records)
	if len(got) != 2 {
		t.Fatalf("got %d suspects, want 2", len(got))
	}
	if got[0].File != "a_test.go" {
		t.Errorf("first suspect File = %q, want %q", got[0].File, "a_test.go")
	}
	if got[1].File != "z_test.go" {
		t.Errorf("second suspect File = %q, want %q", got[1].File, "z_test.go")
	}
}

func TestLeaderboard_EmptyRecords(t *testing.T) {
	got := Leaderboard(nil)
	if len(got) != 0 {
		t.Errorf("Leaderboard(nil) = %+v, want empty", got)
	}
}

func TestLeaderboard_RunCountIsCorrect(t *testing.T) {
	// TestFoo appears in 3 runs, killed in 2.
	records := []RunRecord{
		{RunID: "r1", Outcome: "reaped", PerTest: []TestStat{{File: "f_test.go", Name: "TestFoo", Status: "killed"}}},
		{RunID: "r2", Outcome: "reaped", PerTest: []TestStat{{File: "f_test.go", Name: "TestFoo", Status: "killed"}}},
		{RunID: "r3", Outcome: "passed", PerTest: []TestStat{{File: "f_test.go", Name: "TestFoo", Status: "passed"}}},
	}
	got := Leaderboard(records)
	if len(got) != 1 {
		t.Fatalf("got %d suspects, want 1", len(got))
	}
	if got[0].Runs != 3 {
		t.Errorf("Runs = %d, want 3", got[0].Runs)
	}
	if got[0].ReaperTrips != 2 {
		t.Errorf("ReaperTrips = %d, want 2", got[0].ReaperTrips)
	}
}

func TestMedianDurationMs_PassingRunsForTarget(t *testing.T) {
	recs := []RunRecord{
		{Target: "linux", Outcome: "passed", DurationMs: 100},
		{Target: "linux", Outcome: "passed", DurationMs: 300},
		{Target: "linux", Outcome: "passed", DurationMs: 200},
		{Target: "linux", Outcome: "reaped", DurationMs: 999999}, // excluded
		{Target: "windows", Outcome: "passed", DurationMs: 50},   // other target
	}
	if got := MedianDurationMs(recs, "linux"); got != 200 {
		t.Errorf("MedianDurationMs(linux) = %d, want 200", got)
	}
}

func TestMedianDurationMs_EvenCountAverages(t *testing.T) {
	recs := []RunRecord{
		{Target: "linux", Outcome: "passed", DurationMs: 100},
		{Target: "linux", Outcome: "passed", DurationMs: 200},
	}
	if got := MedianDurationMs(recs, "linux"); got != 150 {
		t.Errorf("MedianDurationMs = %d, want 150", got)
	}
}

func TestMedianDurationMs_NoMatches_ReturnsZero(t *testing.T) {
	if got := MedianDurationMs(nil, "linux"); got != 0 {
		t.Errorf("MedianDurationMs(nil) = %d, want 0", got)
	}
}
