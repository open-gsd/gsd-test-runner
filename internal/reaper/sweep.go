package reaper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Runner executes a docker CLI invocation (args after the `docker` binary) and
// returns its stdout. In production this is backed by internal/dockerexec,
// which shells docker over SSH to the Bench (ADR-0014); in tests it is faked or
// pointed at the local docker daemon.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// psFormat emits one line per run container: ID, deadline label, run-id label,
// name, branch-slug label — tab-separated. .Label yields an empty string when
// the label is absent; .Name yields Docker's auto-generated random name (or
// the gsd-test-<slug>-<runIdTail> value from ADR-0029 when set).
var psFormat = fmt.Sprintf(`{{.ID}}\t{{.Label %q}}\t{{.Label %q}}\t{{.Name}}\t{{.Label %q}}`, LabelDeadline, LabelRunID, LabelBranch)

// List returns the run containers currently present on the Bench, identified by
// the run-id label. Containers without a parseable deadline label report
// DeadlineMs == 0 (and are therefore never reaped by Sweep). Name and
// BranchSlug are populated from --name and sh.gsd-test.branch respectively;
// both are empty for pre-ADR-0029 containers that carry neither.
func List(ctx context.Context, run Runner) ([]Container, error) {
	out, err := run(ctx, "ps", "--no-trunc",
		"--filter", "label="+LabelRunID,
		"--format", psFormat)
	if err != nil {
		return nil, fmt.Errorf("reaper: list containers: %w", err)
	}
	return parsePS(out), nil
}

// parsePS turns `docker ps` tab-separated output into Containers. Blank lines
// are skipped; a missing or non-numeric deadline becomes 0. Fields beyond ID
// are optional: pre-ADR-0029 containers emit only ID+deadline+run-id; modern
// containers add Name (from --name) and BranchSlug (from sh.gsd-test.branch).
func parsePS(out []byte) []Container {
	var cs []Container
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		var c Container
		c.ID = fields[0]
		if len(fields) > 1 {
			if ms, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil {
				c.DeadlineMs = ms
			}
		}
		if len(fields) > 2 {
			c.RunID = fields[2]
		}
		if len(fields) > 3 {
			c.Name = fields[3]
		}
		if len(fields) > 4 {
			c.BranchSlug = fields[4]
		}
		cs = append(cs, c)
	}
	return cs
}

// Kill force-terminates a container by ID. `docker kill` tears down the whole
// container and every process in it identically on Linux and Windows — the
// cross-platform kill primitive from ADR-0021 Decision 4.
func Kill(ctx context.Context, run Runner, id string) error {
	if _, err := run(ctx, "kill", id); err != nil {
		return fmt.Errorf("reaper: kill %s: %w", id, err)
	}
	return nil
}

// isRunning reports whether a container with the given id is still present and
// running on the Bench. Used by Sweep to distinguish a benign already-exited
// container (kill fails, but it is already reaped) from a real kill failure.
func isRunning(ctx context.Context, run Runner, id string) (bool, error) {
	out, err := run(ctx, "ps", "-q", "--no-trunc", "--filter", "id="+id)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// OwnedBy returns, in input order, the containers whose BranchSlug matches
// branchSlug. An empty branchSlug returns every container unchanged — that is
// the operator escape hatch from ADR-0029: pass "" to scope to "every
// gsd-test container I ever labeled" (the pre-ADR-0029 behavior). Non-empty
// branchSlug scopes ownership to containers the current invocation actually
// works on, leaving containers from other branches for their own invocations.
// The match is on the sh.gsd-test.branch label value (Container.BranchSlug),
// not on parsing the human-readable Name, so it stays correct even when Docker
// substitutes a random name.
func OwnedBy(containers []Container, branchSlug string) []Container {
	if branchSlug == "" {
		return containers
	}
	var out []Container
	for _, c := range containers {
		if c.BranchSlug == branchSlug {
			out = append(out, c)
		}
	}
	return out
}

// Sweep lists run containers, selects those past their deadline at nowMs that
// belong to the named branch (empty branchSlug = all labeled containers,
// preserving the pre-ADR-0029 behavior), kills each, and returns the reaped
// slice. It tolerates already-gone containers — if a Kill fails but the
// container is no longer present (e.g. it exited and was removed by --rm, or a
// concurrent sweeper beat us to it), the error is suppressed and the sweep
// continues (#104). Only genuine kill failures (the container is still
// running) are returned as errors, joined via errors.Join so all remaining
// containers are still attempted.
func Sweep(ctx context.Context, run Runner, nowMs int64, branchSlug string) ([]Container, error) {
	containers, err := List(ctx, run)
	if err != nil {
		return nil, err
	}
	owned := OwnedBy(containers, branchSlug)
	overdue := Overdue(owned, nowMs)
	var errs []error
	for _, c := range overdue {
		if err := Kill(ctx, run, c.ID); err != nil {
			// A container that already exited (its own --rm self-removal, or a
			// concurrent sweeper) is already reaped — not a failure. Verify the
			// actual state before treating the kill error as fatal, and keep
			// reaping the rest either way (#104).
			if running, verr := isRunning(ctx, run, c.ID); verr == nil && !running {
				continue
			}
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return overdue, errors.Join(errs...)
	}
	return overdue, nil
}
