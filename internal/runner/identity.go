package runner

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/plan"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/refs"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// DefaultContainerTTL is the Tier-2 reaper deadline (ADR-0021 Decision 2) that
// every Pipeline container's ContainerIdentity.DeadlineMs carries (ADR-0029
// Part C). It must comfortably exceed a realistic single-cell run so a
// container that is merely slow is never mistaken for leaked; a container
// still alive past this TTL is presumed leaked (its process wedged or the
// engine crashed) and is swept on the next invocation that touches the same
// Bench + branch.
const DefaultContainerTTL = 2 * time.Hour

// cellID names one (OS, NodeMajor) cell within a single multi-OS/Node-matrix
// run, disambiguating cells within one run for the ADR-0029 container --name
// (Docker rejects duplicate --name values, so concurrent cells of the same
// run/branch must not collide). Returns "<os>-node<major>" when nodeMajor is
// set, or just "<os>" when the run has no Node-major axis.
func cellID(osName, nodeMajor string) string {
	if nodeMajor == "" {
		return osName
	}
	return osName + "-node" + nodeMajor
}

// resolveBranchSlug determines the ADR-0029 branch slug for this invocation.
// It never fails the run — branch-resolution failures fall back to the
// runspec.BranchSlugUnknown sentinel rather than propagating an error.
//
// Resolution order:
//  1. Start from opts.Head (the fix under test).
//  2. If that is empty or the literal "HEAD" (not supplied, or a detached
//     HEAD upstream), ask git directly via refs.CurrentBranch(ctx, opts.Source).
//  3. If the result is still empty or "HEAD", use the sentinel; otherwise
//     slugify it via runspec.SlugifyBranch (the single ADR-0029 slug rules
//     implementation, shared with the dispatch/Watchdog engine).
func resolveBranchSlug(ctx context.Context, opts Options) string {
	result := opts.Head
	if result == "" || result == "HEAD" {
		if branch, err := refs.CurrentBranch(ctx, opts.Source); err == nil && branch != "" && branch != "HEAD" {
			result = branch
		}
	}
	if result == "" || result == "HEAD" {
		return runspec.BranchSlugUnknown
	}
	return runspec.SlugifyBranch(result)
}

// buildContainerIdentity assembles the ADR-0029 ContainerIdentity for one
// pipeline cell. Factored out as a pure function (rather than inlined at the
// pipeline.New call site) so the identity-construction logic — in particular
// the RunID/BranchSlug non-empty guarantee and the Cell derivation — is
// directly unit-testable without exercising the rest of Run's Bench/Docker
// machinery.
func buildContainerIdentity(branchSlug, runID string, run plan.Run, deadlineMs int64) pipeline.ContainerIdentity {
	return pipeline.ContainerIdentity{
		BranchSlug: branchSlug,
		RunID:      runID,
		Cell:       cellID(run.OS, run.NodeMajor),
		Target:     run.OS,
		DeadlineMs: deadlineMs,
	}
}

// sweepBench is a package-level test seam (ADR-0014 decision 3, mirroring the
// dockerRun-style var pattern used in internal/pipeline) for the Tier-2
// reaper sweep of a single Bench. The real implementation adapts
// dockerexec.Run into a reaper.Runner exactly as cmd/gsd-test/main.go's
// dispatchRun does, then delegates to reaper.Sweep. Tests swap it to assert
// sweepStaleContainers calls it exactly once per distinct Bench (never once
// per cell — concurrent sweeps of the same Bench would race).
var sweepBench = func(ctx context.Context, b bench.Bench, nowMs int64, branchSlug string) ([]reaper.Container, error) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		out, runErr := dockerexec.Run(ctx, b, args)
		return []byte(out), runErr
	}
	return reaper.Sweep(ctx, runner, nowMs, branchSlug)
}

// sweepStaleContainers runs the Tier-2 reaper sweep ("reap on next contact",
// ADR-0021 Decision 2) once for each DISTINCT Bench referenced by
// benchesByOS, scoped to branchSlug (ADR-0029 §3: only containers labeled for
// this invocation's branch are reaped). Benches are deduped by Name and swept
// exactly once — NOT once per cell — because the per-cell schedule.Run work
// closure runs concurrently, and concurrent sweeps of the same Bench would
// race each other. Must be called before schedule.Run starts.
//
// A sweep error is logged to stderr and does NOT fail the run, matching how
// cmd/gsd-test/main.go's dispatch path handles reaper.Sweep failures. Reaped
// containers are also reported to stderr in the same style.
func sweepStaleContainers(ctx context.Context, benchesByOS map[string][]bench.Bench, branchSlug string, stderr io.Writer) {
	seen := make(map[string]bool)
	for _, benches := range benchesByOS {
		for _, b := range benches {
			if seen[b.Name] {
				continue
			}
			seen[b.Name] = true
			reaped, err := sweepBench(ctx, b, time.Now().UnixMilli(), branchSlug)
			if err != nil {
				fmt.Fprintf(stderr, "bench=%s: warning: reaper sweep: %v\n", b.Name, err)
				continue
			}
			if len(reaped) > 0 {
				fmt.Fprintf(stderr, "bench=%s: reaped %d stale container(s) before running\n", b.Name, len(reaped))
			}
		}
	}
}
