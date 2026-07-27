package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/plan"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// runGit runs `git <args...>` in dir, failing the test on error. Test-only
// helper for TestResolveBranchSlug_EmptyHead_ResolvesFromSource.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

var errInjectedSweepFailure = errors.New("injected sweep failure")

// --- cellID ---

func TestCellID_WithNodeMajor(t *testing.T) {
	if got := cellID("linux", "24"); got != "linux-node24" {
		t.Errorf("cellID(linux, 24) = %q, want %q", got, "linux-node24")
	}
}

func TestCellID_WithoutNodeMajor(t *testing.T) {
	if got := cellID("windows", ""); got != "windows" {
		t.Errorf("cellID(windows, \"\") = %q, want %q", got, "windows")
	}
}

// --- buildContainerIdentity ---

// TestBuildContainerIdentity_NonEmptyRunIDAndBranchSlug guards against
// silently regressing to unnamed/unlabeled Pipeline containers: given
// non-empty inputs, the resulting identity must carry a non-empty RunID and
// BranchSlug (StartContainer treats an empty RunID as "do not name or label").
func TestBuildContainerIdentity_NonEmptyRunIDAndBranchSlug(t *testing.T) {
	run := plan.Run{OS: "linux", NodeMajor: "22"}
	ident := buildContainerIdentity("fix-foo", "run-12345678", run, 999)

	if ident.RunID == "" {
		t.Error("expected non-empty RunID")
	}
	if ident.BranchSlug == "" {
		t.Error("expected non-empty BranchSlug")
	}
	if ident.RunID != "run-12345678" {
		t.Errorf("RunID = %q, want %q", ident.RunID, "run-12345678")
	}
	if ident.BranchSlug != "fix-foo" {
		t.Errorf("BranchSlug = %q, want %q", ident.BranchSlug, "fix-foo")
	}
	if ident.Target != "linux" {
		t.Errorf("Target = %q, want %q", ident.Target, "linux")
	}
	if ident.DeadlineMs != 999 {
		t.Errorf("DeadlineMs = %d, want %d", ident.DeadlineMs, 999)
	}
}

// TestBuildContainerIdentity_CellMatchesNodeMajorRule verifies buildContainerIdentity
// delegates Cell derivation to cellID: "<os>-node<major>" when NodeMajor is
// set, "<os>" otherwise.
func TestBuildContainerIdentity_CellMatchesNodeMajorRule(t *testing.T) {
	withNode := buildContainerIdentity("branch", "run-1", plan.Run{OS: "linux", NodeMajor: "20"}, 1)
	if withNode.Cell != "linux-node20" {
		t.Errorf("Cell = %q, want %q", withNode.Cell, "linux-node20")
	}
	withoutNode := buildContainerIdentity("branch", "run-1", plan.Run{OS: "windows"}, 1)
	if withoutNode.Cell != "windows" {
		t.Errorf("Cell = %q, want %q", withoutNode.Cell, "windows")
	}
}

// --- resolveBranchSlug ---

// TestResolveBranchSlug_ExplicitHead verifies a supplied opts.Head is
// slugified directly (ADR-0029 slug rules: '/' -> '-').
func TestResolveBranchSlug_ExplicitHead(t *testing.T) {
	opts := Options{Head: "fix/foo"}
	if got := resolveBranchSlug(context.Background(), opts); got != "fix-foo" {
		t.Errorf("resolveBranchSlug(Head=fix/foo) = %q, want %q", got, "fix-foo")
	}
}

// TestResolveBranchSlug_HeadUnresolvable_FallsBackToSentinel verifies that
// when opts.Head is the literal "HEAD" and opts.Source cannot be resolved to
// a real branch (not a git repo here), resolution falls back to
// runspec.BranchSlugUnknown rather than failing the run.
func TestResolveBranchSlug_HeadUnresolvable_FallsBackToSentinel(t *testing.T) {
	// A plain temp dir is not a git repository, so refs.CurrentBranch errors.
	opts := Options{Head: "HEAD", Source: t.TempDir()}
	got := resolveBranchSlug(context.Background(), opts)
	if got != runspec.BranchSlugUnknown {
		t.Errorf("resolveBranchSlug(Head=HEAD, unresolvable Source) = %q, want %q", got, runspec.BranchSlugUnknown)
	}
}

// TestResolveBranchSlug_EmptyHead_ResolvesFromSource verifies that an empty
// opts.Head falls through to refs.CurrentBranch against opts.Source, and a
// real git branch there is slugified.
func TestResolveBranchSlug_EmptyHead_ResolvesFromSource(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	runGit(t, repo, "checkout", "-q", "-b", "feature/My_Cool-Branch")

	opts := Options{Head: "", Source: repo}
	got := resolveBranchSlug(context.Background(), opts)
	want := runspec.SlugifyBranch("feature/My_Cool-Branch")
	if got != want {
		t.Errorf("resolveBranchSlug(empty Head, real branch) = %q, want %q", got, want)
	}
}

// --- sweepStaleContainers ---

// TestSweepStaleContainers_OncePerDistinctBench verifies the sweep runs
// exactly once per distinct Bench (deduped by Name), never once per cell —
// concurrent sweeps of the same Bench would race each other.
func TestSweepStaleContainers_OncePerDistinctBench(t *testing.T) {
	var calls []string
	var gotBranch []string
	original := sweepBench
	sweepBench = func(ctx context.Context, b bench.Bench, nowMs int64, branchSlug string) ([]reaper.Container, error) {
		calls = append(calls, b.Name)
		gotBranch = append(gotBranch, branchSlug)
		return nil, nil
	}
	t.Cleanup(func() { sweepBench = original })

	// bench-a appears twice (once per OS key) to simulate the same physical
	// Bench being referenced from more than one place; it must still only be
	// swept once. bench-b appears once for a second OS.
	benchesByOS := map[string][]bench.Bench{
		"linux":   {{Name: "bench-a", OS: "linux"}, {Name: "bench-b", OS: "linux"}},
		"windows": {{Name: "bench-a", OS: "windows"}},
	}

	sweepStaleContainers(context.Background(), benchesByOS, "fix-foo", io.Discard)

	if len(calls) != 2 {
		t.Fatalf("expected 2 sweepBench calls (2 distinct bench names), got %d: %v", len(calls), calls)
	}
	seen := map[string]bool{}
	for _, name := range calls {
		if seen[name] {
			t.Fatalf("bench %q swept more than once: calls=%v", name, calls)
		}
		seen[name] = true
	}
	if !seen["bench-a"] || !seen["bench-b"] {
		t.Fatalf("expected both bench-a and bench-b swept, got %v", calls)
	}
	for _, branch := range gotBranch {
		if branch != "fix-foo" {
			t.Errorf("branchSlug passed to sweepBench = %q, want %q", branch, "fix-foo")
		}
	}
}

// TestSweepStaleContainers_ErrorDoesNotPanicOrStop verifies a sweep error on
// one bench is logged and does not prevent sweeping the remaining benches.
func TestSweepStaleContainers_ErrorDoesNotPanicOrStop(t *testing.T) {
	var calls []string
	original := sweepBench
	sweepBench = func(ctx context.Context, b bench.Bench, nowMs int64, branchSlug string) ([]reaper.Container, error) {
		calls = append(calls, b.Name)
		if b.Name == "bench-fail" {
			return nil, errInjectedSweepFailure
		}
		return nil, nil
	}
	t.Cleanup(func() { sweepBench = original })

	benchesByOS := map[string][]bench.Bench{
		"linux": {{Name: "bench-fail", OS: "linux"}, {Name: "bench-ok", OS: "linux"}},
	}

	var stderr bytes.Buffer
	sweepStaleContainers(context.Background(), benchesByOS, "fix-foo", &stderr)

	if len(calls) != 2 {
		t.Fatalf("expected both benches attempted despite one failing, got %d calls: %v", len(calls), calls)
	}
	if stderr.String() == "" {
		t.Error("expected the sweep error to be logged to stderr")
	}
}
