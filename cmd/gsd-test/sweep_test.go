package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/tartreaper"
)

// ── flag parsing / dispatch (no Docker required) ────────────────────────────

// TestRunSweep_AllAndBranchMutuallyExclusive verifies the mutual-exclusion
// guard fires before config.Load or any Bench I/O, so no config is needed to
// exercise it.
func TestRunSweep_AllAndBranchMutuallyExclusive(t *testing.T) {
	rErr, wErr, _ := os.Pipe()
	code := run([]string{"sweep", "--all", "--branch", "fix-foo"}, os.Stdout, wErr)
	wErr.Close()
	errOut := readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
	if !strings.Contains(errOut, "--all and --branch are mutually exclusive") {
		t.Errorf("stderr: got %q, want the mutual-exclusion message", errOut)
	}
}

// TestRunSweep_BadFlag verifies an unrecognized flag is a parse error,
// returning exitInconclusive without reaching config.Load.
func TestRunSweep_BadFlag(t *testing.T) {
	rErr, wErr, _ := os.Pipe()
	code := run([]string{"sweep", "--not-a-real-flag"}, os.Stdout, wErr)
	wErr.Close()
	_ = readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
}

// TestRunSweep_UnknownBench verifies --bench naming a Bench absent from the
// config errors clearly and lists the available names, without ever reaching
// reaper.Sweep (no Docker required).
func TestRunSweep_UnknownBench(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	cfg := "[[benches]]\nname = \"lab-rig-1\"\nhost = \"local\"\nos = \"linux\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	rErr, wErr, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--bench", "does-not-exist"}, os.Stdout, wErr)
	wErr.Close()
	errOut := readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
	if !strings.Contains(errOut, `bench "does-not-exist" not found`) || !strings.Contains(errOut, "lab-rig-1") {
		t.Errorf("stderr: got %q, want a not-found message listing lab-rig-1", errOut)
	}
}

// TestRunSweep_NoBenchesConfigured verifies an empty registry errors clearly
// rather than silently doing nothing.
func TestRunSweep_NoBenchesConfigured(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[defaults]\ntargets = [\"linux\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rErr, wErr, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath}, os.Stdout, wErr)
	wErr.Close()
	errOut := readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
	if !strings.Contains(errOut, "no Benches configured") {
		t.Errorf("stderr: got %q, want a no-Benches message", errOut)
	}
}

// ── dispatch-by-runtime (Tart path, fakes only — no real Tart/SSH hardware) ─

// stubTartSweep swaps the package-level tartSweep var. Restored via
// t.Cleanup, mirroring the stubbable-var pattern used throughout this
// codebase (there is no real Tart/SSH hardware available to test against).
func stubTartSweep(t *testing.T, fn func(ctx context.Context, b bench.Bench, nowMs int64, branchSlug string) ([]tartreaper.VM, error)) {
	t.Helper()
	orig := tartSweep
	tartSweep = fn
	t.Cleanup(func() { tartSweep = orig })
}

// TestRunSweep_TartBench_DispatchesToTartSweep verifies a Bench configured
// with runtime="tart" is routed through tartSweep (internal/tartreaper.Sweep
// in production), not the Docker/reaper.Sweep path, and reaped VMs are
// reported in the same "bench=...: reaped id=... name=... branch=..." shape
// the Docker path uses.
func TestRunSweep_TartBench_DispatchesToTartSweep(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	cfg := "[[benches]]\nname = \"mac-tart-1\"\nhost = \"tart-bench.local\"\nos = \"macos\"\nruntime = \"tart\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotBench bench.Bench
	var gotBranch string
	stubTartSweep(t, func(_ context.Context, b bench.Bench, _ int64, branchSlug string) ([]tartreaper.VM, error) {
		gotBench = b
		gotBranch = branchSlug
		return []tartreaper.VM{{Name: "gsd-tart-macos-run1", BranchSlug: branchSlug, HasState: true}}, nil
	})

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--all"}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("sweep exit = %d, want 0; output:\n%s", code, out)
	}
	if gotBench.Name != "mac-tart-1" {
		t.Errorf("tartSweep called with bench %q, want mac-tart-1", gotBench.Name)
	}
	if gotBranch != "" {
		t.Errorf("expected --all to pass branchSlug=\"\", got %q", gotBranch)
	}
	if !strings.Contains(out, "reaped id=gsd-tart-macos-run1 name=gsd-tart-macos-run1") {
		t.Errorf("stdout did not report the reaped VM in the expected shape; output:\n%s", out)
	}
}

// TestRunSweep_TartBench_NothingToCleanUp verifies the "nothing to clean up"
// message is printed for a Tart Bench exactly like the Docker path, when
// tartSweep returns no reaped VMs and no error.
func TestRunSweep_TartBench_NothingToCleanUp(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	cfg := "[[benches]]\nname = \"mac-tart-1\"\nhost = \"tart-bench.local\"\nos = \"macos\"\nruntime = \"tart\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	stubTartSweep(t, func(context.Context, bench.Bench, int64, string) ([]tartreaper.VM, error) {
		return nil, nil
	})

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--all"}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("sweep exit = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "bench=mac-tart-1: nothing to clean up") {
		t.Errorf("expected a nothing-to-clean-up message; output:\n%s", out)
	}
}

// TestRunSweep_TartBench_SweepError_ExitInconclusive verifies a tartSweep
// error is reported to stderr and the command exits exitInconclusive, the
// same behavior reaper.Sweep failures already have on the Docker path.
func TestRunSweep_TartBench_SweepError_ExitInconclusive(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	cfg := "[[benches]]\nname = \"mac-tart-1\"\nhost = \"tart-bench.local\"\nos = \"macos\"\nruntime = \"tart\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	stubTartSweep(t, func(context.Context, bench.Bench, int64, string) ([]tartreaper.VM, error) {
		return nil, errInjectedTartSweepFailure
	})

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--all"}, wOut, wErr)
	wOut.Close()
	wErr.Close()
	_ = readPipe(rOut)
	errOut := readPipe(rErr)

	if code != exitInconclusive {
		t.Errorf("exit code: got %d, want %d", code, exitInconclusive)
	}
	if !strings.Contains(errOut, "sweep error") {
		t.Errorf("stderr: got %q, want a sweep-error message", errOut)
	}
}

// TestRunSweep_MultipleTartBenches_OnlyNamedBenchSwept verifies --bench still
// scopes the dispatch loop to a single Tart Bench (not every configured
// Bench), the same targeting behavior TestRunSweep_UnknownBench already
// relies on for the Docker path.
func TestRunSweep_MultipleTartBenches_OnlyNamedBenchSwept(t *testing.T) {
	cfgPath := strings.TrimSuffix(t.TempDir(), "/") + "/config.toml"
	cfg := "[[benches]]\nname = \"mac-tart-1\"\nhost = \"tart-bench-1.local\"\nos = \"macos\"\nruntime = \"tart\"\n" +
		"[[benches]]\nname = \"mac-tart-2\"\nhost = \"tart-bench-2.local\"\nos = \"macos\"\nruntime = \"tart\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var calledBenches []string
	stubTartSweep(t, func(_ context.Context, b bench.Bench, _ int64, _ string) ([]tartreaper.VM, error) {
		calledBenches = append(calledBenches, b.Name)
		return nil, nil
	})

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--bench", "mac-tart-2", "--all"}, wOut, os.Stderr)
	wOut.Close()
	_ = readPipe(rOut)

	if code != 0 {
		t.Fatalf("sweep exit = %d, want 0", code)
	}
	if len(calledBenches) != 1 || calledBenches[0] != "mac-tart-2" {
		t.Errorf("tartSweep calls = %v, want exactly [mac-tart-2]", calledBenches)
	}
}

var errInjectedTartSweepFailure = &tartSweepTestError{}

// tartSweepTestError is a minimal error type for
// TestRunSweep_TartBench_SweepError_ExitInconclusive — a plain
// errors.New would work identically, but a named type keeps the test's
// intent ("an injected failure, not a real one") obvious at the call site.
type tartSweepTestError struct{}

func (e *tartSweepTestError) Error() string { return "injected tart sweep failure" }

// ── resolveSweepBranchSlug (pure git resolution, no Docker) ────────────────

// TestResolveSweepBranchSlug_ChecksOutBranch verifies the default --branch
// resolution: the currently checked-out branch of the repo at the working
// directory, slugified via the same runspec.SlugifyBranch rules the
// dispatch/Pipeline engines use.
func TestResolveSweepBranchSlug_ChecksOutBranch(t *testing.T) {
	dir := t.TempDir()
	hermeticGit(t, dir, "init", "-b", "fix/2329-sweep-cmd")
	hermeticGit(t, dir, "config", "user.email", "t@example.com")
	hermeticGit(t, dir, "config", "user.name", "T")
	hermeticGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(dir+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hermeticGit(t, dir, "add", ".")
	hermeticGit(t, dir, "commit", "-m", "init")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	got := resolveSweepBranchSlug(context.Background())
	want := runspec.SlugifyBranch("fix/2329-sweep-cmd")
	if got != want {
		t.Errorf("resolveSweepBranchSlug = %q, want %q", got, want)
	}
}

// TestResolveSweepBranchSlug_FallsBackWhenNotAGitRepo verifies the
// never-fails contract: outside any git repo, resolution falls back to
// runspec.BranchSlugUnknown rather than propagating an error.
func TestResolveSweepBranchSlug_FallsBackWhenNotAGitRepo(t *testing.T) {
	dir := t.TempDir() // no `git init` — not a repo
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	got := resolveSweepBranchSlug(context.Background())
	if got != runspec.BranchSlugUnknown {
		t.Errorf("resolveSweepBranchSlug = %q, want %q", got, runspec.BranchSlugUnknown)
	}
}

// ── end-to-end sweep against real Docker (mirrors execute_integration_test.go) ──

// dockerAvailable skips the calling test when no local Docker daemon is
// reachable, matching ensureTesterImage's gating without requiring the
// Tester Image build (sweep never touches images).
func dockerAvailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

// TestE2E_Sweep_DefaultScopeLeavesOtherBranchAlone proves `gsd-test sweep`
// with no flags defaults to the SAME branch-scoped safety as the automatic
// on-next-contact sweep: it reaps only the current invocation's branch,
// leaving an overdue container from an unrelated branch untouched.
func TestE2E_Sweep_DefaultScopeLeavesOtherBranchAlone(t *testing.T) {
	dockerAvailable(t)

	dir := t.TempDir()
	cfgPath := dir + "/config.toml"
	cfg := "[[benches]]\nname = \"local-linux\"\nhost = \"local\"\nos = \"linux\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real git repo so resolveSweepBranchSlug resolves a real branch.
	hermeticGit(t, dir, "init", "-b", "fix-mine")
	hermeticGit(t, dir, "config", "user.email", "t@example.com")
	hermeticGit(t, dir, "config", "user.name", "T")
	hermeticGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(dir+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hermeticGit(t, dir, "add", ".")
	hermeticGit(t, dir, "commit", "-m", "init")

	mySlug := runspec.SlugifyBranch("fix-mine")
	past := time.Now().Add(-time.Hour)

	mineID := plantSweepContainer(t, "sweep-mine", past, "sh.gsd-test.branch="+mySlug)
	otherID := plantSweepContainer(t, "sweep-other", past, "sh.gsd-test.branch=fix-other")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("sweep exit = %d, want 0; output:\n%s", code, out)
	}
	if sweepContainerRunning(t, mineID) {
		t.Errorf("own-branch (%s) container %s survived a default-scope sweep", mySlug, mineID)
	}
	if !sweepContainerRunning(t, otherID) {
		t.Errorf("other-branch container %s was reaped by a default-scope sweep; branch scoping violated", otherID)
	}
}

// TestE2E_Sweep_AllReapsRegardlessOfBranch proves `gsd-test sweep --all`
// widens to the unscoped operator escape hatch: an overdue container from an
// unrelated branch IS reaped.
func TestE2E_Sweep_AllReapsRegardlessOfBranch(t *testing.T) {
	dockerAvailable(t)

	dir := t.TempDir()
	cfgPath := dir + "/config.toml"
	cfg := "[[benches]]\nname = \"local-linux\"\nhost = \"local\"\nos = \"linux\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour)
	otherID := plantSweepContainer(t, "sweep-all-other", past, "sh.gsd-test.branch=fix-other")

	rOut, wOut, _ := os.Pipe()
	code := run([]string{"sweep", "--config", cfgPath, "--all"}, wOut, os.Stderr)
	wOut.Close()
	out := readPipe(rOut)

	if code != 0 {
		t.Fatalf("sweep --all exit = %d, want 0; output:\n%s", code, out)
	}
	if sweepContainerRunning(t, otherID) {
		t.Errorf("other-branch container %s survived `sweep --all`; unscoped escape hatch should have reaped it", otherID)
	}
	if !strings.Contains(out, "reaped id="+otherID) {
		t.Errorf("stdout did not report the reaped container id; output:\n%s", out)
	}
}

// plantSweepContainer starts a detached, overdue alpine container carrying
// the reaper's run-id + deadline labels plus any extra labels, mirroring
// planStaleContainer (execute_integration_test.go / main_test.go) — kept
// separate here so this file has no ordering dependency on those.
func plantSweepContainer(t *testing.T, runID string, deadline time.Time, extraLabels ...string) string {
	t.Helper()
	args := []string{"run", "-d", "--rm",
		"--label", "sh.gsd-test.run-id=" + runID,
		"--label", "sh.gsd-test.deadline=" + strconv.FormatInt(deadline.UnixMilli(), 10),
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

// sweepContainerRunning reports whether a container with the given id is
// still present on the local Docker daemon.
func sweepContainerRunning(t *testing.T, id string) bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "id="+id).Output()
	if err != nil {
		t.Fatalf("docker ps --filter id=%s: %v", id, err)
	}
	return strings.TrimSpace(string(out)) != ""
}
