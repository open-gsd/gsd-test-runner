package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// initRepo creates an empty git repo in t.TempDir() with minimal user
// config so commits work in CI environments that have no global git config.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")
	return dir
}

// commitFile writes file content, stages it, and commits it in repo.
func commitFile(t *testing.T, repo, file, content, msg string) {
	t.Helper()
	full := filepath.Join(repo, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("commitFile: MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("commitFile: WriteFile: %v", err)
	}
	gitRun(t, repo, "add", file)
	gitRun(t, repo, "commit", "-m", msg)
}

// branch creates a new branch in repo from the current HEAD.
func branch(t *testing.T, repo, name string) {
	t.Helper()
	gitRun(t, repo, "checkout", "-b", name)
}

// checkout checks out a ref in repo.
func checkout(t *testing.T, repo, ref string) {
	t.Helper()
	gitRun(t, repo, "checkout", ref)
}

// gitRun runs a git command in dir, fataling on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	exitCode, stderr, err := runGit(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v (stderr: %s)", args, err, stderr)
	}
	if exitCode != 0 {
		t.Fatalf("git %v exited %d: %s", args, exitCode, stderr)
	}
}

// assertConstructError asserts err is a *ConstructError at the given stage and
// returns it for further inspection.
func assertConstructError(t *testing.T, err error, stage Stage) *ConstructError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *ConstructError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConstructError, got %T: %v", err, err)
	}
	if ce.Stage != stage {
		t.Fatalf("expected stage %s, got %s", stage, ce.Stage)
	}
	return ce
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// HappyPath_CleanMerge: base has commit A; PR branch has A→B. Merging
// produces a merge commit with both files present. Close is idempotent.
func TestHappyPath_CleanMerge(t *testing.T) {
	repo := initRepo(t)

	// Commit A on main.
	commitFile(t, repo, "a.txt", "from A", "add a.txt")

	// Create PR branch from main, add commit B.
	branch(t, repo, "feat")
	commitFile(t, repo, "b.txt", "from B", "add b.txt")

	// Switch back to main so BaseRef is main.
	checkout(t, repo, "main")

	wt, err := Construct(context.Background(), Options{
		SourceRepo: repo,
		BaseRef:    "main",
		PRRef:      "feat",
	})
	if err != nil {
		t.Fatalf("Construct: %v", err)
	}

	// Worktree path must exist.
	if _, statErr := os.Stat(wt.Path()); statErr != nil {
		t.Fatalf("worktree path does not exist: %v", statErr)
	}
	// Both files from A and B must be present.
	if _, statErr := os.Stat(filepath.Join(wt.Path(), "a.txt")); statErr != nil {
		t.Errorf("a.txt missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(wt.Path(), "b.txt")); statErr != nil {
		t.Errorf("b.txt missing: %v", statErr)
	}

	// First Close removes directory.
	if err := wt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if _, statErr := os.Stat(wt.Path()); !os.IsNotExist(statErr) {
		t.Errorf("expected worktree path to be removed; stat returned %v", statErr)
	}

	// Second Close is idempotent.
	if err := wt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// HappyPath_FastForwardMerge: base hasn't diverged from PR branch, so
// git will fast-forward. Distinct from CleanMerge because there is no
// merge commit; we verify the result state is still correct.
func TestHappyPath_FastForwardMerge(t *testing.T) {
	repo := initRepo(t)

	// Commit A on main.
	commitFile(t, repo, "a.txt", "from A", "add a.txt")

	// PR branch adds B on top of A; main has NOT moved forward.
	branch(t, repo, "feat")
	commitFile(t, repo, "b.txt", "from B", "add b.txt")

	// Keep HEAD on feat — base is main (which is still at A).
	// Construct: BaseRef=main, PRRef=feat → fast-forward merge.
	wt, err := Construct(context.Background(), Options{
		SourceRepo: repo,
		BaseRef:    "main",
		PRRef:      "feat",
	})
	if err != nil {
		t.Fatalf("Construct: %v", err)
	}
	defer wt.Close()

	if _, statErr := os.Stat(filepath.Join(wt.Path(), "b.txt")); statErr != nil {
		t.Errorf("b.txt missing after fast-forward: %v", statErr)
	}
}

// InvalidOptions_EmptySourceRepo: SourceRepo == "" should fail at
// StageValidate with InvalidOptionsError. Nothing on disk should have
// been created.
func TestInvalidOptions_EmptySourceRepo(t *testing.T) {
	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: "",
			BaseRef:    "main",
			PRRef:      "feat",
		})
		return err
	}(), StageValidate)

	var ioe *InvalidOptionsError
	if !errors.As(ce.Cause, &ioe) {
		t.Fatalf("expected *InvalidOptionsError, got %T", ce.Cause)
	}
	if ioe.Field != "SourceRepo" {
		t.Errorf("expected Field=SourceRepo, got %q", ioe.Field)
	}
}

// InvalidOptions_NotAGitRepo: SourceRepo is a real directory but has no
// .git. Should fail at StageValidate.
func TestInvalidOptions_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // real dir, no .git

	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: dir,
			BaseRef:    "main",
			PRRef:      "feat",
		})
		return err
	}(), StageValidate)

	var ioe *InvalidOptionsError
	if !errors.As(ce.Cause, &ioe) {
		t.Fatalf("expected *InvalidOptionsError, got %T", ce.Cause)
	}
}

// Clone_BadSourcePath: SourceRepo does not exist. Per the brief, we
// catch this at StageValidate via os.Stat before invoking git.
func TestClone_BadSourcePath(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist")

	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: nonExistent,
			BaseRef:    "main",
			PRRef:      "feat",
		})
		return err
	}(), StageValidate)

	var ioe *InvalidOptionsError
	if !errors.As(ce.Cause, &ioe) {
		t.Fatalf("expected *InvalidOptionsError, got %T", ce.Cause)
	}
}

// Checkout_UnknownBaseRef: valid source repo, but BaseRef does not exist.
// Should fail at StageCheckout with CheckoutError; scratch dir cleaned up.
func TestCheckout_UnknownBaseRef(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "a", "init")

	scratchParent := t.TempDir()

	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: repo,
			BaseRef:    "does-not-exist",
			PRRef:      "main",
			ScratchDir: scratchParent,
		})
		return err
	}(), StageCheckout)

	var coErr *CheckoutError
	if !errors.As(ce.Cause, &coErr) {
		t.Fatalf("expected *CheckoutError, got %T", ce.Cause)
	}
	if coErr.Ref != "does-not-exist" {
		t.Errorf("expected Ref=does-not-exist, got %q", coErr.Ref)
	}
	// Scratch dir must have been cleaned up.
	assertScratchCleaned(t, scratchParent)
}

// Merge_UnknownPRRef: valid repo, BaseRef OK, PRRef does not exist.
// git merge fails with a non-conflict error → MergeError (not MergeConflictError).
func TestMerge_UnknownPRRef(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "a", "init")

	scratchParent := t.TempDir()

	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: repo,
			BaseRef:    "main",
			PRRef:      "no-such-branch",
			ScratchDir: scratchParent,
		})
		return err
	}(), StageMerge)

	var mergeErr *MergeError
	if !errors.As(ce.Cause, &mergeErr) {
		// Must NOT be a MergeConflictError.
		var conflictErr *MergeConflictError
		if errors.As(ce.Cause, &conflictErr) {
			t.Fatalf("got unexpected MergeConflictError instead of MergeError")
		}
		t.Fatalf("expected *MergeError, got %T", ce.Cause)
	}
	assertScratchCleaned(t, scratchParent)
}

// Merge_Conflict: base has foo.txt="base", PR branch has foo.txt="pr".
// git merge produces a conflict; MergeConflictError.Files contains "foo.txt".
func TestMerge_Conflict(t *testing.T) {
	repo := initRepo(t)

	// Common ancestor: both branches share this commit.
	commitFile(t, repo, "foo.txt", "ancestor", "ancestor commit")

	// PR branch changes foo.txt.
	branch(t, repo, "feat")
	commitFile(t, repo, "foo.txt", "pr version", "pr changes foo.txt")

	// Back to main, also change foo.txt differently → conflict.
	checkout(t, repo, "main")
	commitFile(t, repo, "foo.txt", "base version", "base changes foo.txt")

	scratchParent := t.TempDir()

	ce := assertConstructError(t, func() error {
		_, err := Construct(context.Background(), Options{
			SourceRepo: repo,
			BaseRef:    "main",
			PRRef:      "feat",
			ScratchDir: scratchParent,
		})
		return err
	}(), StageMerge)

	var conflictErr *MergeConflictError
	if !errors.As(ce.Cause, &conflictErr) {
		t.Fatalf("expected *MergeConflictError, got %T: %v", ce.Cause, ce.Cause)
	}
	if len(conflictErr.Files) == 0 {
		t.Error("MergeConflictError.Files is empty")
	}
	found := false
	for _, f := range conflictErr.Files {
		if f == "foo.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("foo.txt not in conflict files: %v", conflictErr.Files)
	}
	assertScratchCleaned(t, scratchParent)
}

// Close_Idempotent: happy-path construction, then Close twice; both
// return nil. Distinct from HappyPath_CleanMerge (which only confirms
// idempotency incidentally); this test is the canonical idempotency check.
func TestClose_Idempotent(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "a", "init")
	branch(t, repo, "feat")
	commitFile(t, repo, "b.txt", "b", "add b")
	checkout(t, repo, "main")

	wt, err := Construct(context.Background(), Options{
		SourceRepo: repo,
		BaseRef:    "main",
		PRRef:      "feat",
	})
	if err != nil {
		t.Fatalf("Construct: %v", err)
	}

	if err := wt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Construct_ContextCanceled: pass a context that is already canceled.
// An error of some kind must be returned; no orphan scratch dir should
// remain. We do NOT assert on the exact error shape because cancellation
// can land at any stage.
func TestConstruct_ContextCanceled(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "a", "init")

	scratchParent := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := Construct(ctx, Options{
		SourceRepo: repo,
		BaseRef:    "main",
		PRRef:      "main",
		ScratchDir: scratchParent,
	})
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
	// No orphan scratch dirs should remain.
	assertScratchCleaned(t, scratchParent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// assertScratchCleaned verifies that no gsd-worktree-* subdirectory
// remains inside scratchParent. Fails the test if any are found.
func assertScratchCleaned(t *testing.T, scratchParent string) {
	t.Helper()
	entries, err := os.ReadDir(scratchParent)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", scratchParent, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("orphan scratch dir found: %s", filepath.Join(scratchParent, e.Name()))
		}
	}
}
