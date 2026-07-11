package runner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/open-gsd/gsd-test-runner/internal/digest"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// WriteRunArtifacts writes the failure-first digest (FAILURES.md, failures.json,
// per-failure files) for reps under the per-run XDG dir for runID and returns
// the artifact paths (epic #84, ADR-0023). Best-effort: a write failure is
// logged to stderr and returns whatever paths succeeded, never affecting the
// caller's exit code.
func WriteRunArtifacts(runID string, reps []report.Report, stderr io.Writer) digest.Paths {
	dir, err := runstate.EnsureRunDir(runID)
	if err != nil {
		fmt.Fprintf(stderr, "warning: create run artifact dir: %v\n", err)
		return digest.Paths{}
	}
	paths, err := digest.WriteDigest(dir, reps, digest.WriteOpts{PerFailureFiles: true})
	if err != nil {
		fmt.Fprintf(stderr, "warning: write run digest: %v\n", err)
		return digest.Paths{Dir: dir}
	}
	return paths
}

// WriteVerdict prints the loud last-line machine verdict as the final stdout
// line (Option C). Best-effort: never changes the exit code (the verdict's
// outcome is the source of truth regardless of the artifacts).
func WriteVerdict(reps []report.Report, paths digest.Paths, stdout, stderr io.Writer) {
	if err := digest.Verdict(reps, paths).WriteLine(stdout); err != nil {
		fmt.Fprintf(stderr, "warning: write verdict line: %v\n", err)
	}
}

// WriteInconclusiveVerdict emits a minimal verdict line with outcome=infra_error
// (the canonical project outcome for a genuine-inconclusive run) to stdout.
// Called before any early-return that represents a run-outcome failure (not a
// CLI usage error). Best-effort: a write error is reported to stderr and never
// changes the exit code.
func WriteInconclusiveVerdict(stdout, stderr io.Writer) {
	v := digest.VerdictLine{
		Type:           "verdict",
		Outcome:        string(report.OutcomeInfraError),
		PerOS:          map[string]digest.OSCount{},
		UniqueFailures: 0,
		TotalFailures:  0,
		Top:            []digest.VerdictTop{},
		Artifacts:      digest.VerdictArtifacts{},
	}
	if err := v.WriteLine(stdout); err != nil {
		fmt.Fprintf(stderr, "warning: write inconclusive verdict line: %v\n", err)
	}
}

// emitRunArtifacts is the standard multi-OS path: it mints a run-id, writes the
// digest, persists each OS's full JSONL, and prints the verdict as the final
// stdout line in every outcome.
func emitRunArtifacts(reps []report.Report, osJSONL map[string]string, stdout, stderr io.Writer) {
	runID, err := runspec.NewRunID()
	if err != nil {
		runID = "run-unknown"
	}
	paths := WriteRunArtifacts(runID, reps, stderr)
	if paths.Dir != "" {
		paths.EventsJSONL = copyEventsJSONL(paths.Dir, osJSONL, stderr)
	}
	WriteVerdict(reps, paths, stdout, stderr)
}

// EmitRunDieArtifacts is the run-and-die single-OS path: it writes the digest
// under the run's existing run-id and prints the verdict as the final stdout
// line, so `gsd-test run`/`wait` share the standard path's failure-first
// contract (Option C, epic #84).
func EmitRunDieArtifacts(runID string, rep report.Report, stdout, stderr io.Writer) {
	reps := []report.Report{rep}
	WriteVerdict(reps, WriteRunArtifacts(runID, reps, stderr), stdout, stderr)
}

// copyEventsJSONL persists each per-OS drained JSONL into the run dir as
// test-events-<os>.jsonl so the full per-test detail (passes included) is always
// available, not just the failure digest (Option B, #84). Best-effort.
// Returns the path of the last successfully persisted file (or "" when none),
// which the caller assigns to paths.EventsJSONL for the verdict (B-11).
// After a successful copy the source temp file is removed to avoid orphaned
// os.TempDir() files (issue #102, Option D).
func copyEventsJSONL(dir string, osJSONL map[string]string, stderr io.Writer) string {
	var lastPersisted string
	for osName, src := range osJSONL {
		if src == "" {
			continue
		}
		dst := filepath.Join(dir, "test-events-"+osName+".jsonl")
		if err := copyFile(src, dst); err != nil {
			fmt.Fprintf(stderr, "warning: persist %s JSONL: %v\n", osName, err)
			continue
		}
		lastPersisted = dst
		_ = os.Remove(src)
	}
	return lastPersisted
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
