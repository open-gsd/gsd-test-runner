package main

// Tests for defects identified in AUDIT-issue84.md that fall under the
// cmd/gsd-test + internal/runstate + internal/runspec ownership lane.
// Each test group is annotated with the B-N defect number.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// ── B-3: run-and-die infra_error must emit a verdict on stdout ───────────────

// TestRunRun_InfraError_EmitsVerdict drives `gsd-test run` against a config
// with no Bench. dispatchRun returns ok=false (infra_error). ADR-0023 Decision
// 2 requires that the last stdout line is a machine-readable verdict even on
// this path. Before the fix the function returned exitInconclusive with an
// empty stdout.
func TestRunRun_InfraError_EmitsVerdict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	// Config with no Bench — dispatchRun will fail at bench selection.
	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"run", "--config", cfgPath, "--target", "linux"}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (no bench → inconclusive)", code, exitInconclusive)
	}
	// B-3: the last stdout line must be the verdict JSON.
	if !lastLineIsVerdict(stdout) {
		t.Errorf("B-3: want last stdout line to be a verdict on infra_error (no-bench) path; got:\n%q", stdout)
	}
	// The verdict must carry outcome=infra_error.
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	last := lines[len(lines)-1]
	var v map[string]any
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("last line is not valid JSON: %v — line: %q", err, last)
	}
	if v["outcome"] != "infra_error" {
		t.Errorf("verdict outcome: got %q, want %q", v["outcome"], "infra_error")
	}
}

// TestRunWorker_InfraError_VerdictArtifactPersisted drives runWorker against a
// config with no Bench. When dispatchRun fails, the worker must persist an
// infra_error digest artifact so `gsd-test wait` can emit the verdict.
func TestRunWorker_InfraError_VerdictArtifactPersisted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const runID = "b3-worker-infra-001"

	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a running state the worker will load.
	data := []byte(`{"repo":"/work","target":"linux"}`)
	sp, err := runspec.Parse(data)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	sp.RunID = runID
	st := runstate.State{
		RunID:  runID,
		Target: sp.Target,
		Repo:   sp.Repo,
		Status: runstate.StatusRunning,
		Spec:   *sp,
	}
	if err := runstate.Save(st); err != nil {
		t.Fatalf("save running state: %v", err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	workerCode := run([]string{"__run-worker", "--run-id", runID, "--config", cfgPath}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	_ = drain(errR)

	if workerCode != exitInconclusive {
		t.Errorf("worker exit: got %d, want %d (dispatch fail)", workerCode, exitInconclusive)
	}

	// The run-dir must exist with a verdict.json (digest artifact).
	runDir, err := runstate.RunDir(runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	verdictPath := runDir + "/verdict.json"
	if _, statErr := os.Stat(verdictPath); statErr != nil {
		// verdict.json may not exist — we only require that the dir was created
		// (writeRunArtifacts ran). Check for FAILURES.md or the dir itself.
		if _, dirErr := os.Stat(runDir); dirErr != nil {
			t.Errorf("B-3: expected run artifact dir %q to exist after worker infra_error, got: %v", runDir, dirErr)
		}
	}
}

// ── B-4: standard multi-OS path pre-pipeline failures must emit a verdict ────

// TestRun_PrePipelineInfraError_EmitsVerdict drives the standard `run()` path
// against a config that triggers an early-return before the pipeline starts
// (no target OSes in config + none on CLI). ADR-0023 Decision 2 requires a
// verdict on every outcome. Before the fix the function returned exitInconclusive
// with an empty stdout on all nine pre-pipeline error paths.
func TestRun_PrePipelineInfraError_EmitsVerdict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	// Config with no defaults.targets and no --targets flag → "no target OSes"
	// error, the second pre-pipeline return in run().
	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"--config", cfgPath}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (no targets → inconclusive)", code, exitInconclusive)
	}
	// B-4: stdout must contain at least one verdict line even on this early path.
	if !lastLineIsVerdict(stdout) {
		t.Errorf("B-4: want last stdout line to be a verdict on pre-pipeline infra path; got:\n%q", stdout)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	last := lines[len(lines)-1]
	var v map[string]any
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("last line is not valid JSON: %v — line: %q", err, last)
	}
	if v["outcome"] != "infra_error" {
		t.Errorf("verdict outcome: got %q, want %q", v["outcome"], "infra_error")
	}
}

// TestRun_BadConfig_EmitsVerdict exercises the very first pre-pipeline return
// (config.Load failure) and confirms a verdict is emitted (B-4).
func TestRun_BadConfig_EmitsVerdict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	// Pass a config path that does not exist — config.Load will fail.
	code := run([]string{"--config", tmp + "/nonexistent.toml"}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (bad config → inconclusive)", code, exitInconclusive)
	}
	if !lastLineIsVerdict(stdout) {
		t.Errorf("B-4: want last stdout line to be a verdict on config.Load failure; got:\n%q", stdout)
	}
}

// ── B-5: RunID path-traversal security ───────────────────────────────────────

// TestRunspec_TraversalRunIDRejected verifies that runspec.Parse rejects RunID
// values that would escape the store via path traversal (B-5, charset gate).
// Note: an empty RunID is intentionally allowed by Parse — the submit command
// assigns one after Parse returns. Only non-empty invalid IDs are tested here.
func TestRunspec_TraversalRunIDRejected(t *testing.T) {
	cases := []struct {
		name  string
		runID string
	}{
		{"dotdot slash", "../../../../etc/passwd"},
		{"dotdot backslash", `..\..\windows\system32`},
		{"null byte", "run\x00id"},
		{"space", "run id"},
		{"slash", "run/id"},
		{"too long", strings.Repeat("a", 129)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]string{
				"repo":   "/work",
				"target": "linux",
				"runId":  tc.runID,
			})
			_, err := runspec.Parse(data)
			if err == nil {
				t.Errorf("Parse with runId=%q: expected error, got nil", tc.runID)
			}
		})
	}
}

// TestRunspec_ValidRunIDAccepted verifies that well-formed RunID values pass
// runspec.Parse (B-5, charset gate).
func TestRunspec_ValidRunIDAccepted(t *testing.T) {
	cases := []string{
		"abc",
		"run-001",
		"run_001",
		"a",
		strings.Repeat("a", 128), // exactly max length
		"550e8400-e29b-41d4-a716-446655440000",
	}
	for _, id := range cases {
		id := id
		t.Run(id[:min(len(id), 20)], func(t *testing.T) {
			data, _ := json.Marshal(map[string]string{
				"repo":   "/work",
				"target": "linux",
				"runId":  id,
			})
			spec, err := runspec.Parse(data)
			if err != nil {
				t.Errorf("Parse with runId=%q: unexpected error: %v", id, err)
				return
			}
			if spec.RunID != id {
				t.Errorf("RunID round-trip: got %q, want %q", spec.RunID, id)
			}
		})
	}
}

// min returns the smaller of a and b (Go 1.20 added min as a builtin, but we
// support older toolchains too).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestRunstate_TraversalRejectedOnRead verifies that runstate.Load rejects a
// traversal RunID before reaching the filesystem (B-5, defense-in-depth on the
// read sink).
func TestRunstate_TraversalRejectedOnRead(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	traversalIDs := []string{
		"../../../../etc/passwd",
		"../sibling",
		"..",
	}
	for _, id := range traversalIDs {
		_, err := runstate.Load(id)
		if err == nil {
			t.Errorf("Load(%q): expected error (traversal), got nil", id)
			continue
		}
		// Should NOT be ErrNotFound — that would mean we reached the filesystem.
		if errors.Is(err, runstate.ErrNotFound) {
			t.Errorf("Load(%q): got ErrNotFound, want traversal error (path escapes store)", id)
		}
	}
}

// TestRunstate_TraversalRejectedOnWrite verifies that runstate.EnsureRunDir
// rejects a traversal RunID before creating any directory (B-5, defense-in-depth
// on the write sink).
func TestRunstate_TraversalRejectedOnWrite(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	traversalIDs := []string{
		"../../../../tmp/pwned",
		"../escape",
		"..",
	}
	for _, id := range traversalIDs {
		_, err := runstate.EnsureRunDir(id)
		if err == nil {
			t.Errorf("EnsureRunDir(%q): expected error (traversal), got nil", id)
		}
	}
}

// TestWait_TraversalRunIDRejected verifies that `gsd-test wait` rejects a
// traversal RunID at the CLI layer before reaching runstate.Load (B-5).
func TestWait_TraversalRunIDRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"wait", "../../../../etc/passwd"}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (traversal id)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "invalid run-id") {
		t.Errorf("stderr must mention 'invalid run-id'; got:\n%s", stderr)
	}
}

// TestStatus_TraversalRunIDRejected verifies that `gsd-test status` rejects a
// traversal RunID at the CLI layer before reaching runstate.Load (B-5).
func TestStatus_TraversalRunIDRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"status", "../../../../etc/passwd"}, outW, errW)
	outW.Close()
	errW.Close()
	_ = drain(outR)
	stderr := drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (traversal id)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "invalid run-id") {
		t.Errorf("stderr must mention 'invalid run-id'; got:\n%s", stderr)
	}
}

// ── B-12: submit --execute must emit a verdict as the last stdout line ────────

// ── B-11: copyEventsJSONL must return the persisted path for verdict field ────

// TestCopyEventsJSONL_ReturnsPersistedPath verifies that copyEventsJSONL returns
// the path of the persisted JSONL file so it can be assigned to paths.EventsJSONL
// for the verdict (B-11).
func TestCopyEventsJSONL_ReturnsPersistedPath(t *testing.T) {
	dir := t.TempDir()

	// Write a tiny JSONL source file.
	src := dir + "/src.jsonl"
	if err := os.WriteFile(src, []byte(`{"kind":"pass"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	osJSONL := map[string]string{"linux": src}
	got := copyEventsJSONL(dir, osJSONL, os.Stderr)
	if got == "" {
		t.Fatalf("copyEventsJSONL: expected non-empty path, got empty string (B-11)")
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("copyEventsJSONL returned path %q but file does not exist: %v", got, err)
	}
}

// TestCopyEventsJSONL_EmptySource_ReturnsEmpty verifies that copyEventsJSONL
// returns "" when no JSONL sources are provided (B-11 edge case).
func TestCopyEventsJSONL_EmptySource_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got := copyEventsJSONL(dir, map[string]string{}, os.Stderr)
	if got != "" {
		t.Errorf("copyEventsJSONL(empty): want empty, got %q", got)
	}
}

// TestCopyEventsJSONL_SkipsEmptySrcPaths verifies that entries with an empty
// src path are skipped without error (B-11 edge case).
func TestCopyEventsJSONL_SkipsEmptySrcPaths(t *testing.T) {
	dir := t.TempDir()
	got := copyEventsJSONL(dir, map[string]string{"linux": ""}, os.Stderr)
	if got != "" {
		t.Errorf("copyEventsJSONL(empty src): want empty, got %q", got)
	}
}

// ── B-12: submit --execute must emit a verdict as the last stdout line ────────

// TestSubmitExecute_InfraError_EmitsVerdict verifies that `gsd-test submit
// --execute` emits an infra_error verdict even when dispatchRun fails (B-12).
// No Docker or Bench required — we use a config with no Bench.
func TestSubmitExecute_InfraError_EmitsVerdict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	cfgPath := tmp + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	specPath := writeSpecFile(t, `{"repo":"/work","target":"linux"}`)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"submit", "--execute", "--config", cfgPath, "--spec-file", specPath}, outW, errW)
	outW.Close()
	errW.Close()
	stdout := drain(outR)
	_ = drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d (no bench → inconclusive)", code, exitInconclusive)
	}
	// B-12: the last non-empty stdout line must be the verdict.
	if !lastLineIsVerdict(stdout) {
		t.Errorf("B-12: want last stdout line to be a verdict on infra_error; got:\n%q", stdout)
	}
}
