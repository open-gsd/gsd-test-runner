package runner

import (
	"bytes"
	"encoding/json"
	"io"
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

func TestEmitRunDieArtifacts_VerdictIsLastLine(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := artifactTestReport("linux", report.OutcomeReaped, 0, 3)

	var stdout, stderr bytes.Buffer
	EmitRunDieArtifacts("run-test-1", rep, &stdout, &stderr)

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

func TestCopyEventsJSONL_ReturnsPersistedPath(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/src.jsonl"
	if err := os.WriteFile(src, []byte(`{"kind":"pass"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	osJSONL := map[string]string{"linux": src}
	got := copyEventsJSONL(dir, osJSONL, io.Discard)
	if got == "" {
		t.Fatalf("copyEventsJSONL: expected non-empty path, got empty string")
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("copyEventsJSONL returned path %q but file does not exist: %v", got, err)
	}
}

func TestCopyEventsJSONL_EmptySource_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got := copyEventsJSONL(dir, map[string]string{}, io.Discard)
	if got != "" {
		t.Errorf("copyEventsJSONL(empty): want empty, got %q", got)
	}
}

func TestCopyEventsJSONL_SkipsEmptySrcPaths(t *testing.T) {
	dir := t.TempDir()
	got := copyEventsJSONL(dir, map[string]string{"linux": ""}, io.Discard)
	if got != "" {
		t.Errorf("copyEventsJSONL(empty src): want empty, got %q", got)
	}
}
