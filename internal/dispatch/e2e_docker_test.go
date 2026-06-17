package dispatch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireDocker skips when no docker daemon is reachable, keeping the unit
// suite green on machines without Docker.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

// TestE2E_WatchdogReapsHangingTestInContainer is the run-and-die end-to-end
// proof (#60, ADR-0021): a real node container runs the watchdog wrapping a
// hanging `node --test`; the watchdog must reap it, emit a reaped envelope, and
// exit EXIT_REAPED (75). With --rm the container — and every orphan in it —
// dies. Verified against a live Docker daemon.
func TestE2E_WatchdogReapsHangingTestInContainer(t *testing.T) {
	requireDocker(t)

	reporterDir, err := filepath.Abs("../../reporter")
	if err != nil {
		t.Fatal(err)
	}

	// A test that leaks a dangling timer (the issue's motivating orphan case):
	// the interval keeps the event loop alive and the promise never resolves, so
	// node --test (default --test-timeout=Infinity) hangs until the watchdog
	// reaps it.
	work := t.TempDir()
	hang := "import { test } from 'node:test';\n" +
		"test('hangs forever', async () => {\n" +
		"  await new Promise(() => { setInterval(() => {}, 1000); });\n" +
		"});\n"
	if err := os.WriteFile(filepath.Join(work, "hang.test.mjs"), []byte(hang), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", reporterDir+":/opt/gsd-test:ro",
		"-v", work+":/work:ro",
		"node:22-alpine",
		"node", "/opt/gsd-test/watchdog.mjs",
		"--deadline-ms", "600", "--grace-ms", "300", "--reason", "estimate_overrun",
		"--", "node", "--test", "/work/hang.test.mjs",
	)
	out, err := cmd.CombinedOutput()

	// EXIT_REAPED is 75; exec reports it as a non-nil ExitError.
	var exitCode int
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("docker run failed to launch: %v\n%s", err, out)
	}

	got := string(out)
	if !strings.Contains(got, `"outcome":"reaped"`) {
		t.Errorf("watchdog did not report a reaped outcome.\noutput:\n%s", got)
	}
	if exitCode != 75 {
		t.Errorf("exit code = %d, want 75 (EXIT_REAPED).\noutput:\n%s", exitCode, got)
	}
}
