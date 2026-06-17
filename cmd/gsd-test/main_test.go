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

// ── commaSplit ────────────────────────────────────────────────────────────────

func TestCommaSplit_Empty(t *testing.T) {
	got := commaSplit("")
	if got != nil {
		t.Errorf("commaSplit(%q): got %v, want nil", "", got)
	}
}

func TestCommaSplit_Single(t *testing.T) {
	got := commaSplit("linux")
	if len(got) != 1 || got[0] != "linux" {
		t.Errorf("commaSplit(%q): got %v, want [linux]", "linux", got)
	}
}

func TestCommaSplit_Multiple(t *testing.T) {
	got := commaSplit("linux,windows,macos")
	if len(got) != 3 {
		t.Errorf("commaSplit(%q): got len=%d, want 3: %v", "linux,windows,macos", len(got), got)
		return
	}
	want := []string{"linux", "windows", "macos"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("commaSplit index %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestCommaSplit_TrimsWhitespace(t *testing.T) {
	got := commaSplit(" linux , windows ")
	if len(got) != 2 {
		t.Errorf("commaSplit(whitespace): got len=%d, want 2: %v", len(got), got)
		return
	}
	if got[0] != "linux" {
		t.Errorf("commaSplit[0]: got %q, want %q", got[0], "linux")
	}
	if got[1] != "windows" {
		t.Errorf("commaSplit[1]: got %q, want %q", got[1], "windows")
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
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout: got %q, want empty (no run accepted)", out)
	}
	if !strings.Contains(errOut, "target") {
		t.Errorf("stderr: got %q, want it to name the bad field 'target'", errOut)
	}
}
