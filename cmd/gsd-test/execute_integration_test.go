package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
)

var execImageOnce sync.Once

// ensureTesterImage builds the Linux Tester Image under the ghcr ref that
// executeSpec resolves, so images.EnsurePresent finds it present (no pull, no
// fallback build) when the CLI runs. Uses absolute paths — independent of the
// test's working directory.
func ensureTesterImage(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	execImageOnce.Do(func() {
		cmd := exec.Command("docker", "build",
			"-f", filepath.Join(root, "dockerfiles", "linux.Dockerfile"),
			"--build-arg", "IMAGE_VERSION=e2e",
			"-t", "ghcr.io/open-gsd/gsd-tester-linux", root)
		if out, berr := cmd.CombinedOutput(); berr != nil {
			t.Fatalf("build tester image: %v\n%s", berr, out)
		}
	})
}

// TestE2E_SubmitExecute_PassingRun drives the whole `gsd-test submit --execute`
// path: read spec -> config.Load -> Bench pick -> EnsurePresent -> copy-in
// run-and-die under the watchdog -> per-OS Report (#60, ADR-0021).
func TestE2E_SubmitExecute_PassingRun(t *testing.T) {
	ensureTesterImage(t)

	// Isolate the persistent telemetry log so the run's append (median/leaderboard
	// accumulation) writes under a temp dir, not the real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir()

	worktree := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	passTest := "import { test } from 'node:test';\nimport assert from 'node:assert';\n" +
		"test('passes', () => { assert.ok(true); });\n"
	if err := os.WriteFile(filepath.Join(worktree, "ok.test.mjs"), []byte(passTest), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := "[defaults]\ntargets = [\"linux\"]\n\n" +
		"[[benches]]\nname = \"local-linux\"\nhost = \"local\"\nos = \"linux\"\n\n" +
		"[versions]\nlinux = \"e2e\"\n\n" +
		"[testing]\ncommand = \"node --test\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	specPath := filepath.Join(dir, "spec.json")
	spec := `{"repo":"` + worktree + `","target":"linux","budget":{"estimateMs":120000}}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--execute", "--config", cfgPath, "--spec-file", specPath}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, `"outcome": "passed"`) {
		t.Errorf("report did not show passed outcome:\n%s", out)
	}
}

// TestE2E_SubmitExecute_SweepsStaleContainer proves the Tier-2 reaper runs on
// contact: a labelled container whose deadline has already passed is killed
// when `submit --execute` starts (ADR-0021 Decision 2).
func TestE2E_SubmitExecute_SweepsStaleContainer(t *testing.T) {
	ensureTesterImage(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()

	worktree := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "ok.test.mjs"),
		[]byte("import { test } from 'node:test';\ntest('ok', () => {});\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := "[defaults]\ntargets = [\"linux\"]\n\n" +
		"[[benches]]\nname = \"local-linux\"\nhost = \"local\"\nos = \"linux\"\n\n" +
		"[versions]\nlinux = \"e2e\"\n\n[testing]\ncommand = \"node --test\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, []byte(`{"repo":"`+worktree+`","target":"linux"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plant a stale run container with a deadline in the past.
	past := time.Now().Add(-time.Hour).UnixMilli()
	idOut, err := exec.Command("docker", "run", "-d", "--rm",
		"--label", reaper.LabelRunID+"=stale-sweep-it",
		"--label", reaper.LabelDeadline+"="+strconv.FormatInt(past, 10),
		"alpine:3", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("plant stale container: %v", err)
	}
	staleID := strings.TrimSpace(string(idOut))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", staleID).Run() })

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--execute", "--config", cfgPath, "--spec-file", specPath}, wOut, os.Stderr)
	wOut.Close()
	_ = readPipe(rOut)
	if code != 0 {
		t.Fatalf("submit --execute exit = %d, want 0", code)
	}

	// The stale container must have been swept.
	psOut, _ := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "id="+staleID).Output()
	if strings.TrimSpace(string(psOut)) != "" {
		t.Errorf("stale container %s survived; Tier-2 sweep did not run", staleID)
	}
}
