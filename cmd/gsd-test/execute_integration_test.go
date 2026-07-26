package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
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
			"-t", "ghcr.io/open-gsd/gsd-tester-linux:e2e", root)
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

// planStaleContainer starts a detached, overdue alpine container carrying the
// reaper's run-id + deadline labels (both required for reaper.List to surface
// it) plus whatever extra labels the caller supplies (e.g. sh.gsd-test.branch
// for branch-scoping tests). It registers a t.Cleanup that force-removes the
// container and returns the container ID.
func planStaleContainer(t *testing.T, runID string, deadline time.Time, extraLabels ...string) string {
	t.Helper()
	args := []string{"run", "-d", "--rm",
		"--label", reaper.LabelRunID + "=" + runID,
		"--label", reaper.LabelDeadline + "=" + strconv.FormatInt(deadline.UnixMilli(), 10),
	}
	for _, l := range extraLabels {
		args = append(args, "--label", l)
	}
	args = append(args, "alpine:3", "sleep", "300")
	idOut, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("plant container (run-id=%s): %v", runID, err)
	}
	id := strings.TrimSpace(string(idOut))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	return id
}

// containerRunning reports whether a container with the given id is still
// present on the local Docker daemon.
func containerRunning(t *testing.T, id string) bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "id="+id).Output()
	if err != nil {
		t.Fatalf("docker ps --filter id=%s: %v", id, err)
	}
	return strings.TrimSpace(string(out)) != ""
}

// TestE2E_SubmitExecute_SweepsStaleContainer proves the Tier-2 reaper's
// ADR-0029 §3 branch-scoped contract: `submit --execute` reaps only overdue
// containers whose sh.gsd-test.branch label matches the branch slug the
// current invocation actually works on. The spec here sets neither base nor
// prBranch, so spec.BranchSlug() (and therefore the invocation's sweep scope)
// resolves to the "unknown" sentinel — mirrored below via
// runspec.Spec{}.BranchSlug() rather than a duplicated string literal. An
// overdue container belonging to a different branch, and a pre-ADR-0029
// overdue container with no branch label at all, must both survive: per the
// ADR-0029 Consequences, scoped sweeps intentionally leave other-branch and
// legacy containers for their own invocations (or the `Sweep(_, _, _, "")`
// operator escape hatch) to clean up.
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
	// Deliberately no base/prBranch: adding either would switch the Engine onto
	// the PR-merged-worktree construction path (ADR-0021 §A), which requires a
	// real git repo — this test's worktree is a plain directory. BranchSlug()
	// falls back to "unknown" for exactly this shape of spec.
	if err := os.WriteFile(specPath, []byte(`{"repo":"`+worktree+`","target":"linux"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The branch slug this invocation actually works on, derived from the
	// production helper so the test tracks runspec's sentinel instead of
	// duplicating it.
	unknownSlug := runspec.Spec{}.BranchSlug()

	past := time.Now().Add(-time.Hour)

	// owned: same branch as this invocation, overdue -> must be reaped.
	ownedID := planStaleContainer(t, "stale-owned", past, reaper.LabelBranch+"="+unknownSlug)
	// otherBranch: a different branch, overdue -> must survive. This is the
	// ADR-0029 §3 branch-scoping feature: an invocation must not touch another
	// branch's leftover containers.
	otherBranchID := planStaleContainer(t, "stale-other", past, reaper.LabelBranch+"=fix-other")
	// legacy: pre-ADR-0029 container (no branch label at all), overdue -> must
	// survive. Documented in ADR-0029 Consequences: scoped sweeps no longer
	// auto-clean containers that predate branch labeling.
	legacyID := planStaleContainer(t, "stale-legacy", past)

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--execute", "--config", cfgPath, "--spec-file", specPath}, wOut, os.Stderr)
	wOut.Close()
	_ = readPipe(rOut)
	if code != 0 {
		t.Fatalf("submit --execute exit = %d, want 0", code)
	}

	if containerRunning(t, ownedID) {
		t.Errorf("owned container %s (branch=%s) survived; Tier-2 sweep should have reaped it", ownedID, unknownSlug)
	}
	if !containerRunning(t, otherBranchID) {
		t.Errorf("other-branch container %s (branch=fix-other) was reaped; ADR-0029 branch-scoping violated", otherBranchID)
	}
	if !containerRunning(t, legacyID) {
		t.Errorf("legacy (no branch label) container %s was reaped; pre-ADR-0029 containers must survive scoped sweeps", legacyID)
	}
}

// hermeticGit runs git in dir with global/system config neutralized.
func hermeticGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestE2E_SubmitExecute_PRMergedWorktree proves the {base, prBranch} form: the
// Engine builds a PR-merged worktree from the source repo and runs it, so the
// PR branch's added test executes (ADR-0021 §A).
func TestE2E_SubmitExecute_PRMergedWorktree(t *testing.T) {
	ensureTesterImage(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// main: a base test.
	hermeticGit(t, src, "init", "-b", "main")
	hermeticGit(t, src, "config", "user.email", "t@example.com")
	hermeticGit(t, src, "config", "user.name", "T")
	hermeticGit(t, src, "config", "commit.gpgsign", "false")
	write("base.test.mjs", "import { test } from 'node:test';\ntest('base ok', () => {});\n")
	hermeticGit(t, src, "add", ".")
	hermeticGit(t, src, "commit", "-m", "base")
	// feat: adds a PR test.
	hermeticGit(t, src, "checkout", "-b", "feat")
	write("pr.test.mjs", "import { test } from 'node:test';\ntest('pr feature works', () => {});\n")
	hermeticGit(t, src, "add", ".")
	hermeticGit(t, src, "commit", "-m", "pr")
	hermeticGit(t, src, "checkout", "main")

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets=[\"linux\"]\n\n[[benches]]\nname=\"local\"\nhost=\"local\"\nos=\"linux\"\n\n[versions]\nlinux=\"e2e\"\n\n[testing]\ncommand=\"node --test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, []byte(`{"repo":"`+src+`","target":"linux","base":"main","prBranch":"feat"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"submit", "--execute", "--config", cfgPath, "--spec-file", specPath}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
	}
	// The PR branch's test must have run in the merged worktree.
	if !strings.Contains(out, "pr feature works") {
		t.Errorf("PR test did not run in the merged worktree; per_test:\n%s", out)
	}
}

// TestE2E_RunAsync_WaitMatchesBlocking drives the full async path end-to-end:
//
//  1. `gsd-test run --async` spawns a detached worker (re-exec of the real binary)
//     and returns exit 0 immediately with a dispatched-notice containing the run-id.
//  2. `gsd-test status <id>` returns immediately (non-blocking) with state=running
//     or state=done — either is valid depending on timing.
//  3. `gsd-test wait <id>` blocks until the worker finishes the real container run
//     and renders the verdict (ℹ tests / ℹ pass).
//  4. A blocking `gsd-test run` produces the same passing verdict, proving async
//     wait and blocking run agree.
//
// The test builds the real `gsd-test` binary so that os.Executable() (used by the
// async worker spawn) resolves to a binary that routes `__run-worker`, not the test
// binary. XDG_STATE_HOME is set explicitly on every subprocess so the worker writes
// run-state to the test's temp dir, never the real ~/.local/state.
//
// This test is Docker-gated: ensureTesterImage skips when `docker version` fails.
func TestE2E_RunAsync_WaitMatchesBlocking(t *testing.T) {
	ensureTesterImage(t)

	// stateDir is the shared XDG_STATE_HOME for all subprocesses. We set it
	// explicitly on each exec.Command — NOT via t.Setenv — so the detached worker
	// process inherits it too.
	stateDir := t.TempDir()

	// Build the real gsd-test binary into its own temp dir so os.Executable()
	// inside the subprocess resolves to a binary that routes __run-worker.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "gsd-test")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/gsd-test")
	buildCmd.Dir = root
	if out, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build gsd-test binary: %v\n%s", buildErr, out)
	}

	// Set up a worktree temp dir with a single passing test and a config.toml.
	// cmd.Dir is set to worktree on every run/wait/status subprocess so that
	// repoRoot() (which falls back to cwd when not inside a git repo) picks up
	// the correct directory.
	worktree := t.TempDir()
	passTest := "import { test } from 'node:test';\nimport assert from 'node:assert';\n" +
		"test('passes', () => { assert.ok(true); });\n"
	if err := os.WriteFile(filepath.Join(worktree, "ok.test.mjs"), []byte(passTest), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(worktree, "config.toml")
	cfg := "[defaults]\ntargets = [\"linux\"]\n\n" +
		"[[benches]]\nname = \"local-linux\"\nhost = \"local\"\nos = \"linux\"\n\n" +
		"[versions]\nlinux = \"e2e\"\n\n" +
		"[testing]\ncommand = \"node --test\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	subEnv := append(os.Environ(), "XDG_STATE_HOME="+stateDir)

	// ── Step 1: dispatch async ────────────────────────────────────────────────

	asyncCmd := exec.Command(bin,
		"run", "--async",
		"--config", cfgPath,
		"--target", "linux",
		"--estimate-ms", "120000",
	)
	asyncCmd.Dir = worktree
	asyncCmd.Env = subEnv
	asyncOut, asyncErr := asyncCmd.CombinedOutput()
	if asyncErr != nil {
		t.Fatalf("run --async: exit error: %v\noutput:\n%s", asyncErr, asyncOut)
	}

	// Parse the run-id from the dispatched-notice line.
	// Format: "dispatched run-id=<id>  (use ...)"
	var runID string
	for _, field := range strings.Fields(string(asyncOut)) {
		if strings.HasPrefix(field, "run-id=") {
			runID = strings.TrimPrefix(field, "run-id=")
			// Strip any trailing punctuation (e.g. comma).
			runID = strings.TrimRight(runID, ",;.")
			break
		}
	}
	if runID == "" {
		t.Fatalf("could not parse run-id from async output:\n%s", asyncOut)
	}
	t.Logf("async run-id: %s", runID)

	// ── Step 2: status (non-blocking) ────────────────────────────────────────

	statusCmd := exec.Command(bin, "status", runID)
	statusCmd.Env = subEnv
	statusOut, statusErr := statusCmd.CombinedOutput()
	if statusErr != nil {
		t.Fatalf("status %s: exit error: %v\noutput:\n%s", runID, statusErr, statusOut)
	}
	statusStr := string(statusOut)
	if !strings.Contains(statusStr, "state=running") && !strings.Contains(statusStr, "state=done") {
		t.Errorf("status output must contain state=running or state=done; got:\n%s", statusStr)
	}

	// ── Step 3: wait (blocking) ───────────────────────────────────────────────

	const waitTimeout = 180 * time.Second
	waitCtx, waitCancel := context.WithTimeout(context.Background(), waitTimeout)
	defer waitCancel()

	waitCmd := exec.CommandContext(waitCtx, bin, "wait", runID)
	waitCmd.Env = subEnv
	waitOut, waitErr := waitCmd.CombinedOutput()
	if waitCtx.Err() == context.DeadlineExceeded {
		t.Fatalf("wait %s: timed out after %s — potential silent-hang regression\noutput so far:\n%s",
			runID, waitTimeout, waitOut)
	}
	if waitErr != nil {
		t.Fatalf("wait %s: exit error: %v\noutput:\n%s", runID, waitErr, waitOut)
	}
	waitStr := string(waitOut)
	if !strings.Contains(waitStr, "ℹ tests") {
		t.Errorf("wait output missing 'ℹ tests'; got:\n%s", waitStr)
	}
	if !strings.Contains(waitStr, "ℹ pass") {
		t.Errorf("wait output missing 'ℹ pass'; got:\n%s", waitStr)
	}
	if !strings.Contains(waitStr, "ok.test.mjs") {
		t.Errorf("wait output missing passing test file 'ok.test.mjs'; got:\n%s", waitStr)
	}

	// ── Step 4: fidelity check — blocking run must agree ─────────────────────

	blockCtx, blockCancel := context.WithTimeout(context.Background(), waitTimeout)
	defer blockCancel()

	blockCmd := exec.CommandContext(blockCtx, bin,
		"run",
		"--config", cfgPath,
		"--target", "linux",
		"--estimate-ms", "120000",
	)
	blockCmd.Dir = worktree
	blockCmd.Env = subEnv
	blockOut, blockErr := blockCmd.CombinedOutput()
	if blockCtx.Err() == context.DeadlineExceeded {
		t.Fatalf("blocking run: timed out after %s\noutput:\n%s", waitTimeout, blockOut)
	}
	if blockErr != nil {
		t.Fatalf("blocking run: exit error: %v\noutput:\n%s", blockErr, blockOut)
	}
	blockStr := string(blockOut)
	if !strings.Contains(blockStr, "ℹ pass") {
		t.Errorf("blocking run output missing 'ℹ pass'; got:\n%s", blockStr)
	}
	// Both async wait and blocking run must agree: both pass.
	t.Logf("async wait verdict: %s", waitStr)
	t.Logf("blocking run verdict: %s", blockStr)
}
