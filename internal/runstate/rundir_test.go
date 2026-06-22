package runstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunDir_HonorsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	got, err := RunDir("run-abc")
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	want := filepath.Join(tmp, "gsd-test", "runs", "run-abc")
	if got != want {
		t.Errorf("RunDir = %q, want %q", got, want)
	}

	// RunDir(id) must share a parent with the state file Path(id).
	statePath, err := Path("run-abc")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if filepath.Dir(got) != filepath.Dir(statePath) {
		t.Errorf("RunDir parent %q != state-file parent %q", filepath.Dir(got), filepath.Dir(statePath))
	}
}

func TestEnsureRunDir_Idempotent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir, err := EnsureRunDir("run-xyz")
	if err != nil {
		t.Fatalf("EnsureRunDir first: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("expected dir created at %q: %v", dir, err)
	}
	// Second call is a no-op and must not error.
	if _, err := EnsureRunDir("run-xyz"); err != nil {
		t.Fatalf("EnsureRunDir second: %v", err)
	}
}
