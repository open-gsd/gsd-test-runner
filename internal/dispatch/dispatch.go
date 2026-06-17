// Package dispatch builds pure command-line argument slices for run-and-die
// execution (ADR-0021). All functions are stateless and have no side-effects;
// they construct a fresh []string on each call.
package dispatch

import (
	"fmt"
	"sort"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// Resource-cap values used in DockerRunArgs. Exported as named consts so they
// are greppable and so tests can assert the values without duplicating the
// literals.
const (
	DefaultPidsLimit = "512"
	DefaultMemory    = "2g"
	DefaultCPUs      = "2"
)

// ReporterPath is the in-image path of the JSON Lines reporter. The run-and-die
// node --test command emits structured events through it to stdout so the
// watchdog can track in-flight tests (kill.last_active_test) and per-test
// telemetry, rather than parsing TAP.
const ReporterPath = "/opt/gsd-test/reporter.mjs"

// isNodeTestPath reports whether spec.TestCommand follows the node --test
// convention that warrants hardening flags. The check requires at least two
// elements, element[0] == "node", and the slice must contain "--test".
func isNodeTestPath(cmd []string) bool {
	if len(cmd) < 2 || cmd[0] != "node" {
		return false
	}
	for _, arg := range cmd {
		if arg == "--test" {
			return true
		}
	}
	return false
}

// TestRunnerArgs builds the argv for the in-container test runner (ADR-0021
// §E). When spec.TestCommand is the node --test path, hardening flags are
// appended in the required order followed by any TestPathPatterns. For any
// other command the slice is returned unchanged (with patterns appended), so
// the watchdog deadline still bounds it.
//
// The function never mutates spec.TestCommand's backing array.
func TestRunnerArgs(spec runspec.Spec, effectiveDeadlineMs int64) []string {
	if !isNodeTestPath(spec.TestCommand) {
		// Custom command: pass through + patterns; build a fresh slice.
		result := make([]string, len(spec.TestCommand), len(spec.TestCommand)+len(spec.TestPathPatterns))
		copy(result, spec.TestCommand)
		result = append(result, spec.TestPathPatterns...)
		return result
	}

	// Node --test path: start with a fresh copy of the base command.
	result := make([]string, len(spec.TestCommand), len(spec.TestCommand)+4+len(spec.TestPathPatterns))
	copy(result, spec.TestCommand)

	// Append hardening flags in the specified order.
	result = append(result, "--test-force-exit")
	result = append(result, fmt.Sprintf("--test-timeout=%d", effectiveDeadlineMs))
	result = append(result, fmt.Sprintf("--experimental-test-isolation=%s", spec.Isolation))

	// Pin concurrency explicitly to bound the orphan fan-out inside the
	// container (ADR-0021 §D/§E). An agent-supplied value wins; otherwise pin to
	// the CPU cap (DefaultCPUs) so the default is bounded, not left to the
	// runner's host-derived default.
	if spec.Concurrency != nil {
		result = append(result, fmt.Sprintf("--test-concurrency=%d", *spec.Concurrency))
	} else {
		result = append(result, "--test-concurrency="+DefaultCPUs)
	}

	// Emit structured events through the JSON reporter to stdout so the watchdog
	// can track in-flight tests and per-test telemetry (ADR-0021 §C/§F).
	result = append(result, "--test-reporter="+ReporterPath)
	result = append(result, "--test-reporter-destination=stdout")

	result = append(result, spec.TestPathPatterns...)
	return result
}

// DockerRunArgs builds the argv that follows the literal "docker" word when
// launching a run container (ADR-0021 §B/D2). The caller is responsible for
// appending the in-container command; DockerRunArgs stops after <imageID>.
//
// Env entries are sorted by key for deterministic output.
func DockerRunArgs(spec runspec.Spec, imageID string, deadlineEpochMs int64, workdirMount string) []string {
	// Sort env keys for determinism.
	envKeys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	// Pre-calculate capacity: base (8) + labels (6) + env pairs (2*N) + image (1).
	base := []string{
		"run", "--rm",
		"--pids-limit", DefaultPidsLimit,
		"--memory", DefaultMemory,
		"--cpus", DefaultCPUs,
		"--label", fmt.Sprintf("%s=%s", reaper.LabelRunID, spec.RunID),
		"--label", fmt.Sprintf("%s=%d", reaper.LabelDeadline, deadlineEpochMs),
		"--label", fmt.Sprintf("sh.gsd-test.target=%s", spec.Target),
	}

	result := make([]string, len(base), len(base)+2*len(envKeys)+1)
	copy(result, base)

	for _, k := range envKeys {
		result = append(result, "-e", fmt.Sprintf("%s=%s", k, spec.Env[k]))
	}

	result = append(result, imageID)
	return result
}
