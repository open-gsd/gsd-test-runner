package refs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// initRepo creates a git repo in t.TempDir() with a few commits and tags
// so that branch, HEAD, full SHA, short SHA, lightweight tag, and annotated
// tag resolution can all be exercised.
//
// Returns the repo path and a map of named refs to their full commit SHAs
// as understood by the repo at creation time.
func initRepo(t *testing.T) (repoPath string, shas map[string]string) {
	t.Helper()
	dir := t.TempDir()

	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")

	// Commit A on main.
	writeFile(t, dir, "a.txt", "content-a")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-m", "add a.txt")
	shaA := revParse(t, dir, "HEAD")

	// Lightweight tag at commit A.
	gitRun(t, dir, "tag", "v0.1-lw")

	// Annotated tag at commit A.
	gitRun(t, dir, "tag", "-a", "v0.1", "-m", "annotated v0.1")
	// The annotated tag object SHA differs from shaA; ^{commit} should still
	// resolve to shaA.

	// Commit B on a feature branch.
	gitRun(t, dir, "checkout", "-b", "feat")
	writeFile(t, dir, "b.txt", "content-b")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-m", "add b.txt")
	shaB := revParse(t, dir, "HEAD")

	// Leave HEAD on feat.
	return dir, map[string]string{
		"main": shaA,
		"feat": shaB,
		"HEAD": shaB,
	}
}

// hermeticGitEnv returns the process environment with global and system git
// config neutralized (GIT_CONFIG_GLOBAL/SYSTEM -> os.DevNull). Without this the
// developer's gitconfig leaks into the test: e.g. tag.gpgSign/forceSignAnnotated
// promote the lightweight `git tag v0.1-lw` to a signed annotated tag, which
// then fails with "fatal: no tag message?". Isolation also pins behavior that
// global config can change (init.defaultBranch, commit.gpgsign), making these
// tests deterministic on any machine.
func hermeticGitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
	)
}

// revParse shells `git -C dir rev-parse <ref>^{commit}` and returns the SHA.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", ref+"^{commit}")
	cmd.Env = hermeticGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("revParse %q in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes content to path inside dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
}

// gitRun runs a git command in dir, fataling on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = hermeticGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestResolve_Branch(t *testing.T) {
	repo, shas := initRepo(t)
	got, err := Resolve(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != shas["main"] {
		t.Errorf("got %q, want %q", got, shas["main"])
	}
	if len(got) != 40 {
		t.Errorf("SHA length %d, want 40", len(got))
	}
}

func TestResolve_HEAD(t *testing.T) {
	repo, shas := initRepo(t)
	got, err := Resolve(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != shas["HEAD"] {
		t.Errorf("got %q, want %q", got, shas["HEAD"])
	}
}

func TestResolve_FullSHA(t *testing.T) {
	repo, shas := initRepo(t)
	sha := shas["main"]
	got, err := Resolve(context.Background(), repo, sha)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sha {
		t.Errorf("got %q, want %q", got, sha)
	}
}

func TestResolve_ShortSHA(t *testing.T) {
	repo, shas := initRepo(t)
	short := shas["main"][:7]
	got, err := Resolve(context.Background(), repo, short)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != shas["main"] {
		t.Errorf("got %q, want full SHA %q", got, shas["main"])
	}
}

func TestResolve_LightweightTag(t *testing.T) {
	repo, shas := initRepo(t)
	// v0.1-lw was created at commit A (main).
	got, err := Resolve(context.Background(), repo, "v0.1-lw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != shas["main"] {
		t.Errorf("got %q, want %q", got, shas["main"])
	}
}

func TestResolve_AnnotatedTag(t *testing.T) {
	repo, shas := initRepo(t)
	// v0.1 is an annotated tag pointing at commit A. ^{commit} should
	// dereference through the tag object to the underlying commit SHA.
	got, err := Resolve(context.Background(), repo, "v0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify independently that ^{commit} resolves to commit A's SHA,
	// not the tag object's SHA.
	want := revParse(t, repo, "v0.1")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got != shas["main"] {
		t.Errorf("annotated tag did not dereference to commit A: got %q, want %q", got, shas["main"])
	}
}

func TestResolve_UnknownRef(t *testing.T) {
	repo, _ := initRepo(t)
	_, err := Resolve(context.Background(), repo, "no-such-branch")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ure *UnknownRefError
	if !errors.As(err, &ure) {
		t.Fatalf("expected *UnknownRefError, got %T: %v", err, err)
	}
	if ure.Ref != "no-such-branch" {
		t.Errorf("Ref = %q, want %q", ure.Ref, "no-such-branch")
	}
}

func TestResolve_EmptyRepoPath(t *testing.T) {
	_, err := Resolve(context.Background(), "", "main")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ire *InvalidRepoError
	if !errors.As(err, &ire) {
		t.Fatalf("expected *InvalidRepoError, got %T: %v", err, err)
	}
}

func TestResolve_EmptyRef(t *testing.T) {
	repo, _ := initRepo(t)
	_, err := Resolve(context.Background(), repo, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ure *UnknownRefError
	if !errors.As(err, &ure) {
		t.Fatalf("expected *UnknownRefError, got %T: %v", err, err)
	}
	if ure.Ref != "" {
		t.Errorf("Ref = %q, want empty string", ure.Ref)
	}
}

func TestResolve_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // real dir, no .git
	_, err := Resolve(context.Background(), dir, "main")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ire *InvalidRepoError
	if !errors.As(err, &ire) {
		t.Fatalf("expected *InvalidRepoError, got %T: %v", err, err)
	}
}

func TestResolve_ContextCanceled(t *testing.T) {
	repo, _ := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := Resolve(ctx, repo, "main")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}
