package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Integration tests for run() itself require a real Docker daemon, real
// Bench machines, and a real git repo with reachable refs. Those are
// out of scope for unit tests. This file covers the two purely-functional
// surfaces: parseFlags and commaSplit.

// ── parseFlags ────────────────────────────────────────────────────────────────

func TestParseFlags_Defaults(t *testing.T) {
	f, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags([]): unexpected error: %v", err)
	}
	if f.printVersion {
		t.Error("printVersion: got true, want false")
	}
	if f.base != "main" {
		t.Errorf("base: got %q, want %q", f.base, "main")
	}
	if f.head != "HEAD" {
		t.Errorf("head: got %q, want %q", f.head, "HEAD")
	}
	if f.source != "." {
		t.Errorf("source: got %q, want %q", f.source, ".")
	}
	if f.configPath != "" {
		t.Errorf("configPath: got %q, want empty", f.configPath)
	}
	if f.probeBenches {
		t.Error("probeBenches: got true, want false")
	}
	if f.targets != "" {
		t.Errorf("targets: got %q, want empty", f.targets)
	}
	if f.pin != "" {
		t.Errorf("pin: got %q, want empty", f.pin)
	}
	if f.exclude != "" {
		t.Errorf("exclude: got %q, want empty", f.exclude)
	}
	if f.jsonEvents {
		t.Error("jsonEvents: got true, want false")
	}
	if f.scratch != "" {
		t.Errorf("scratch: got %q, want empty", f.scratch)
	}
}

func TestParseFlags_Version(t *testing.T) {
	f, err := parseFlags([]string{"--version"})
	if err != nil {
		t.Fatalf("parseFlags(--version): unexpected error: %v", err)
	}
	if !f.printVersion {
		t.Error("printVersion: got false, want true")
	}
}

func TestParseFlags_AllSet(t *testing.T) {
	args := []string{
		"--config", "x.toml",
		"--probe-benches",
		"--targets", "linux,windows",
		"--bench", "lab-rig-1",
		"--exclude", "lab-rig-2,lab-rig-3",
		"--json-events",
		"--base", "release/v2",
		"--head", "refs/pull/42/head",
		"--source", "/repo",
		"--scratch", "/tmp/scratch",
	}
	f, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags(allSet): unexpected error: %v", err)
	}
	if f.configPath != "x.toml" {
		t.Errorf("configPath: got %q, want %q", f.configPath, "x.toml")
	}
	if !f.probeBenches {
		t.Error("probeBenches: got false, want true")
	}
	if f.targets != "linux,windows" {
		t.Errorf("targets: got %q, want %q", f.targets, "linux,windows")
	}
	if f.pin != "lab-rig-1" {
		t.Errorf("pin: got %q, want %q", f.pin, "lab-rig-1")
	}
	if f.exclude != "lab-rig-2,lab-rig-3" {
		t.Errorf("exclude: got %q, want %q", f.exclude, "lab-rig-2,lab-rig-3")
	}
	if !f.jsonEvents {
		t.Error("jsonEvents: got false, want true")
	}
	if f.base != "release/v2" {
		t.Errorf("base: got %q, want %q", f.base, "release/v2")
	}
	if f.head != "refs/pull/42/head" {
		t.Errorf("head: got %q, want %q", f.head, "refs/pull/42/head")
	}
	if f.source != "/repo" {
		t.Errorf("source: got %q, want %q", f.source, "/repo")
	}
	if f.scratch != "/tmp/scratch" {
		t.Errorf("scratch: got %q, want %q", f.scratch, "/tmp/scratch")
	}
}

func TestParseFlags_BadFlag(t *testing.T) {
	_, err := parseFlags([]string{"--unknown-flag"})
	if err == nil {
		t.Error("parseFlags(--unknown-flag): expected error, got nil")
	}
}

// TestRun_Version verifies that --version prints the version string and exits 0.
// run() writes to *os.File, so we use os.Pipe to capture output.
func TestRun_Version(t *testing.T) {
	// Override version for the test.
	original := version
	version = "v1.2.3-test"
	defer func() { version = original }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	// run writes to w; we read from r.
	code := run([]string{"--version"}, w, os.Stderr)
	w.Close()

	var buf strings.Builder
	tmp := make([]byte, 256)
	for {
		n, readErr := r.Read(tmp)
		buf.Write(tmp[:n])
		if readErr != nil {
			break
		}
	}
	r.Close()

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	got := strings.TrimSpace(buf.String())
	if got != "v1.2.3-test" {
		t.Errorf("output: got %q, want %q", got, "v1.2.3-test")
	}
}

// ── gsd-test run (issue #67) ───────────────────────────────────────────────────

// drain reads a pipe's read end to a string after the writer is closed.
func drain(r *os.File) string {
	var b strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		b.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return b.String()
}

// TestRun_RunCommand_NotifiesAndExitsTwoWhenNoBench drives `gsd-test run`
// against a config with no Bench for the target: it must print the handoff
// banner (so the agent knows not to re-run locally) and exit 2 (inconclusive)
// without spawning any local node. No Docker required — it fails at Bench
// resolution, before the container path.
func TestRun_RunCommand_NotifiesAndExitsTwoWhenNoBench(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"run", "--config", cfgPath, "--target", "linux"}, outW, errW)
	outW.Close()
	errW.Close()
	stdout, stderr := drain(outR), drain(errR)

	if code != exitInconclusive {
		t.Errorf("exit = %d, want %d (no Bench → inconclusive)", code, exitInconclusive)
	}
	if !strings.Contains(stderr, "handed off to Docker") || !strings.Contains(stderr, "run-id=") {
		t.Errorf("handoff banner missing from stderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "ℹ tests") {
		t.Errorf("no verdict should be rendered when the run could not start; stdout:\n%s", stdout)
	}
}

// ── submit subcommand ───────────────────────────────────────────────────────

// readPipe drains an *os.File read-end fully into a string.
func readPipe(r *os.File) string {
	var buf strings.Builder
	tmp := make([]byte, 256)
	for {
		n, readErr := r.Read(tmp)
		buf.Write(tmp[:n])
		if readErr != nil {
			break
		}
	}
	r.Close()
	return buf.String()
}

// writeSpecFile writes a run spec JSON to a temp file and returns its path.
func writeSpecFile(t *testing.T, body string) string {
	t.Helper()
	path := strings.TrimSuffix(t.TempDir(), "/") + "/spec.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writeSpecFile: %v", err)
	}
	return path
}

func TestRunSubmit_ValidSpec_AssignsRunID(t *testing.T) {
	path := writeSpecFile(t, `{"repo":"/work","target":"linux"}`)

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--spec-file", path}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\noutput=%q", err, out)
	}
	if got["runId"] == nil || got["runId"] == "" {
		t.Errorf("runId: got %v, want non-empty", got["runId"])
	}
	if got["target"] != "linux" {
		t.Errorf("target: got %v, want linux", got["target"])
	}
	cmd, _ := got["testCommand"].([]any)
	if len(cmd) != 2 || cmd[0] != "node" || cmd[1] != "--test" {
		t.Errorf("testCommand: got %v, want [node --test]", got["testCommand"])
	}
}

func TestRunSubmit_PreservesProvidedRunID(t *testing.T) {
	path := writeSpecFile(t, `{"runId":"fixed-abc-123","repo":"/work","target":"linux"}`)

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--spec-file", path}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if got["runId"] != "fixed-abc-123" {
		t.Errorf("runId: got %v, want fixed-abc-123", got["runId"])
	}
}

func TestRunSubmit_InvalidSpec_Exit2(t *testing.T) {
	path := writeSpecFile(t, `{"repo":"/work","target":"solaris"}`)

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	code := run([]string{"submit", "--spec-file", path}, wOut, wErr)
	wOut.Close()
	wErr.Close()
	out := readPipe(rOut)
	errOut := readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
	// ADR-0023 Option C: stdout must contain a verdict line even on inconclusive
	// early-return paths (run-outcome failure, not a CLI usage error).
	var foundVerdict bool
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["type"] == "verdict" {
			foundVerdict = true
			break
		}
	}
	if !foundVerdict {
		t.Errorf("stdout: got %q, want a verdict line (ADR-0023 Option C)", out)
	}
	if !strings.Contains(errOut, "target") {
		t.Errorf("stderr: got %q, want it to name the bad field 'target'", errOut)
	}
}
