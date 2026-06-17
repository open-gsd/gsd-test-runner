package codexshim

// Integration guards for issue #69: the Codex shim must REDIRECT a matched
// node --test / npm test invocation to `gsd-test run` (executing it), and exec
// any non-test command unchanged. Driven through bash with stub gsd-test/node
// binaries on PATH so we observe what the shim actually invokes.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runShim runs codex-shim.sh <args...> with a PATH containing stub `gsd-test`,
// `node`, and `npm` that each append their argv to a sentinel file. Returns the
// combined sentinel contents and the shim's exit code.
func runShim(t *testing.T, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gsd-test", "node", "npm"} {
		stub := "#!/bin/sh\nprintf '%s:%s\\n' \"" + name + "\" \"$*\" >> \"" + sentinel + "\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := filepath.Abs(filepath.Join("..", "..", "agent-integration", "codex-shim.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{shim}, args...)...)
	// Prepend the stub dir so our gsd-test/node/npm shadow the real ones, while
	// keeping the system PATH so the shim's own sed/printf still resolve.
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()
	_ = out
	code := cmd.ProcessState.ExitCode()
	data, _ := os.ReadFile(sentinel)
	return string(data), code
}

func TestCodexShim_RedirectsNodeTestToGsdTestRun(t *testing.T) {
	log, code := runShim(t, "node", "--test", "src/foo.test.mjs")
	if code != 0 {
		t.Errorf("exit = %d, want 0 (redirect+exec)", code)
	}
	if !strings.Contains(log, "gsd-test:run src/foo.test.mjs") {
		t.Errorf("did not redirect to `gsd-test run src/foo.test.mjs`; calls:\n%s", log)
	}
	if strings.Contains(log, "node:") {
		t.Errorf("real node must not run for a test invocation; calls:\n%s", log)
	}
}

func TestCodexShim_NpmTestRunsWholeSuite(t *testing.T) {
	log, code := runShim(t, "npm", "test")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(log, "gsd-test:run") {
		t.Errorf("npm test should redirect to `gsd-test run`; calls:\n%s", log)
	}
}

func TestCodexShim_PassesThroughNonTestCommands(t *testing.T) {
	log, code := runShim(t, "node", "build.js")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(log, "node:build.js") {
		t.Errorf("non-test command must exec the real binary unchanged; calls:\n%s", log)
	}
	if strings.Contains(log, "gsd-test:") {
		t.Errorf("non-test command must not be redirected; calls:\n%s", log)
	}
}

// TestCodexShim_PathShimPassthroughNoRecursion installs the shim as a `node`
// PATH-shim (the codex-bin layout) and verifies a non-test command resolves the
// REAL node instead of recursing into the shim — the issue #78 bug. A context
// timeout fails the test rather than hanging if recursion regresses.
func TestCodexShim_PathShimPassthroughNoRecursion(t *testing.T) {
	dir := t.TempDir()
	codexBin := filepath.Join(dir, "codex-bin")
	realDir := filepath.Join(dir, "real")
	for _, d := range []string{codexBin, realDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shim, _ := filepath.Abs(filepath.Join("..", "..", "agent-integration", "codex-shim.sh"))
	// codex-bin/node wrapper: export GSD_SHIM_DIR, exec the shim in wrapper mode.
	wrapper := "#!/bin/sh\nGSD_SHIM_DIR=" + codexBin + " exec sh " + shim + " node \"$@\"\n"
	if err := os.WriteFile(filepath.Join(codexBin, "node"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real node + gsd-test the shim should resolve.
	if err := os.WriteFile(filepath.Join(realDir, "node"), []byte("#!/bin/sh\necho REAL:$*\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "gsd-test"), []byte("#!/bin/sh\necho GSD:$*\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pathEnv := codexBin + string(os.PathListSeparator) + realDir + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(codexBin, "node"), args...)
		cmd.Env = append(os.Environ(), "PATH="+pathEnv)
		out, err := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("shim hung (recursion regression): %s", out)
		}
		return string(out), err
	}

	if out, err := run("app.js"); err != nil || !strings.Contains(out, "REAL:app.js") {
		t.Errorf("passthrough should reach real node: out=%q err=%v", out, err)
	}
	if out, _ := run("--test", "x.test.mjs"); !strings.Contains(out, "GSD:run x.test.mjs") {
		t.Errorf("test invocation should redirect to gsd-test run: out=%q", out)
	}
}
