// Package dispatch_test exercises the pure command-construction helpers in the
// dispatch package. Tests follow the project convention: stdlib testing only,
// table-driven with t.Run(tt.name,...), names TestXxx_Description, slices
// compared with reflect.DeepEqual.
package dispatch_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// helpers

func ptrInt(v int) *int { return &v }

// baseSpec returns a minimal valid Spec with the node --test defaults so
// individual test cases only need to override the fields they care about.
func baseSpec() runspec.Spec {
	return runspec.Spec{
		RunID:       "run-abc",
		Repo:        "/work",
		Target:      "linux",
		TestCommand: []string{"node", "--test"},
		Isolation:   runspec.IsolationProcess,
	}
}

// ── TestRunnerArgs ────────────────────────────────────────────────────────────

func TestRunnerArgs_NodePath_AllHardeningFlagsInOrder(t *testing.T) {
	spec := baseSpec()
	got := dispatch.TestRunnerArgs(spec, 180000)
	want := []string{
		"node", "--test",
		"--test-force-exit",
		"--test-timeout=180000",
		"--experimental-test-isolation=process",
		"--test-concurrency=2",
		"--test-reporter=/opt/gsd-test/reporter.mjs",
		"--test-reporter-destination=stdout",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_NodePath_WithPatterns(t *testing.T) {
	spec := baseSpec()
	spec.TestPathPatterns = []string{"test/**/*.test.js", "src/**/*.test.js"}
	got := dispatch.TestRunnerArgs(spec, 60000)
	want := []string{
		"node", "--test",
		"--test-force-exit",
		"--test-timeout=60000",
		"--experimental-test-isolation=process",
		"--test-concurrency=2",
		"--test-reporter=/opt/gsd-test/reporter.mjs",
		"--test-reporter-destination=stdout",
		"test/**/*.test.js",
		"src/**/*.test.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_NodePath_IsolationNone(t *testing.T) {
	spec := baseSpec()
	spec.Isolation = runspec.IsolationNone
	got := dispatch.TestRunnerArgs(spec, 90000)
	want := []string{
		"node", "--test",
		"--test-force-exit",
		"--test-timeout=90000",
		"--experimental-test-isolation=none",
		"--test-concurrency=2",
		"--test-reporter=/opt/gsd-test/reporter.mjs",
		"--test-reporter-destination=stdout",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_NodePath_ConcurrencyPinWhenSet(t *testing.T) {
	spec := baseSpec()
	spec.Concurrency = ptrInt(4)
	got := dispatch.TestRunnerArgs(spec, 120000)
	want := []string{
		"node", "--test",
		"--test-force-exit",
		"--test-timeout=120000",
		"--experimental-test-isolation=process",
		"--test-concurrency=4",
		"--test-reporter=/opt/gsd-test/reporter.mjs",
		"--test-reporter-destination=stdout",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_NodePath_ConcurrencyPinnedToCPUCapWhenNil(t *testing.T) {
	spec := baseSpec()
	// Concurrency is nil by default; it must still be pinned (to the CPU cap) so
	// the orphan fan-out inside the container is bounded (ADR-0021 §D/§E).
	got := dispatch.TestRunnerArgs(spec, 30000)
	var found bool
	for _, arg := range got {
		if arg == "--test-concurrency="+dispatch.DefaultCPUs {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --test-concurrency=%s when Concurrency == nil, got %v", dispatch.DefaultCPUs, got)
	}
}

func TestRunnerArgs_NodePath_ConcurrencyAndPatterns(t *testing.T) {
	spec := baseSpec()
	spec.Concurrency = ptrInt(2)
	spec.TestPathPatterns = []string{"tests/unit"}
	got := dispatch.TestRunnerArgs(spec, 45000)
	want := []string{
		"node", "--test",
		"--test-force-exit",
		"--test-timeout=45000",
		"--experimental-test-isolation=process",
		"--test-concurrency=2",
		"--test-reporter=/opt/gsd-test/reporter.mjs",
		"--test-reporter-destination=stdout",
		"tests/unit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_CustomCommand_PassthroughNoNodeFlags(t *testing.T) {
	spec := baseSpec()
	spec.TestCommand = []string{"npm", "test"}
	got := dispatch.TestRunnerArgs(spec, 180000)
	want := []string{"npm", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRunnerArgs_CustomCommand_WithPatterns(t *testing.T) {
	spec := baseSpec()
	spec.TestCommand = []string{"npm", "test"}
	spec.TestPathPatterns = []string{"test/**"}
	got := dispatch.TestRunnerArgs(spec, 180000)
	want := []string{"npm", "test", "test/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// A command starting with "node" but lacking "--test" is treated as custom.
func TestRunnerArgs_NodeWithoutTestFlag_TreatedAsCustom(t *testing.T) {
	spec := baseSpec()
	spec.TestCommand = []string{"node", "server.js"}
	got := dispatch.TestRunnerArgs(spec, 180000)
	want := []string{"node", "server.js"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// A single-element command cannot satisfy the "at least 2 elements" check.
func TestRunnerArgs_SingleElementCommand_TreatedAsCustom(t *testing.T) {
	spec := baseSpec()
	spec.TestCommand = []string{"jest"}
	got := dispatch.TestRunnerArgs(spec, 180000)
	want := []string{"jest"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TestRunnerArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// Verify that TestRunnerArgs never mutates the original TestCommand slice.
func TestRunnerArgs_DoesNotMutateSpec(t *testing.T) {
	spec := baseSpec()
	original := make([]string, len(spec.TestCommand))
	copy(original, spec.TestCommand)
	_ = dispatch.TestRunnerArgs(spec, 60000)
	if !reflect.DeepEqual(spec.TestCommand, original) {
		t.Errorf("TestCommand was mutated: got %v, want %v", spec.TestCommand, original)
	}
}

// ── TestDockerRunArgs ─────────────────────────────────────────────────────────

func TestDockerRunArgs_RequiredStructureAndCaps(t *testing.T) {
	spec := baseSpec()
	got := dispatch.DockerRunArgs(spec, "sha256:abc123", 9999000, "/host/work:/work")
	// Check run --rm at the front.
	if len(got) < 2 || got[0] != "run" || got[1] != "--rm" {
		t.Errorf("DockerRunArgs[0:2] = %v, want [run --rm]", got[:min(2, len(got))])
	}
	// Check last element is imageID.
	if got[len(got)-1] != "sha256:abc123" {
		t.Errorf("DockerRunArgs last = %q, want imageID", got[len(got)-1])
	}
	// Resource caps must be present.
	mustContainPair(t, got, "--pids-limit", "512")
	mustContainPair(t, got, "--memory", "2g")
	mustContainPair(t, got, "--cpus", "2")
}

func TestDockerRunArgs_LabelsPresent(t *testing.T) {
	spec := baseSpec()
	spec.RunID = "run-xyz"
	spec.Target = "linux"
	got := dispatch.DockerRunArgs(spec, "img:latest", 1234567890, "/w:/w")
	mustContainPair(t, got, "--label", "sh.gsd-test.run-id=run-xyz")
	mustContainPair(t, got, "--label", "sh.gsd-test.deadline=1234567890")
	mustContainPair(t, got, "--label", "sh.gsd-test.target=linux")
}

func TestDockerRunArgs_EnvVarsSortedAndRendered(t *testing.T) {
	spec := baseSpec()
	spec.Env = map[string]string{
		"ZEBRA": "z",
		"ALPHA": "a",
		"MANGO": "m",
	}
	got := dispatch.DockerRunArgs(spec, "img:latest", 0, "/w:/w")
	// Collect -e positions.
	var envArgs []string
	for i := 0; i+1 < len(got); i++ {
		if got[i] == "-e" {
			envArgs = append(envArgs, got[i+1])
			i++ // skip value
		}
	}
	wantEnv := []string{"ALPHA=a", "MANGO=m", "ZEBRA=z"}
	if !reflect.DeepEqual(envArgs, wantEnv) {
		t.Errorf("env args = %v, want %v", envArgs, wantEnv)
	}
}

func TestDockerRunArgs_NoEnvArgs_WhenEnvEmpty(t *testing.T) {
	spec := baseSpec()
	spec.Env = nil
	got := dispatch.DockerRunArgs(spec, "img:latest", 0, "/w:/w")
	for _, arg := range got {
		if arg == "-e" {
			t.Errorf("unexpected -e flag in args when Env is nil: %v", got)
			break
		}
	}
}

// TestDockerRunArgs_TelemetrySamplingEnv verifies that the run-spec telemetry
// knobs are forwarded to the container as env so the in-child leak-probe can do
// periodic in-flight handle sampling (ADR-0021 §A).
func TestDockerRunArgs_TelemetrySamplingEnv(t *testing.T) {
	collectEnv := func(args []string) []string {
		var env []string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-e" {
				env = append(env, args[i+1])
				i++
			}
		}
		return env
	}

	t.Run("no telemetry → no sampling env", func(t *testing.T) {
		got := dispatch.DockerRunArgs(baseSpec(), "img:latest", 0, "")
		for _, e := range collectEnv(got) {
			if strings.HasPrefix(e, "GSD_SAMPLE_HANDLES_MS") || strings.HasPrefix(e, "GSD_CAPTURE_STACKS") {
				t.Errorf("unexpected sampling env when telemetry unset: %q", e)
			}
		}
	})

	t.Run("interval only", func(t *testing.T) {
		spec := baseSpec()
		spec.Telemetry.SampleHandlesMs = 5000
		env := collectEnv(dispatch.DockerRunArgs(spec, "img:latest", 0, ""))
		if !slicesContains(env, "GSD_SAMPLE_HANDLES_MS=5000") {
			t.Errorf("env = %v, want GSD_SAMPLE_HANDLES_MS=5000", env)
		}
		if slicesContains(env, "GSD_CAPTURE_STACKS=1") {
			t.Errorf("env = %v, did not want GSD_CAPTURE_STACKS when captureStacks false", env)
		}
	})

	t.Run("interval + captureStacks", func(t *testing.T) {
		spec := baseSpec()
		spec.Telemetry.SampleHandlesMs = 250
		spec.Telemetry.CaptureStacks = true
		env := collectEnv(dispatch.DockerRunArgs(spec, "img:latest", 0, ""))
		if !slicesContains(env, "GSD_SAMPLE_HANDLES_MS=250") || !slicesContains(env, "GSD_CAPTURE_STACKS=1") {
			t.Errorf("env = %v, want both sampling vars", env)
		}
	})

	t.Run("captureStacks without an interval is inert", func(t *testing.T) {
		spec := baseSpec()
		spec.Telemetry.CaptureStacks = true // sampling disabled (interval 0)
		for _, e := range collectEnv(dispatch.DockerRunArgs(spec, "img:latest", 0, "")) {
			if strings.HasPrefix(e, "GSD_SAMPLE_HANDLES_MS") || strings.HasPrefix(e, "GSD_CAPTURE_STACKS") {
				t.Errorf("unexpected sampling env with no interval: %q", e)
			}
		}
	})
}

func slicesContains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestDockerRunArgs_FullOrder(t *testing.T) {
	spec := baseSpec()
	spec.RunID = "run-1"
	spec.Target = "linux"
	spec.Env = map[string]string{"CI": "1", "NODE_ENV": "test"}
	got := dispatch.DockerRunArgs(spec, "sha256:deadbeef", 5000, "/mnt:/mnt")
	want := []string{
		"run", "--rm",
		"--pids-limit", "512",
		"--memory", "2g",
		"--cpus", "2",
		"--label", "sh.gsd-test.run-id=run-1",
		"--label", "sh.gsd-test.deadline=5000",
		"--label", "sh.gsd-test.target=linux",
		"-e", "CI=1",
		"-e", "NODE_ENV=test",
		"sha256:deadbeef",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DockerRunArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// TestDockerRunArgs_ExportedCaps verifies that the exported cap consts match
// the actual values embedded in the argv (so refactors keep them consistent).
func TestDockerRunArgs_ExportedCaps(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"DefaultPidsLimit string", dispatch.DefaultPidsLimit, "512"},
		{"DefaultMemory string", dispatch.DefaultMemory, "2g"},
		{"DefaultCPUs string", dispatch.DefaultCPUs, "2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// mustContainPair asserts that flag and value appear as adjacent elements in args.
func mustContainPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("args do not contain %q %q\n  full args: %v", flag, value, args)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
