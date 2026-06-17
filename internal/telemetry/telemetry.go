// Package telemetry implements per-repo telemetry aggregation for
// run-and-die execution (ADR-0021 Decision 3, §F). Records are stored
// as append-only JSONL files on the dev workstation, one file per repo.
package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// TestStat captures per-test telemetry within a single run.
type TestStat struct {
	File         string `json:"file"`
	Name         string `json:"name"`
	DurationMs   int64  `json:"duration_ms"`
	Status       string `json:"status"`
	PeakRssBytes int64  `json:"peak_rss_bytes,omitempty"`
	ExitedClean  bool   `json:"exited_clean"`
}

// RunRecord is the telemetry envelope fragment for one run (§F).
// Outcome is one of: passed, failed, reaped, infra_error.
type RunRecord struct {
	RunID      string     `json:"run_id"`
	Target     string     `json:"target"`
	Outcome    string     `json:"outcome"`
	DurationMs int64      `json:"duration_ms"`
	Reaped     bool       `json:"reaped"`
	ReapReason string     `json:"reap_reason,omitempty"`
	PerTest    []TestStat `json:"per_test,omitempty"`
}

// SuspectTest is an entry in the runaway leaderboard.
type SuspectTest struct {
	File        string
	Name        string
	ReaperTrips int // number of runs in which this test had Status=="killed"
	Runs        int // total number of runs in which this test appeared
}

// MalformedLineError is returned by Load when a line cannot be decoded as JSON.
// Line is 1-based.
type MalformedLineError struct {
	Line int
	Err  error
}

func (e *MalformedLineError) Error() string {
	return fmt.Sprintf("telemetry: malformed JSON at line %d: %v", e.Line, e.Err)
}

func (e *MalformedLineError) Unwrap() error { return e.Err }

// RepoLogPath returns the canonical JSONL telemetry path for a repo root.
func RepoLogPath(repo string) string {
	return filepath.Join(repo, ".gsd-test", "telemetry.jsonl")
}

// Append encodes rec as a compact JSON object and appends it (with a
// trailing newline) to the file at path. Parent directories are created
// if they do not exist.
func Append(path string, rec RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("telemetry: create dirs for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("telemetry: open %s: %w", path, err)
	}
	defer f.Close()

	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("telemetry: marshal record: %w", err)
	}
	b = append(b, '\n')

	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("telemetry: write to %s: %w", path, err)
	}
	return nil
}

// Load reads the JSONL file at path and returns all RunRecords. A missing
// file is not an error — it simply means no telemetry has been recorded yet,
// so (nil, nil) is returned. Any line that cannot be decoded returns a
// *MalformedLineError carrying the 1-based line number.
func Load(path string) ([]RunRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("telemetry: open %s: %w", path, err)
	}
	defer f.Close()

	var records []RunRecord
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, &MalformedLineError{Line: lineNum, Err: err}
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("telemetry: read %s: %w", path, err)
	}
	return records, nil
}

// Leaderboard computes the "runaway leaderboard" (§F) from a set of records.
//
// Heuristic: a test is considered to have tripped the reaper in a given run
// if its PerTest entry has Status == "killed". This covers both the explicit
// reaped outcome and any in-flight kill during an otherwise passing run. We
// do not additionally require Outcome == "reaped" at the run level because a
// single run can have multiple tests with killed status (e.g., parallel test
// pools). Aggregation is by (File, Name) across all records.
//
// Only tests with ReaperTrips >= 1 are included. Results are sorted by
// ReaperTrips descending, then File ascending as a stable tiebreaker.
func Leaderboard(records []RunRecord) []SuspectTest {
	type key struct{ file, name string }
	type stats struct{ trips, runs int }
	agg := make(map[key]*stats)

	for _, rec := range records {
		for _, ts := range rec.PerTest {
			k := key{ts.File, ts.Name}
			s, ok := agg[k]
			if !ok {
				s = &stats{}
				agg[k] = s
			}
			s.runs++
			if ts.Status == "killed" {
				s.trips++
			}
		}
	}

	var suspects []SuspectTest
	for k, s := range agg {
		if s.trips >= 1 {
			suspects = append(suspects, SuspectTest{
				File:        k.file,
				Name:        k.name,
				ReaperTrips: s.trips,
				Runs:        s.runs,
			})
		}
	}

	sort.Slice(suspects, func(i, j int) bool {
		if suspects[i].ReaperTrips != suspects[j].ReaperTrips {
			return suspects[i].ReaperTrips > suspects[j].ReaperTrips
		}
		return suspects[i].File < suspects[j].File
	})

	return suspects
}
