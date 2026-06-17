package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// Exec runs a run-and-die container with copy-in semantics (ADR-0002 / ADR-0021
// Decision 7): the PR-merged worktree is copied into the container, not
// bind-mounted. The sequence is:
//
//  1. docker create --rm <caps/labels> <image> node watchdog.mjs ... -- <test cmd>
//  2. docker cp <worktreeDir>/. <cid>:/work
//  3. docker start -a <cid>     (streams the watchdog envelope on stdout; --rm
//     auto-removes the container on exit, so no orphan survives)
//
// Returns the container's stdout (the watchdog JSON envelope). Each docker
// invocation goes through runner, which is dockerexec over SSH in production
// (ADR-0014) or the local daemon in tests.
func Exec(ctx context.Context, runner reaper.Runner, spec runspec.Spec, imageID, worktreeDir string, deadlineEpochMs, effectiveDeadlineMs int64) ([]byte, error) {
	// Build the create argv from the run argv, swapping the verb and appending
	// the watchdog-wrapped command. create (not run) so we can copy the worktree
	// in before the process starts.
	createArgs := DockerRunArgs(spec, imageID, deadlineEpochMs, "")
	createArgs[0] = "create"
	createArgs = append(createArgs, InContainerCommand(spec, effectiveDeadlineMs)...)

	idOut, err := runner(ctx, createArgs...)
	if err != nil {
		return nil, fmt.Errorf("dispatch: create container: %w", err)
	}
	cid := strings.TrimSpace(string(idOut))
	if cid == "" {
		return nil, fmt.Errorf("dispatch: create container: empty container id")
	}

	if _, err := runner(ctx, "cp", worktreeDir+"/.", cid+":/work"); err != nil {
		return nil, fmt.Errorf("dispatch: copy worktree into %s: %w", cid, err)
	}

	// `docker start -a` propagates the container's own exit code, so a test
	// failure (1) or a reap (EXIT_REAPED=75) surfaces as a non-zero exit. Those
	// are expected outcomes carried in the watchdog envelope on stdout — not
	// infra failures. Treat a non-zero exit as an error only when no envelope
	// was produced (a genuine launch failure).
	out, err := runner(ctx, "start", "-a", cid)
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("dispatch: start container %s: %w", cid, err)
	}
	return out, nil
}
