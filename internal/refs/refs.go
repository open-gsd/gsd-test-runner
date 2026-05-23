package refs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UnknownRefError reports that the requested ref does not resolve in
// repoPath.
type UnknownRefError struct {
	Ref      string
	RepoPath string
	Stderr   string
}

func (e *UnknownRefError) Error() string {
	return fmt.Sprintf("unknown ref %q in %s: %s", e.Ref, e.RepoPath, strings.TrimSpace(e.Stderr))
}

// InvalidRepoError reports that repoPath is not a git repository.
type InvalidRepoError struct {
	RepoPath string
	Detail   string
}

func (e *InvalidRepoError) Error() string {
	return fmt.Sprintf("not a git repository: %s (%s)", e.RepoPath, e.Detail)
}

// Resolve turns a user-supplied git ref into a full 40-character
// commit SHA by running `git rev-parse <userRef>^{commit}` in repoPath.
// The ^{commit} suffix dereferences annotated tags to the underlying
// commit, which is what every downstream consumer expects.
//
// Returns *UnknownRefError if the ref does not resolve, *InvalidRepoError
// if repoPath is not a git repo, or a wrapped exec error for other
// failures (e.g., git binary missing).
func Resolve(ctx context.Context, repoPath, userRef string) (string, error) {
	if repoPath == "" {
		return "", &InvalidRepoError{RepoPath: repoPath, Detail: "empty path"}
	}
	if userRef == "" {
		return "", &UnknownRefError{Ref: userRef, RepoPath: repoPath, Stderr: "empty ref"}
	}

	// Verify repoPath is a git repo. A .git file (submodules) or a
	// .git directory both count.
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &InvalidRepoError{RepoPath: repoPath, Detail: "no .git entry"}
		}
		return "", &InvalidRepoError{RepoPath: repoPath, Detail: err.Error()}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--verify", userRef+"^{commit}")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &UnknownRefError{Ref: userRef, RepoPath: repoPath, Stderr: stderr.String()}
		}
		return "", fmt.Errorf("git rev-parse failed for %q in %s: %w", userRef, repoPath, err)
	}

	sha := strings.TrimSpace(stdout.String())
	if len(sha) != 40 {
		return "", fmt.Errorf("git rev-parse returned unexpected output for %q in %s: %q", userRef, repoPath, sha)
	}
	return sha, nil
}
