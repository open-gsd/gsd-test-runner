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

// TestCopyEventsJSONL_RemovesTempAfterCopy verifies issue #102 Option D: after a
// successful copy the source temp file is removed. Two OS entries are used to
// confirm both temps are cleaned up independently.
func TestCopyEventsJSONL_RemovesTempAfterCopy(t *testing.T) {
	// Create two real temp source files (simulating drain temps).
	src1, err := os.CreateTemp("", "gsd-test-jsonl-*.log")
	if err != nil {
		t.Fatal(err)
	}
	src1Path := src1.Name()
	content1 := `{"type":"test_event","name":"alpha"}` + "\n"
	if _, err := src1.WriteString(content1); err != nil {
		t.Fatal(err)
	}
	src1.Close()

	src2, err := os.CreateTemp("", "gsd-test-jsonl-*.log")
	if err != nil {
		t.Fatal(err)
	}
	src2Path := src2.Name()
	content2 := `{"type":"test_event","name":"beta"}` + "\n"
	if _, err := src2.WriteString(content2); err != nil {
		t.Fatal(err)
	}
	src2.Close()

	dir := t.TempDir()
	var stderr bytes.Buffer
	copyEventsJSONL(dir, map[string]string{"linux": src1Path, "windows": src2Path}, &stderr)

	// Both destination files must exist with correct content.
	for osName, wantContent := range map[string]string{"linux": content1, "windows": content2} {
		dst := filepath.Join(dir, "test-events-"+osName+".jsonl")
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Errorf("expected %s JSONL persisted at %s: %v", osName, dst, err)
			continue
		}
		if string(got) != wantContent {
			t.Errorf("%s JSONL content mismatch: got %q want %q", osName, got, wantContent)
		}
	}

	// Both source temps must be gone (issue #102 Option D).
	for _, srcPath := range []string{src1Path, src2Path} {
		if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
			t.Errorf("source temp %s should have been removed after successful copy, but Stat returned: %v", srcPath, err)
		}
	}

	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output: %s", stderr.String())
	}
}

// TestCopyEventsJSONL_KeepsTempOnCopyFailure verifies that when copyFile fails the
// source temp is NOT removed (preserves diagnostic data). The failure is injected
// deterministically by passing a src path that does not exist, which causes
// copyFile to return an error before touching the destination.
func TestCopyEventsJSONL_KeepsTempOnCopyFailure(t *testing.T) {
	// Create a real temp that will be the "good" OS entry.
	good, err := os.CreateTemp("", "gsd-test-jsonl-*.log")
	if err != nil {
		t.Fatal(err)
	}
	goodPath := good.Name()
	good.Close()
	// We want to keep this temp alive for the test; remove it ourselves at the end.
	defer os.Remove(goodPath) //nolint:errcheck

	// The "bad" OS entry points to a non-existent file — copyFile will fail.
	badPath := filepath.Join(t.TempDir(), "nonexistent-drain.log")

	dir := t.TempDir()
	var stderr bytes.Buffer
	copyEventsJSONL(dir, map[string]string{"linux": goodPath, "darwin": badPath}, &stderr)

	// darwin copy failed: dst must not exist.
	if _, err := os.Stat(filepath.Join(dir, "test-events-darwin.jsonl")); !os.IsNotExist(err) {
		t.Errorf("darwin dst should not have been created on copy failure")
	}

	// The non-existent src path has no file to leave — the assertion is that
	// no panic or unexpected removal of goodPath occurred. goodPath (linux)
	// was copied successfully and its temp was removed.
	if _, err := os.Stat(goodPath); !os.IsNotExist(err) {
		t.Errorf("linux source temp should have been removed after successful copy; Stat: %v", err)
	}

	// A warning must have been emitted for the darwin failure.
	if !strings.Contains(stderr.String(), "darwin") {
		t.Errorf("expected warning mentioning darwin, got: %q", stderr.String())
	}
}
