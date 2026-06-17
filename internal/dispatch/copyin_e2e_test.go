package dispatch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

var buildOnce sync.Once

// buildTesterImage builds the real Linux Tester Image (which bakes the
// watchdog) once, so the copy-in path is exercised against the production image
// rather than a stand-in.
func buildTesterImage(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const tag = "gsd-tester-linux:e2e"
	buildOnce.Do(func() {
		cmd := exec.Command("docker", "build",
			"-f", filepath.Join(root, "dockerfiles", "linux.Dockerfile"),
			"--build-arg", "IMAGE_VERSION=e2e",
			"-t", tag, root)
		if out, berr := cmd.CombinedOutput(); berr != nil {
			t.Fatalf("docker build tester image: %v\n%s", berr, out)
		}
	})
	return tag
}

func localRunner(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).Output()
}

// writeWorktree creates a temp worktree dir holding one test file.
func writeWorktree(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestE2E_CopyIn_PassingRun proves the full production copy-in path against the
// real Tester Image: create --rm -> cp worktree -> start -> watchdog envelope ->
// passed Report. The container auto-removes (#60, ADR-0021 / ADR-0002).
func TestE2E_CopyIn_PassingRun(t *testing.T) {
	requireDocker(t)
	image := buildTesterImage(t)

	wt := writeWorktree(t, "ok.test.mjs",
		"import { test } from 'node:test';\nimport assert from 'node:assert';\n"+
			"test('passes', () => { assert.ok(true); });\n")

	spec := runspec.Spec{
		RunID: "e2e-pass", Repo: wt, Target: "linux",
		TestCommand: []string{"node", "--test"},
		Budget:      runspec.Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
		Isolation:   runspec.IsolationProcess,
	}
	rep, err := dispatch.RunCopyIn(context.Background(), localRunner, spec, image, wt,
		time.Now().Add(time.Hour).UnixMilli(), 30000, time.Now())
	if err != nil {
		t.Fatalf("RunCopyIn: %v", err)
	}
	if rep.Outcome != report.OutcomePassed {
		t.Errorf("Outcome = %q, want passed", rep.Outcome)
	}
	// Per-test telemetry is captured via the JSON reporter.
	if len(rep.PerTest) == 0 || rep.PerTest[0].Status != "passed" {
		t.Errorf("per-test telemetry not captured: %+v", rep.PerTest)
	}
}

// TestE2E_CopyIn_WedgeReaped proves the watchdog backstop. Under isolation=none
// all tests share the runner process, so a synchronous busy loop blocks that
// process's event loop — neither --test-timeout nor --test-force-exit can fire.
// Only the watchdog (a separate process) reaps it via SIGKILL. This is exactly
// the "a wedged test poisons the shared process and only the reaper can save it"
// case from ADR-0021 Decision 5. (Under process isolation, by contrast, the
// parent runner kills the wedged child via --test-timeout and no reap is needed
// — which is the hardening working as designed.)
func TestE2E_CopyIn_WedgeReaped(t *testing.T) {
	requireDocker(t)
	image := buildTesterImage(t)

	wt := writeWorktree(t, "wedge.test.mjs",
		"import { test } from 'node:test';\n"+
			"test('wedges the runner', () => { while (true) {} });\n")

	spec := runspec.Spec{
		RunID: "e2e-wedge", Repo: wt, Target: "linux",
		TestCommand: []string{"node", "--test"},
		Budget:      runspec.Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
		Isolation:   runspec.IsolationNone,
	}
	// effectiveDeadlineMs passed directly (2s) to keep the test fast; the
	// watchdog default grace is 5s, so SIGKILL lands ~7s in.
	rep, err := dispatch.RunCopyIn(context.Background(), localRunner, spec, image, wt,
		time.Now().Add(time.Hour).UnixMilli(), 2000, time.Now())
	if err != nil {
		t.Fatalf("RunCopyIn: %v", err)
	}
	if rep.Outcome != report.OutcomeReaped {
		t.Fatalf("Outcome = %q, want reaped; Kill=%+v", rep.Outcome, rep.Kill)
	}
	if rep.Kill == nil || rep.Kill.ReapedBy != report.ReapedByInContainer {
		t.Errorf("Kill = %+v, want in_container reap", rep.Kill)
	}
	// NOTE: a *synchronous* busy loop blocks the runner's event loop, so the
	// reporter cannot emit test:start before the wedge — kill.last_active_test
	// is legitimately unavailable for a CPU-blocked runner (the reporter is
	// blocked too). The container-teardown guarantee still holds; attribution is
	// best-effort. Per-test attribution is exercised by TestE2E_CopyIn_PassingRun
	// (events flow for tests that yield to the event loop) and the watchdog/
	// dispatch unit tests.
}

// TestE2E_CopyIn_NpmCiAndBuild proves the entry script runs `npm ci` and
// `npm run build` before the tests (ADR-0021 Decision 1). The build writes a
// file the test imports, so a green run can only happen if both ran first.
func TestE2E_CopyIn_NpmCiAndBuild(t *testing.T) {
	requireDocker(t)
	image := buildTesterImage(t)

	wt := t.TempDir()
	writeFile := func(name, body string) {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("package.json", `{
  "name": "demo", "version": "1.0.0",
  "scripts": { "build": "node -e \"require('fs').writeFileSync('built.txt','ok')\"" }
}`)
	writeFile("package-lock.json", `{
  "name": "demo", "version": "1.0.0", "lockfileVersion": 3, "requires": true,
  "packages": { "": { "name": "demo", "version": "1.0.0" } }
}`)
	// Passes only if `npm run build` created built.txt before the tests ran.
	writeFile("build.test.mjs",
		"import { test } from 'node:test';\nimport assert from 'node:assert';\nimport { existsSync } from 'node:fs';\n"+
			"test('build output is present', () => { assert.ok(existsSync('built.txt')); });\n")

	spec := runspec.Spec{
		RunID: "e2e-ci", Repo: wt, Target: "linux",
		TestCommand: []string{"node", "--test"},
		Budget:      runspec.Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
		Isolation:   runspec.IsolationProcess,
	}
	rep, err := dispatch.RunCopyIn(context.Background(), localRunner, spec, image, wt,
		time.Now().Add(time.Hour).UnixMilli(), 120000, time.Now())
	if err != nil {
		t.Fatalf("RunCopyIn: %v", err)
	}
	if rep.Outcome != report.OutcomePassed {
		t.Fatalf("Outcome = %q, want passed (npm ci + build must run before tests)", rep.Outcome)
	}
}
