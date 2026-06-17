package dispatch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// requireWindowsContainers skips unless the docker daemon runs Windows
// containers. The Windows orphaned-node.exe gate (ADR-0021 Decision 4) can only
// be verified against a real Windows Bench; this test is written to run there
// and skips on Linux/macOS daemons.
func requireWindowsContainers(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").Output()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	if strings.TrimSpace(string(out)) != "windows" {
		t.Skip("requires a Windows-container docker daemon (Windows Bench)")
	}
}

var winBuildOnce sync.Once

// TestE2E_Windows_WatchdogReapsViaTaskkill verifies that on Windows the
// watchdog reaps a hanging node --test via the taskkill /T tree path (no POSIX
// process groups) and that the disposable container leaves nothing behind —
// the orphaned-node.exe gate from ADR-0021 Decision 4. Runs only on a Windows
// Bench; skips elsewhere.
func TestE2E_Windows_WatchdogReapsViaTaskkill(t *testing.T) {
	requireWindowsContainers(t)

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const image = "ghcr.io/open-gsd/gsd-tester-windows"
	winBuildOnce.Do(func() {
		cmd := exec.Command("docker", "build",
			"-f", filepath.Join(root, "dockerfiles", "windows.Dockerfile"),
			"--build-arg", "IMAGE_VERSION=e2e", "-t", image, root)
		if out, berr := cmd.CombinedOutput(); berr != nil {
			t.Fatalf("build windows tester image: %v\n%s", berr, out)
		}
	})

	// A leaked-timer hang under isolation=none so the shared runner wedges and
	// only the watchdog can reap it.
	wt := t.TempDir()
	hang := "import { test } from 'node:test';\n" +
		"test('wedges', async () => { await new Promise(() => { setInterval(() => {}, 1000); }); });\n"
	if err := os.WriteFile(filepath.Join(wt, "wedge.test.mjs"), []byte(hang), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := runspec.Spec{
		RunID: "win-e2e", Repo: wt, Target: "windows",
		TestCommand: []string{"node", "--test"},
		Budget:      runspec.Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
		Isolation:   runspec.IsolationNone,
	}
	rep, err := dispatch.RunCopyIn(context.Background(), localRunner, spec, image, wt,
		time.Now().Add(time.Hour).UnixMilli(), 2000, time.Now())
	if err != nil {
		t.Fatalf("RunCopyIn: %v", err)
	}
	if rep.Outcome != report.OutcomeReaped {
		t.Fatalf("Outcome = %q, want reaped (taskkill path)", rep.Outcome)
	}
	// The --rm container is gone, so no node.exe can have survived it.
	psOut, _ := exec.Command("docker", "ps", "-aq",
		"--filter", "label=sh.gsd-test.run-id=win-e2e").Output()
	if strings.TrimSpace(string(psOut)) != "" {
		t.Errorf("run container survived; orphaned processes possible")
	}
}
