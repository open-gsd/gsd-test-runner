package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

func artifactTestReport(os string, outcome report.Outcome, failed, total int) report.Report {
	r := report.New(os, "bench-"+os, "img", "v1", time.Unix(0, 0).UTC())
	r.Total = total
	r.Passed = total - failed
	r.Failed = failed
	r.Outcome = outcome
	return r
}

// TestEmitRunArtifacts_VerdictIsLastLine pins the Option C contract for the
// standard path: the final stdout line is the machine verdict, in every outcome.
func TestEmitRunArtifacts_VerdictIsLastLine(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	reps := []report.Report{
		artifactTestReport("linux", report.OutcomeFailed, 1, 2),
		artifactTestReport("windows", report.OutcomePassed, 0, 2),
	}

	var stdout, stderr bytes.Buffer
	emitRunArtifacts(reps, nil, &stdout, &stderr)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	var v map[string]any
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("last stdout line is not JSON: %q (%v)", last, err)
	}
	if v["type"] != "verdict" {
		t.Errorf("type = %v, want verdict", v["type"])
	}
	if v["outcome"] != "failed" {
		t.Errorf("outcome = %v, want failed (worst-of across OSes)", v["outcome"])
	}
	if _, ok := v["per_os"].(map[string]any)["windows"]; !ok {
		t.Errorf("per_os missing windows: %v", v["per_os"])
	}
}

// TestEmitRunDieArtifacts_VerdictIsLastLine pins Option C for the run-and-die
// path: `gsd-test run`/`wait` print the same machine verdict as the final stdout
// line, under the run's existing run-id, including a reaped outcome.
func TestEmitRunDieArtifacts_VerdictIsLastLine(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := artifactTestReport("linux", report.OutcomeReaped, 0, 3)

	var stdout, stderr bytes.Buffer
	emitRunDieArtifacts("run-test-1", rep, &stdout, &stderr)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	var v map[string]any
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("last stdout line is not JSON: %q (%v)", last, err)
	}
	if v["type"] != "verdict" {
		t.Errorf("type = %v, want verdict", v["type"])
	}
	if v["outcome"] != "reaped" {
		t.Errorf("outcome = %v, want reaped", v["outcome"])
	}
	art, _ := v["artifacts"].(map[string]any)
	if art == nil || art["dir"] == "" {
		t.Errorf("expected artifacts.dir to be set, got %v", v["artifacts"])
	}
}

// TestCopyEventsJSONL verifies Option B persistence: each non-empty per-OS JSONL
// is copied into the run dir as test-events-<os>.jsonl; empty paths are skipped.
func TestCopyEventsJSONL(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	content := `{"type":"test_event","kind":"pass","name":"x"}` + "\n"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	copyEventsJSONL(dir, map[string]string{"linux": src, "windows": ""}, &stderr)

	got, err := os.ReadFile(filepath.Join(dir, "test-events-linux.jsonl"))
	if err != nil {
		t.Fatalf("expected linux JSONL persisted: %v", err)
	}
	if string(got) != content {
		t.Errorf("JSONL content mismatch: got %q want %q", got, content)
	}
	if _, err := os.Stat(filepath.Join(dir, "test-events-windows.jsonl")); !os.IsNotExist(err) {
		t.Errorf("empty src should be skipped, but a file was written")
	}
}
